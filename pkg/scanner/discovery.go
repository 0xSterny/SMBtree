package scanner

import (
	"net"
	"smbtree/pkg/utils"
	"sync"
	"time"
)

// CheckHostLive attempts to connect to port 445 to see if the host is responsive for SMB
func CheckHostLive(ip string) bool {
	timeout := 800 * time.Millisecond
	conn, err := net.DialTimeout("tcp", ip+":445", timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// PerformDiscovery scans a list of candidates and returns only live hosts.
// It sends chunks of results to the provided channel to allow progressive UI updates.
func PerformDiscovery(candidates []utils.Host, results chan<- []utils.Host, threads int) {
	defer close(results)

	in := make(chan utils.Host, 100)
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Buffer for batching updates (e.g. every /24 or 256 hosts roughly)
	var batch []utils.Host
	batchSize := 10 // Smaller batch for responsiveness, or use logic based on subnet

	// Worker Pool
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var localBatch []utils.Host

			for h := range in {
				if CheckHostLive(h.IP) {
					localBatch = append(localBatch, h)
				}

				// If we have enough, or periodically, push to main batch logic?
				// To keep it simple, workers just find live ones.
				// The main coordinator should probably do batching?
				// Or workers push to a safe intermediate channel?
			}
			// Push trailing
			if len(localBatch) > 0 {
				// We can't push to 'results' directly from multiple workers without coordination if we want strict sizing
				// But we can just push smaller chunks.
				// However, user said "Update every /24".
				// Let's assume the order of processing roughly follows input.
			}
		}()
	}

	// This design with localBatch inside workers doesn't guarantee /24 grouping.
	// Alternative: Coordinate.
	// Simple approach:

	// Create a worker function that pings and returns bool
	sem := make(chan struct{}, threads) // Semaphore

	// Iterate candidates, launch goroutine per candidate, but limited by sem
	// Collect results

	// To support "Update every /24", we can group candidates by subnet first?
	// Or simply accumulate N live hosts and send them.

	// Let's iterate candidates and process.
	// We want to yield results periodically.

	resultChan := make(chan utils.Host, 100)

	go func() {
		for _, h := range candidates {
			sem <- struct{}{}
			wg.Add(1)
			go func(host utils.Host) {
				defer wg.Done()
				defer func() { <-sem }()
				if CheckHostLive(host.IP) {
					resultChan <- host
				}
			}(h)
		}
		wg.Wait()
		close(resultChan)
	}()

	// Collector
	for h := range resultChan {
		mu.Lock()
		batch = append(batch, h)
		limit := batchSize
		if len(batch) >= limit {
			// Send a copy
			toSend := make([]utils.Host, len(batch))
			copy(toSend, batch)
			results <- toSend
			batch = nil // Reset
		}
		mu.Unlock()
	}

	// Send remaining
	if len(batch) > 0 {
		results <- batch
	}
}
