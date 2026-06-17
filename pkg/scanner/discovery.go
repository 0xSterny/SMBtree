package scanner

import (
	"net"
	"github.com/0xSterny/SMBtree/pkg/utils"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// CheckHostLive attempts to connect to port 445 to see if the host is responsive for SMB
func CheckHostLive(ip string, dialer proxy.Dialer, timeout time.Duration) bool {
	target := ip + ":445"
	if dialer != nil {
		// Just try dialing
		c, err := dialer.Dial("tcp", target)
		if err == nil {
			c.Close()
			return true
		}
		return false
	}

	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// PerformDiscovery scans a list of candidates and returns only live hosts.
func PerformDiscovery(candidates []utils.Host, results chan<- []utils.Host, threads int, dialer proxy.Dialer, timeout time.Duration) {
	defer close(results)

	var wg sync.WaitGroup
	var mu sync.Mutex

	// Buffer for batching updates
	var batch []utils.Host
	batchSize := 10

	sem := make(chan struct{}, threads) // Semaphore

	resultChan := make(chan utils.Host, 100)

	go func() {
		for _, h := range candidates {
			sem <- struct{}{}
			wg.Add(1)
			go func(host utils.Host) {
				defer wg.Done()
				defer func() { <-sem }()
				if CheckHostLive(host.IP, dialer, timeout) {
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
