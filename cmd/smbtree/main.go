package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"smbtree/pkg/exfil"
	"smbtree/pkg/queue"
	"smbtree/pkg/tui"
	"smbtree/pkg/utils"
	"time"

	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/net/proxy"
)

// reorderArgs moves flags to the front and non-flag arguments (targets) to the end
// This allows the user to specify targets anywhere in the command line
func reorderArgs(args []string) []string {
	var flags []string
	var positionals []string

	// Map of boolean flags (that don't take arguments)
	boolFlags := map[string]bool{
		"headless": true, "k": true, "no-pass": true, "no-limit": true, "blind": true, "b": true,
	}

	for i := 1; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			// It's a flag
			flags = append(flags, arg)

			// Check if it's a value flag (takes an argument)
			// Handle --flag=value case
			if strings.Contains(arg, "=") {
				continue
			}

			// Clean dashes
			name := strings.TrimLeft(arg, "-")
			if !boolFlags[name] {
				// It consumes the next argument as value
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					flags = append(flags, args[i+1])
					i++
				}
			}
		} else {
			// Positional argument
			positionals = append(positionals, arg)
		}
	}

	// Reconstruct args: [ProgramName] + [Flags...] + [Positionals...]
	newArgs := []string{args[0]}
	newArgs = append(newArgs, flags...)
	newArgs = append(newArgs, positionals...)
	return newArgs
}

func main() {
	// Custom Arg Parsing to allow interleaved flags/targets
	os.Args = reorderArgs(os.Args)

	// 1. Auth Flags
	username := flag.String("u", "", "Username")
	password := flag.String("p", "", "Password")
	domain := flag.String("d", "", "Domain")
	hash := flag.String("H", "", "NTLM Hash")
	kerberos := flag.Bool("k", false, "Use Kerberos")
	noPass := flag.Bool("no-pass", false, "Don't ask for password (use empty or guest)")
	authHoldStr := flag.String("auth-hold", "true", "Hold SMB sessions open (persistent connection) to reduce logs")
	flag.StringVar(authHoldStr, "a", "true", "Hold SMB sessions open (alias)")

	// 2. Delay / OpSec
	authDuration := flag.String("auth-duration", "0s", "Duration to spread SMB authentication/tree (e.g. 60m)")
	exfilDuration := flag.String("exfil-duration", "0s", "Duration to spread Exfiltration/Pull (e.g. 120m)")
	fileJitter := flag.String("file-jitter", "0s", "Jitter/Delay between directory reads (e.g. 100ms)")
	flag.StringVar(fileJitter, "j", "0s", "Jitter/Delay between directory reads (alias)")

	// OPSEC Logic
	safeSharesStr := flag.String("safe-shares", "true", "Skip administrative shares (C$, ADMIN$, IPC$)")
	flag.StringVar(safeSharesStr, "s", "true", "Skip administrative shares (alias)")

	blindMode := flag.Bool("blind", false, "Blind Mode: Skip share access checks (reduces log noise)")
	flag.BoolVar(blindMode, "b", false, "Blind Mode (alias)")

	// Concurrency
	threads := flag.Int("t", 10, "Number of concurrent worker threads")
	noLimit := flag.Bool("no-limit", false, "Disable all time delays (overrides durations to 0)")

	// 3. Exfil Flags
	exfilMethod := flag.String("exfil-method", "local", "Exfiltration method: local, http")
	exfilURL := flag.String("exfil-url", "", "HTTP URL for exfiltration")
	lootDir := flag.String("l", "loot", "Local directory for looted files")
	flag.StringVar(lootDir, "loot", "loot", "Local directory for looted files (alias)")

	// 4. Mode
	headlessMode := flag.Bool("headless", false, "Run in headless scan mode")
	depth := flag.Int("D", 2, "Depth of directory crawl (default 2)")

	// 5. Network
	proxyURL := flag.String("proxy", "", "SOCKS5 Proxy URL (e.g. socks5://127.0.0.1:9050)")
	timeoutStr := flag.String("timeout", "5s", "Connection timeout (default 5s)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <target>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Target: File path, IP/Hostname, or CIDR range.\n\n")

		fmt.Fprintln(os.Stderr, "Authentication Flags:")
		fmt.Fprintf(os.Stderr, "  -u string\n    \tUsername\n")
		fmt.Fprintf(os.Stderr, "  -p string\n    \tPassword\n")
		fmt.Fprintf(os.Stderr, "  -d string\n    \tDomain\n")
		fmt.Fprintf(os.Stderr, "  -H string\n    \tNTLM Hash\n")
		fmt.Fprintf(os.Stderr, "  -k\tUse Kerberos\n")
		fmt.Fprintf(os.Stderr, "  -no-pass\n    \tDon't ask for password (use empty or guest)\n")
		fmt.Fprintf(os.Stderr, "  -a, --auth-hold string\n    \tHold SMB sessions open (persistent connection) (default true)\n")

		fmt.Fprintln(os.Stderr, "\nDelay & Concurrency Flags:")
		fmt.Fprintf(os.Stderr, "  -auth-duration string\n    \tSpread SMB auth/tree over time (e.g. 60m)\n")
		fmt.Fprintf(os.Stderr, "  -exfil-duration string\n    \tSpread Exfil/Pull over time (e.g. 120m)\n")
		fmt.Fprintf(os.Stderr, "  -j, --file-jitter string\n    \tJitter/Delay between directory reads (e.g. 100ms)\n")
		fmt.Fprintf(os.Stderr, "  -t int\n    \tNumber of concurrent threads (default 10)\n")
		fmt.Fprintf(os.Stderr, "  -no-limit\n    \tDisable all time delays (Go burr)\n")
		fmt.Fprintf(os.Stderr, "  -D int\n    \tRecursion depth for directories (default 2)\n")

		fmt.Fprintln(os.Stderr, "\nOPSEC Flags:")
		fmt.Fprintf(os.Stderr, "  -s, --safe-shares string\n    \tSkip administrative shares (C$, ADMIN$, IPC$) (default true)\n")
		fmt.Fprintf(os.Stderr, "  -b, --blind\n    \tBlind Mode: Skip share access checks\n")

		fmt.Fprintln(os.Stderr, "\nExfiltration Flags:")
		fmt.Fprintf(os.Stderr, "  -exfil-method string\n    \tMethod: local, http (default local)\n")
		fmt.Fprintf(os.Stderr, "  -exfil-url string\n    \tHTTP URL for exfiltration\n")
		fmt.Fprintf(os.Stderr, "  -l, --loot string\n    \tLocal directory for looted files (default 'loot')\n")

		fmt.Fprintln(os.Stderr, "\nMode Flags:")
		fmt.Fprintf(os.Stderr, "  -headless\n    \tRun in headless scan mode\n")
		fmt.Fprintf(os.Stderr, "  -no-ping\n    \tDisable ping sweep/live host discovery\n")

		fmt.Fprintln(os.Stderr, "\nNetwork Flags:")
		fmt.Fprintf(os.Stderr, "  -proxy string\n    \tSOCKS5 Proxy URL (e.g. socks5://127.0.0.1:9050)\n")
		fmt.Fprintf(os.Stderr, "  -timeout string\n    \tConnection timeout (default 5s)\n")
	}

	flag.Parse()

	// Positional args
	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}
	targetInput := args[0]

	hosts, err := utils.ParseInput(targetInput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not load targets: %v\n", err)
		os.Exit(1)
	}

	// Parse Timeout
	timeout, err := time.ParseDuration(*timeoutStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid timeout: %v\n", err)
		os.Exit(1)
	}

	// Calculate Auth Jitter/Schedule
	var authDur time.Duration
	if *authDuration != "0s" {
		var err error
		authDur, err = time.ParseDuration(*authDuration)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid auth-duration: %v\n", err)
			os.Exit(1)
		}
	}

	// Exfil Duration will be passed to Model
	var exfilDur time.Duration
	if *exfilDuration != "0s" {
		var err error
		exfilDur, err = time.ParseDuration(*exfilDuration)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid exfil-duration: %v\n", err)
			os.Exit(1)
		}
	}

	// Logic for --no-limit
	if *noLimit {
		authDur = 0
		exfilDur = 0
		fmt.Println("No-Limit mode active: Disabling time delays and executing concurrently.")
	}

	// Apply Schedule to Hosts for initial Tree/Auth
	rand.Seed(time.Now().UnixNano())
	now := time.Now()
	for i := range hosts {
		if authDur > 0 {
			// Random delay within [0, authDur)
			jitter := time.Duration(rand.Int63n(int64(authDur)))
			hosts[i].ScheduledTime = now.Add(jitter)
		} else {
			hosts[i].ScheduledTime = now
		}
	}

	// Build Configs
	exfilCfg := exfil.Config{
		Method: *exfilMethod,
		URL:    *exfilURL,
	}

	// Construct Global Creds
	globalCreds := utils.Credential{
		Username: *username,
		Password: *password,
		Domain:   *domain,
		Hash:     *hash,
	}

	if *kerberos {
		globalCreds.AuthType = "kerberos"
	} else if *hash != "" {
		globalCreds.AuthType = "hash"
	} else if *noPass {
		// Could mean Guest or just empty password
		if *username == "" {
			globalCreds.AuthType = "guest"
		} else {
			globalCreds.AuthType = "password" // with empty pass
		}
	} else {
		// Default means nothing specified globally regarding auth type
		// But if password/user is set, use it.
		if *password != "" || *username != "" {
			globalCreds.AuthType = "password"
		}
	}

	hosts = utils.ApplyGlobalCreds(hosts, globalCreds)

	jitter, _ := time.ParseDuration(*fileJitter)
	authHold := parseBool(*authHoldStr)
	safeShares := parseBool(*safeSharesStr)

	// Setup Proxy
	var dialer proxy.Dialer
	if *proxyURL != "" {
		u, err := url.Parse(*proxyURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid proxy URL: %v\n", err)
			os.Exit(1)
		}
		dialer, err = proxy.FromURL(u, proxy.Direct)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create proxy dialer: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Using Proxy: %s\n", *proxyURL)
	}

	if *headlessMode {
		runHeadless(hosts, exfilCfg, *threads, *depth, *lootDir, authHold, safeShares, *blindMode, jitter, dialer, timeout)
		return
	}

	// Reverted to always loading hosts directly due to Discovery TUI issues
	// We will handle liveness checking JIT in the scanner
	m := tui.NewModel(hosts, exfilCfg, exfilDur, *threads, *depth, *lootDir, authHold, safeShares, *blindMode, jitter, dialer, timeout)

	p := tea.NewProgram(m, tea.WithAltScreen())

	if err := p.Start(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func runHeadless(hosts []utils.Host, exfilCfg exfil.Config, workerCount int, depth int, lootDir string, authHold bool, safeShares bool, blindMode bool, jitter time.Duration, dialer proxy.Dialer, timeout time.Duration) {
	fmt.Println("Starting headless scan...")
	s := queue.NewScheduler(workerCount, exfilCfg, authHold, safeShares, blindMode, jitter, dialer, timeout)
	s.SetLootDir(lootDir)
	s.Start()

	// 1. Auth & Queue Scans
	results := make(map[string]*utils.Host)
	for i := range hosts {
		results[hosts[i].IP] = &hosts[i]

		// Auth check (synchronous or via queue? Headless -> synchronous is easier or just push all)
		// We'll trust the scheduler to handle it.
		// Actually, standard scheduler logic expects TREE job.
		// We need to trigger Tree job.

		job := utils.QueueItem{
			ID:         fmt.Sprintf("scan-%s", hosts[i].IP),
			ActionType: "TREE",
			HostIP:     hosts[i].IP,
			Host:       &hosts[i],
			Priority:   true,
			DepthParam: depth,
		}
		s.Queue <- job
	}

	// 2. Wait for results
	// Simple timeout based wait or counter?
	// We know how many hosts.
	completed := 0
	total := len(hosts)
	scanTimeoutCh := time.After(30 * time.Second)

	for completed < total {
		select {
		case msg := <-s.Output:
			// Cast to JobResult?
			// tea.Msg is interface{}.
			if res, ok := msg.(utils.JobResult); ok {
				fmt.Printf("[%s] Finished: Success=%v Error=%v\n", res.HostIP, res.Success, res.Error)
				if h, exists := results[res.HostIP]; exists {
					if res.Success {
						h.Status = utils.StatusComplete
						h.Shares = res.Shares
					} else {
						h.Status = utils.StatusError
						h.ErrorMsg = res.Error
					}
				}
				if res.ActionType == "TREE" {
					completed++
				}
			}
		case <-scanTimeoutCh:
			fmt.Println("Scan timed out.")
			completed = total // force exit
		}
	}

	// 3. Export
	utils.GenerateReport(hosts)
	fmt.Println("Report generated.")
}

func parseBool(s string) bool {
	s = strings.ToLower(s)
	return s == "true" || s == "t" || s == "1" || s == "yes" || s == "y"
}
