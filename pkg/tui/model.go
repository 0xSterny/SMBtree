package tui

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"github.com/0xSterny/SMBtree/pkg/exfil"
	"github.com/0xSterny/SMBtree/pkg/loot"
	"github.com/0xSterny/SMBtree/pkg/queue"
	"github.com/0xSterny/SMBtree/pkg/scanner"
	"github.com/0xSterny/SMBtree/pkg/utils"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/net/proxy"
)

// TreeRow unifies Hosts and FileNodes for the Global Tree View
type TreeRow struct {
	Label       string
	Depth       int
	Expanded    bool
	Selected    bool
	IsHost      bool
	HostIdx     int             // index in m.Hosts
	FileNodePtr *utils.FileNode // Pointer to the actual node if not a host
	Permissions utils.PermFlags // Permissions for the node, useful for color
}

type TickMsg time.Time

func doTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

type activeView int

const (
	viewHosts activeView = iota
	viewTree
	viewLog
	viewLoot
	viewQueue
)

type Model struct {
	Hosts             []utils.Host
	ActiveTab         activeView
	HostCursor        int
	TreeCursor        int
	SelectedHostIndex int
	Scheduler         *queue.Scheduler

	Ready         bool
	ExfilDuration time.Duration
	Logs          []string
	ScanDepth     int

	// Scrolling
	WindowHeight  int
	WindowWidth   int
	ListHeight    int
	HostsScroll   int
	TreeScroll    int
	LogScroll     int
	LogAutoScroll bool

	// Notifications
	Notification string

	// Loot Viewer
	LootDir      string
	LootNodes    []*utils.FileNode
	LootCursor   int
	LootScroll   int
	LootViewport viewport.Model
	LootLoaded   bool

	// Queue View
	QueueScroll int
	QueueCursor int

	// Discovery
	PendingHosts    []utils.Host
	DiscoveryChan   chan []utils.Host
	DiscoveryActive bool

	// Network
	Dialer  proxy.Dialer
	Timeout time.Duration
}

type ClearNotificationMsg struct{}

func (m Model) notify(msg string) tea.Cmd {
	return func() tea.Msg {
		// Wait 3 seconds then clear
		time.Sleep(3 * time.Second)
		return ClearNotificationMsg{}
	}
}

type AuthMsg struct {
	Index   int
	Success bool
	Error   string
}

type HostsFoundMsg []utils.Host
type DiscoveryDoneMsg struct{}

func RunDiscovery(candidates []utils.Host, threads int) tea.Cmd {
	return func() tea.Msg {
		// We can't use a channel easily in a single tea.Cmd return
		// unless we use a "subscription" command or loop.
		// Bubbletea Sub vs Cmd.
		// For simple "stream", we can start a goroutine that sends Msgs to the Program?
		// But tea.Cmd returns a single Msg.
		// Wait, if we want multiple updates, we need a subscription (tea.Sub).
		// Or we chain Cmds.

		// Actually, simpler integration:
		// Model has a channel "DiscoveryChan".
		// Init() starts PerformDiscovery goroutine pushing to that channel.
		// And we have a "waitForDiscovery" Cmd that listens.
		return nil
	}
}

func waitForDiscovery(ch <-chan []utils.Host) tea.Cmd {
	return func() tea.Msg {
		hosts, ok := <-ch
		if !ok {
			return DiscoveryDoneMsg{}
		}
		return HostsFoundMsg(hosts)
	}
}

func waitForScheduler(s *queue.Scheduler) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-s.Output
		if !ok {
			return nil
		}
		return msg
	}
}

func NewModel(hosts []utils.Host, exfilCfg exfil.Config, exfilDur time.Duration, workerCount int, depth int, lootDir string, authHold bool, safeShares bool, blindMode bool, jitter time.Duration, dialer proxy.Dialer, timeout time.Duration) Model {
	s := queue.NewScheduler(workerCount, exfilCfg, authHold, safeShares, blindMode, jitter, dialer, timeout)
	s.SetLootDir(lootDir)
	s.Start()

	return Model{
		Hosts:         hosts,
		ActiveTab:     viewHosts,
		Scheduler:     s,
		ExfilDuration: exfilDur,
		ScanDepth:     depth,
		ListHeight:    20, // Default fallback
		LootDir:       lootDir,
		LootViewport:  viewport.New(80, 20),
		PendingHosts:  nil, // Set manually or via helper
		DiscoveryChan: make(chan []utils.Host, 10),
		LogAutoScroll: true,
		Dialer:        dialer,
		Timeout:       timeout,
	}
}

func (m *Model) SetPendingHosts(hosts []utils.Host) {
	m.PendingHosts = hosts
	// m.Hosts should remain empty if we are discovering
}

func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd

	if len(m.PendingHosts) > 0 {
		// Discovery Mode
		m.DiscoveryActive = true
		go scanner.PerformDiscovery(m.PendingHosts, m.DiscoveryChan, 100, m.Dialer, m.Timeout)
		cmds = append(cmds, waitForDiscovery(m.DiscoveryChan))
		m.Logs = append(m.Logs, fmt.Sprintf("[%s] Starting Ping Sweep on %d targets...", time.Now().Format("15:04:05"), len(m.PendingHosts)))
	} else {
		// No Discovery: Start queueing existing hosts immediately
		for i := range m.Hosts {
			h := &m.Hosts[i]
			// Mark as pending if not already
			// Mark as pending if not already
			// h.Status defaults to StatusPending (0) so this is unnecessary to check against string
			// if h.Status == "" { h.Status = utils.StatusPending }
			job := utils.QueueItem{
				ID:            fmt.Sprintf("scan-%s", h.IP),
				ActionType:    "TREE",
				HostIP:        h.IP,
				Host:          h,
				Priority:      false,
				ScheduledTime: h.ScheduledTime,
				DepthParam:    m.ScanDepth,
			}
			m.Scheduler.Queue <- job
		}
	}

	// Start listener
	cmds = append(cmds, waitForScheduler(m.Scheduler))
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case ClearNotificationMsg:
		m.Notification = ""
	case TickMsg:
		if m.ActiveTab == viewQueue {
			cmds = append(cmds, doTick())
		}
	case tea.WindowSizeMsg:
		m.WindowHeight = msg.Height
		m.WindowWidth = msg.Width
		m.ListHeight = msg.Height - 6 // Header(4) + Footer(2)
		if m.ListHeight < 1 {
			m.ListHeight = 1
		}
		m.resizeLootViewport()

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.ActiveTab++
			if m.ActiveTab > viewQueue { // Cycle 0..4
				m.ActiveTab = viewHosts
			}
			if m.ActiveTab == viewLoot {
				m.reloadLoot()
			}
			if m.ActiveTab == viewQueue {
				cmds = append(cmds, doTick())
			}

		case "j", "down":
			if m.ActiveTab == viewHosts {
				visibleIndices := m.getVisibleHosts()
				if m.HostCursor < len(visibleIndices)-1 {
					m.HostCursor++
					if m.HostCursor >= m.HostsScroll+m.ListHeight {
						m.HostsScroll++
					}
				}
			} else if m.ActiveTab == viewTree {
				visibleRows := m.getTreeItems()
				if m.TreeCursor < len(visibleRows)-1 {
					m.TreeCursor++
					if m.TreeCursor >= m.TreeScroll+m.ListHeight {
						m.TreeScroll++
					}
				}
			} else if m.ActiveTab == viewLoot {
				nodes := flattenNodes(m.LootNodes)
				if m.LootCursor < len(nodes)-1 {
					m.LootCursor++
					if m.LootCursor >= m.LootScroll+m.ListHeight {
						m.LootScroll++
					}
				}
			} else if m.ActiveTab == viewQueue {
				queueItems := m.Scheduler.GetQueueSnapshot()
				if m.QueueCursor < len(queueItems)-1 {
					m.QueueCursor++
					if m.QueueCursor >= m.QueueScroll+m.ListHeight {
						m.QueueScroll++
					}
				}
			} else if m.ActiveTab == viewLog {
				if !m.LogAutoScroll {
					m.LogScroll++
					// If we hit bottom, enable auto-scroll
					if m.LogScroll >= len(m.Logs)-m.ListHeight {
						m.LogAutoScroll = true
					}
				}
			}

		case "k", "up":
			if m.ActiveTab == viewHosts {
				if m.HostCursor > 0 {
					m.HostCursor--
					if m.HostCursor < m.HostsScroll {
						m.HostsScroll--
					}
				}
			} else if m.ActiveTab == viewTree {
				if m.TreeCursor > 0 {
					m.TreeCursor--
					if m.TreeCursor < m.TreeScroll {
						m.TreeScroll--
					}
				}
			} else if m.ActiveTab == viewLoot {
				if m.LootCursor > 0 {
					m.LootCursor--
					if m.LootCursor < m.LootScroll {
						m.LootScroll--
					}
				}
			} else if m.ActiveTab == viewQueue {
				if m.QueueCursor > 0 {
					m.QueueCursor--
					if m.QueueCursor < m.QueueScroll {
						m.QueueScroll--
					}
				}
			} else if m.ActiveTab == viewLog {
				if m.LogAutoScroll {
					m.LogAutoScroll = false
					m.LogScroll = len(m.Logs) - m.ListHeight - 1
					if m.LogScroll < 0 {
						m.LogScroll = 0
					}
				} else {
					if m.LogScroll > 0 {
						m.LogScroll--
					}
				}
			}

		case "pgdown", "pagedown":
			if m.ActiveTab == viewLoot {
				m.LootViewport.ViewDown()
			}

		case "pgup", "pageup":
			if m.ActiveTab == viewLoot {
				m.LootViewport.ViewUp()
			}

		case "enter":
			if m.ActiveTab == viewTree {
				visibleRows := m.getTreeItems()
				if m.TreeCursor < len(visibleRows) {
					row := visibleRows[m.TreeCursor]
					if row.IsHost {
						// Toggle host expansion
						m.Hosts[row.HostIdx].Expanded = !m.Hosts[row.HostIdx].Expanded
					} else if row.FileNodePtr != nil && row.FileNodePtr.IsDir {
						row.FileNodePtr.Expanded = !row.FileNodePtr.Expanded
					}
				}
			} else if m.ActiveTab == viewLoot {
				if cmd := m.handleLootEnter(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}

		case " ":
			if m.ActiveTab == viewTree {
				visibleRows := m.getTreeItems()
				if m.TreeCursor < len(visibleRows) {
					row := visibleRows[m.TreeCursor]
					if !row.IsHost && row.FileNodePtr != nil {
						row.FileNodePtr.Selected = !row.FileNodePtr.Selected

						status := "Selected"
						if !row.FileNodePtr.Selected {
							status = "Deselected"
						}
						m.Notification = fmt.Sprintf("%s %s", status, row.FileNodePtr.Name)
						cmds = append(cmds, m.notify(m.Notification))
					}
				}
			}

		case "p": // Pull / Execute Batch
			if m.ActiveTab == viewTree {
				if cmd := m.queueBatchPull(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}

		case "f": // Force
			if m.ActiveTab == viewHosts {
				if cmd := m.forceExecute(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.ActiveTab == viewTree {
				if cmd := m.forceExecute(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else if m.ActiveTab == viewQueue {
				// Queue Force
				snapshot := m.Scheduler.GetQueueSnapshot()
				if m.QueueCursor < len(snapshot) { // Use Cursor!
					item := snapshot[m.QueueCursor]
					m.Scheduler.PrioritizeJob(item.ID)
					m.Notification = "Prioritized Job: " + item.ID
					cmds = append(cmds, m.notify(m.Notification))
				}
			}

		case "d", "D": // Depth Expand

			if cmd := m.expandDepth(); cmd != nil {
				cmds = append(cmds, cmd)
			}

		case "x":
			// Generate Report
			utils.GenerateReport(m.Hosts)
			m.Notification = "Generated Report"
			cmds = append(cmds, m.notify(m.Notification))

		case "z": // Unzip/Extract
			if m.ActiveTab == viewLoot {
				if cmd := m.handleLootExtract(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}

	case AuthMsg:
		if msg.Index >= 0 && msg.Index < len(m.Hosts) {
			if msg.Success {
				m.Hosts[msg.Index].Status = utils.StatusAuthenticating // Pending scan

				// Queue scan
				job := utils.QueueItem{
					ID:         fmt.Sprintf("scan-%d", msg.Index),
					ActionType: "TREE",
					HostIP:     m.Hosts[msg.Index].IP,
					Host:       &m.Hosts[msg.Index],
					Priority:   false,
				}

				// Non-blocking push
				go func() {
					m.Scheduler.Queue <- job
				}()
				m.Hosts[msg.Index].Status = utils.StatusPending // Waiting for worker
			} else {
				m.Hosts[msg.Index].Status = utils.StatusError
				m.Hosts[msg.Index].ErrorMsg = msg.Error
			}
		}

	case utils.JobResult: // Received from Scheduler
		// Log it
		status := "OK"
		if !msg.Success {
			status = "ERR"
		}
		logEntry := fmt.Sprintf("[%s] %s %s %s \"%s\" %s", time.Now().Format("15:04:05"), status, msg.ActionType, msg.HostIP, msg.Target, msg.Error)
		m.Logs = append(m.Logs, logEntry)

		// Find host by IP
		var idx = -1
		for i, h := range m.Hosts {
			if h.IP == msg.HostIP {
				idx = i
				break
			}
		}

		if idx != -1 {
			if msg.ActionType == "PULL" {
				// Handle Pull result (e.g. log status)
				// For now, assume success means downloaded
			} else if msg.ActionType == "TREE" {
				if msg.Success {
					m.Hosts[idx].Status = utils.StatusComplete
					m.Hosts[idx].Shares = msg.Shares
				} else {
					m.Hosts[idx].Status = utils.StatusError
					m.Hosts[idx].ErrorMsg = msg.Error
				}
			} else if msg.ActionType == "EXPAND_DIR" {
				// We need to attach children to the right node
				if msg.Success && len(msg.Shares) > 0 {
					share, path := utils.SplitSharePath(msg.Target)
					// Normalize path separators to always use / as internal tree uses /
					path = strings.ReplaceAll(path, "\\", "/")

					node := findNodeInShare(m.Hosts[idx].Shares, share, path)
					if node != nil {

						// Fix Depth relative to parent
						fixDepth(msg.Shares, node.Depth)
						node.Children = msg.Shares
						node.Expanded = true

						m.Notification = fmt.Sprintf("Expanded %s (1 level)", node.Name)
						cmds = append(cmds, m.notify(m.Notification))
					} else {
						m.Notification = fmt.Sprintf("Expand Fail: Node not found (%s)", path)
						cmds = append(cmds, m.notify(m.Notification))
					}
				}
			}
		} else {
			// Log unknown host?
			if msg.ActionType == "EXPAND_DIR" {
				m.Notification = fmt.Sprintf("Expand Fail: Host IP not found (%s)", msg.HostIP)
				cmds = append(cmds, m.notify(m.Notification))
			}
		}

		// Re-subscribe
		cmds = append(cmds, waitForScheduler(m.Scheduler))

	case HostsFoundMsg:
		incoming := []utils.Host(msg)
		var newHosts []utils.Host

		// Deduplicate: Don't add if IP already exists
		existingMap := make(map[string]bool)
		for _, h := range m.Hosts {
			existingMap[h.IP] = true
		}

		for _, h := range incoming {
			if !existingMap[h.IP] {
				newHosts = append(newHosts, h)
				existingMap[h.IP] = true
			}
		}

		startIdx := len(m.Hosts)
		m.Hosts = append(m.Hosts, newHosts...)

		// Log reception
		m.Logs = append(m.Logs, fmt.Sprintf("[%s] Debug: Received HostsFoundMsg with %d hosts. Total now: %d", time.Now().Format("15:04:05"), len(newHosts), len(m.Hosts)))

		for i := range newHosts {
			idx := startIdx + i
			h := &m.Hosts[idx]
			h.Status = utils.StatusPending

			job := utils.QueueItem{
				ID:            fmt.Sprintf("scan-%s", h.IP),
				ActionType:    "TREE",
				HostIP:        h.IP,
				Host:          h,
				Priority:      false,
				ScheduledTime: h.ScheduledTime,
				DepthParam:    m.ScanDepth,
			}
			m.Scheduler.Queue <- job
		}

		cmds = append(cmds, waitForDiscovery(m.DiscoveryChan))
		// Log
		m.Logs = append(m.Logs, fmt.Sprintf("[%s] DISCOVERY Found %d live hosts", time.Now().Format("15:04:05"), len(newHosts)))

	case DiscoveryDoneMsg:
		m.DiscoveryActive = false
		m.Logs = append(m.Logs, fmt.Sprintf("[%s] DISCOVERY Complete.", time.Now().Format("15:04:05")))
		if len(m.Hosts) == 0 {
			m.Logs = append(m.Logs, fmt.Sprintf("[%s] WARNING: No live hosts found.", time.Now().Format("15:04:05")))
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) toggleExpand() {
	if m.SelectedHostIndex < 0 || m.SelectedHostIndex >= len(m.Hosts) {
		return
	}
	host := &m.Hosts[m.SelectedHostIndex]
	nodes := flattenNodes(host.Shares)
	if m.TreeCursor < len(nodes) {
		nodes[m.TreeCursor].Expanded = !nodes[m.TreeCursor].Expanded
	}
}

func (m *Model) toggleSelect(autoQueue bool) tea.Cmd {
	if m.SelectedHostIndex < 0 || m.SelectedHostIndex >= len(m.Hosts) {
		return nil
	}
	host := &m.Hosts[m.SelectedHostIndex]
	nodes := flattenNodes(host.Shares)
	if m.TreeCursor < len(nodes) {
		node := nodes[m.TreeCursor]
		node.Selected = !node.Selected
		// Removed auto-queue logic

		status := "Selected"
		if !node.Selected {
			status = "Deselected"
		}
		m.Notification = fmt.Sprintf("%s %s", status, node.Name)
		return m.notify(m.Notification)
	}
	return nil
}

func (m *Model) queueBatchPull() tea.Cmd {
	// iterate all hosts, all nodes, find selected
	rand.Seed(time.Now().UnixNano())
	now := time.Now()
	count := 0

	for i := range m.Hosts {
		host := &m.Hosts[i]
		// recursively find selected nodes
		var selected []*utils.FileNode
		collectSelected(host.Shares, &selected)

		for _, node := range selected {
			count++
			// Calculate Schedule
			schedTime := now
			if m.ExfilDuration > 0 {
				jitter := time.Duration(rand.Int63n(int64(m.ExfilDuration)))
				schedTime = now.Add(jitter)
			}

			job := utils.QueueItem{
				ID:            fmt.Sprintf("pull-%s-%s", host.IP, node.Path),
				ActionType:    "PULL",
				HostIP:        host.IP,
				Host:          host,
				Target:        filepath.Join(node.ShareName, node.Path),
				Priority:      false,
				ScheduledTime: schedTime,
			}
			go func(j utils.QueueItem) {
				m.Scheduler.Queue <- j
			}(job)

			node.Selected = false // Uncheck
		}
	}
	if count > 0 {
		m.Notification = fmt.Sprintf("Queued %d files for pull", count)
		return m.notify(m.Notification)
	}
	return nil
}

func (m *Model) forceExecute() tea.Cmd {
	// Priority = True, ScheduledTime = Now
	if m.ActiveTab == viewHosts {
		// Force Auth/Scan
		visibleIndices := m.getVisibleHosts()
		if m.HostCursor >= 0 && m.HostCursor < len(visibleIndices) {
			realIdx := visibleIndices[m.HostCursor]
			host := &m.Hosts[realIdx]
			job := utils.QueueItem{
				ID:            fmt.Sprintf("scan-%s-force", host.IP),
				ActionType:    "TREE",
				HostIP:        host.IP,
				Host:          host,
				Priority:      true,
				ScheduledTime: time.Now(),
			}
			go func() { m.Scheduler.Queue <- job }()
			host.Status = utils.StatusPending
			m.Notification = "Forced Scan on " + host.IP
			return m.notify(m.Notification)
		}
	} else if m.ActiveTab == viewTree {
		// Force Pull of highlighted item (if file)
		visibleRows := m.getTreeItems()
		if m.TreeCursor < len(visibleRows) {
			row := visibleRows[m.TreeCursor]
			if !row.IsHost && row.FileNodePtr != nil {
				host := &m.Hosts[row.HostIdx]
				node := row.FileNodePtr
				job := utils.QueueItem{
					ID:            fmt.Sprintf("pull-%s-force", host.IP),
					ActionType:    "PULL",
					HostIP:        host.IP,
					Host:          host,
					Target:        filepath.Join(node.ShareName, node.Path),
					Priority:      true,
					ScheduledTime: time.Now(),
				}
				go func() { m.Scheduler.Queue <- job }()
				m.Notification = "Forced Pull " + node.Name
				return m.notify(m.Notification)
			}
		}
	}
	return nil
}

func (m *Model) expandDepth() tea.Cmd {
	if m.ActiveTab == viewHosts {
		visibleIndices := m.getVisibleHosts()
		if m.HostCursor >= 0 && m.HostCursor < len(visibleIndices) {
			// Trigger TREE scan with Depth + 1
			realIdx := visibleIndices[m.HostCursor]
			host := &m.Hosts[realIdx]
			job := utils.QueueItem{
				ID:            fmt.Sprintf("scan-%s-deep", host.IP),
				ActionType:    "TREE",
				HostIP:        host.IP,
				Host:          host,
				Priority:      true,
				DepthParam:    m.ScanDepth + 1, // Go deeper
				ScheduledTime: time.Now(),
			}
			go func() { m.Scheduler.Queue <- job }()
			host.Status = utils.StatusPending

			m.Notification = "Deep scanning " + host.IP
			return m.notify(m.Notification)
		}
	} else if m.ActiveTab == viewTree {
		visibleRows := m.getTreeItems()

		if m.TreeCursor < len(visibleRows) {
			row := visibleRows[m.TreeCursor]

			if !row.IsHost && row.FileNodePtr != nil && row.FileNodePtr.IsDir {
				// Expand this dir
				// Verify HostIdx
				if row.HostIdx < 0 || row.HostIdx >= len(m.Hosts) {
					return nil
				}

				host := &m.Hosts[row.HostIdx]
				node := row.FileNodePtr

				job := utils.QueueItem{
					ID:            fmt.Sprintf("expand-%s-%s", host.IP, node.Path),
					ActionType:    "EXPAND_DIR",
					HostIP:        host.IP,
					Host:          host,
					Target:        filepath.Join(node.ShareName, node.Path),
					Priority:      true,
					DepthParam:    1,
					ScheduledTime: time.Now(),
				}
				go func() { m.Scheduler.Queue <- job }()

				m.Notification = fmt.Sprintf("Expanding %s (Depth %d)", node.Name, node.Depth+1)
				return m.notify(m.Notification)
			}
		}
	}
	return nil
}

func findNodeInShare(nodes []*utils.FileNode, share, path string) *utils.FileNode {
	for _, n := range nodes {
		// Roots are shares.
		if n.ShareName == share {
			// Path in n is path relative to share root?
			// Actually ScanHost stores shareName as Path on root.
			// Path="C$", Name="C$".
			// But child's path is "Windows".
			// If path is empty string, we want root.
			if path == "" {
				return n
			}
			return findNodeByPath([]*utils.FileNode{n}, path) // Search children
		}
	}
	return nil
}

func findNodeByPath(nodes []*utils.FileNode, path string) *utils.FileNode {
	for _, n := range nodes {
		if n.Path == path {
			return n
		}
		found := findNodeByPath(n.Children, path)
		if found != nil {
			return found
		}
	}
	return nil
}

func collectSelected(nodes []*utils.FileNode, result *[]*utils.FileNode) {
	for _, n := range nodes {
		if n.Selected {
			*result = append(*result, n)
		}
		collectSelected(n.Children, result)
	}
}

func fixDepth(nodes []*utils.FileNode, parentDepth int) {
	for _, n := range nodes {
		n.Depth += parentDepth
		fixDepth(n.Children, parentDepth)
	}
}

func flattenNodes(nodes []*utils.FileNode) []*utils.FileNode {
	var result []*utils.FileNode
	for _, n := range nodes {
		result = append(result, n)
		if n.Expanded && n.IsDir {
			result = append(result, flattenNodes(n.Children)...)
		}
	}
	return result
}

// getTreeItems builds a linear list of visible items (Hosts + expanded children)
func (m Model) getTreeItems() []TreeRow {
	var rows []TreeRow
	visibleIndices := m.getVisibleHosts()

	for _, i := range visibleIndices {
		h := m.Hosts[i]
		// Add Host Row
		label := fmt.Sprintf("%s (%s)", h.IP, h.Hostname)
		if h.Hostname == "" {
			label = h.IP
		}

		// If scanning/failed, maybe add status?
		status := ""
		if h.Status == utils.StatusError {
			status = " [ERR]"
		} else if h.Status == utils.StatusScanning {
			status = " [SCAN]"
		}

		rows = append(rows, TreeRow{
			Label:    label + status,
			Depth:    0,
			Expanded: h.Expanded,
			IsHost:   true,
			HostIdx:  i,
			// Host node is always readable
			Permissions: utils.PermFlags{Read: true},
		})

		// If Expanded, add children
		if h.Expanded {
			// Flatten shares
			flat := flattenNodes(h.Shares)
			for _, n := range flat {
				rows = append(rows, TreeRow{
					Label:       n.Name,
					Depth:       n.Depth + 1, // Shift depth by 1
					Expanded:    n.Expanded,
					Selected:    n.Selected,
					IsHost:      false,
					HostIdx:     i,
					FileNodePtr: n,
					Permissions: n.Permissions,
				})
			}
		}
	}
	return rows
}

// getVisibleHosts filters and sorts hosts for the Hosts Tab
func (m Model) getVisibleHosts() []int {
	// Returns slice of indices to m.Hosts
	var indices []int
	// Sort logic: Success > Scanning > Pending > Failed (Auth) > Dead (Hidden)
	for i, h := range m.Hosts {
		// Filter dead hosts
		if h.Status == utils.StatusError {
			errLow := strings.ToLower(h.ErrorMsg)
			if strings.Contains(errLow, "unreachable") ||
				strings.Contains(errLow, "timeout") ||
				strings.Contains(errLow, "dial tcp") {
				continue // Skip hidden
			}
		}
		// Filter Pending (Queued) hosts - Clean UI
		if h.Status == utils.StatusPending {
			continue
		}

		indices = append(indices, i)
	}

	sort.SliceStable(indices, func(i, j int) bool {
		h1 := m.Hosts[indices[i]]
		h2 := m.Hosts[indices[j]]

		// Helper to score status
		score := func(h utils.Host) int {
			// (Dead are filtered out above)
			switch h.Status {
			case utils.StatusComplete:
				return 4
			case utils.StatusScanning:
				return 3
			case utils.StatusAuthenticating:
				return 2
			case utils.StatusPending:
				return 1
			case utils.StatusError:
				return 0 // Auth failure
			default:
				return 0
			}
		}

		s1 := score(h1)
		s2 := score(h2)

		if s1 != s2 {
			return s1 > s2 // Higher score first
		}
		// Tie-break by IP
		return h1.IP < h2.IP
	})

	return indices
}

func (m Model) View() string {
	// Styles
	activeTabStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	inactiveTabStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)

	// Helper to render tabs
	renderTab := func(t string, isActive bool) string {
		if isActive {
			return activeTabStyle.Render(fmt.Sprintf("[%s]", t))
		}
		return inactiveTabStyle.Render(fmt.Sprintf(" %s ", t))
	}

	tabs := []string{"HOSTS", "TREE", "LOGS", "LOOT", "QUEUE"}
	var headerParts []string
	for i, t := range tabs {
		headerParts = append(headerParts, renderTab(t, activeView(i) == m.ActiveTab))
	}
	width := m.WindowWidth
	if width == 0 {
		width = 80 // Fallback
	}
	header := strings.Join(headerParts, " ") + "\n" + strings.Repeat("-", width) + "\n"

	content := ""
	switch m.ActiveTab {
	case viewHosts:
		visibleIndices := m.getVisibleHosts()

		if len(visibleIndices) == 0 {
			if len(m.Hosts) > 0 {
				// Hosts exist but are hidden (Pending or Dead)
				pendingCount := 0
				deadCount := 0
				for _, h := range m.Hosts {
					if h.Status == utils.StatusPending {
						pendingCount++
					}
					if h.Status == utils.StatusError {
						deadCount++
					}
				}
				content = fmt.Sprintf("Scanning in progress...\n%d hosts queued for scan.\n%d dead/hidden.", pendingCount, deadCount)
				content += "\nPlease wait or check LOGS tab."
			} else if m.DiscoveryActive {
				content = "Discovery in progress... (Pinging targets on port 445)\n"
				content += "Please wait or check LOGS tab."
			} else {
				content = "No active hosts. Press 'q' to quit."
			}
		} else {
			// Header
			headerStr := fmt.Sprintf("  %-20s %-15s %s", "HOST", "STATUS", "INFO")
			content += headerStyle.Render(headerStr) + "\n"

			start := m.HostsScroll
			end := start + m.ListHeight
			if end > len(visibleIndices) {
				end = len(visibleIndices)
			}

			for i := start; i < end; i++ {
				// Safety check
				if i >= len(visibleIndices) {
					break
				}

				realIdx := visibleIndices[i]
				h := m.Hosts[realIdx] // Access via index

				cursor := "  "
				// Cursor Logic: m.HostCursor is position in VISIBLE list now
				if m.HostCursor == i {
					cursor = "> "
				}

				statusStr := "PENDING"
				statusColor := lipgloss.Color("240")
				switch h.Status {
				case utils.StatusAuthenticating:
					statusStr = "AUTH..."
					statusColor = lipgloss.Color("220")
				case utils.StatusScanning:
					statusStr = "SCANNING"
					statusColor = lipgloss.Color("33")
				case utils.StatusComplete:
					statusStr = "SUCCESS"
					statusColor = lipgloss.Color("46")
				case utils.StatusError:
					errLow := strings.ToLower(h.ErrorMsg)
					if strings.Contains(errLow, "unreachable") ||
						strings.Contains(errLow, "timeout") ||
						strings.Contains(errLow, "dial tcp") {
						statusStr = "DEAD"
						statusColor = lipgloss.Color("240") // Dark Gray
					} else {
						statusStr = "FAILED"
						statusColor = lipgloss.Color("196")
					}
				case utils.StatusPending:
					statusStr = "QUEUED"
					statusColor = lipgloss.Color("250")
				}

				// Apply color only to the data columns, keep cursor plain
				rowStyle := lipgloss.NewStyle().Foreground(statusColor)
				rowStr := fmt.Sprintf("%-20s %-15s %s", h.IP, statusStr, h.ErrorMsg)

				content += fmt.Sprintf("%s%s\n", cursor, rowStyle.Render(rowStr))
			}
		}
	case viewTree:
		content = "Global File Tree\n\n"

		visibleRows := m.getTreeItems()
		start := m.TreeScroll
		end := start + m.ListHeight
		if end > len(visibleRows) {
			end = len(visibleRows)
		}

		for i := start; i < end; i++ {
			if i >= len(visibleRows) {
				break
			}
			row := visibleRows[i]

			cursor := "  "
			if m.TreeCursor == i {
				cursor = "> "
			}

			indent := strings.Repeat("  ", row.Depth)
			icon := "📄"

			// Determine Icon
			if row.IsHost {
				icon = "🖥️" // Host Icon
				if row.Expanded {
					icon = "📂" // Opened Host
				}
			} else if row.FileNodePtr != nil && row.FileNodePtr.IsDir {
				if row.Expanded {
					icon = "📂"
				} else {
					icon = "📁"
				}
			}

			// Selection mark (only meaningful for files/shares for now?)
			// Hosts dont have 'Selected' in the same way (checkbox).
			selectedMark := "   "
			if !row.IsHost {
				selectedMark = "[ ]"
				if row.Selected {
					selectedMark = "[x]"
				}
			}

			// Colorize Name based on Read Permission
			nameColor := lipgloss.Color("46") // Green
			if !row.Permissions.Read {
				nameColor = lipgloss.Color("196") // Red
			}
			nameStyle := lipgloss.NewStyle().Foreground(nameColor)

			// Render Line
			line := fmt.Sprintf("%s%s%s %s %s", cursor, indent, icon, nameStyle.Render(row.Label), selectedMark)
			content += line + "\n"
		}
	case viewLoot:
		content = m.lootView()
	case viewLog:
		content = "Activity Logs:\n\n"

		start := m.LogScroll
		// If AutoScroll is ON, force to bottom
		if m.LogAutoScroll {
			start = len(m.Logs) - m.ListHeight
		}

		// Bounds check
		if start < 0 {
			start = 0
		}
		if start > len(m.Logs) {
			start = len(m.Logs)
		}

		end := start + m.ListHeight
		if end > len(m.Logs) {
			end = len(m.Logs)
		}

		for i := start; i < end; i++ {
			content += m.Logs[i] + "\n"
		}
	case viewQueue:
		content = m.queueView()
	}

	return header + content + m.renderFooter()
}

func (m Model) renderFooter() string {
	footerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).PaddingTop(1)

	// Notification Overlay
	if m.Notification != "" {
		notifyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true).PaddingTop(1)
		return notifyStyle.Render("Checking: " + m.Notification)
	}

	var commands []string

	commands = append(commands, "q:Quit", "tab:Next Tab")

	switch m.ActiveTab {
	case viewHosts:
		commands = append(commands, "j/k:Nav", "f:Force Scan", "D:Deep Scan(+1)", "x:Report")
	case viewTree:
		commands = append(commands, "j/k:Nav", "space:Select", "enter:Expand", "p:Pull", "f:Force Pull", "D:Expand Dir")

		// Add Legend
		green := lipgloss.NewStyle().Foreground(lipgloss.Color("46")).Render("Green:Read")
		red := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("Red:NoAccess")
		commands = append(commands, "|", green, red)
	case viewLoot:
		commands = append(commands, "j/k:Nav", "enter:Expand/View", "pgup/pgdn:Preview Scroll")
	case viewLog:
		commands = append(commands, "j/k:Scroll")
	case viewQueue:
		commands = append(commands, "j/k:Nav", "f:Force")
	}

	// Add Discovery Progress to footer if scanning
	if m.ActiveTab == viewHosts {
		pending := 0
		scanning := 0
		for _, h := range m.Hosts {
			if h.Status == utils.StatusPending {
				pending++
			}
			if h.Status == utils.StatusScanning || h.Status == utils.StatusAuthenticating {
				scanning++
			}
		}
		if pending > 0 || scanning > 0 || m.DiscoveryActive {
			statusStr := fmt.Sprintf(" | Progress: %d Scanning, %d Pending", scanning, pending)
			if m.DiscoveryActive {
				statusStr += " [Discovery Active]"
			}
			return footerStyle.Render(strings.Join(commands, "  ") + statusStr)
		}
	}

	return footerStyle.Render(strings.Join(commands, "  "))
}

func (m *Model) reloadLoot() {
	nodes, err := loot.ScanLootDir(m.LootDir)
	if err != nil {
		m.Notification = "Error loading loot: " + err.Error()
		return
	}
	m.LootNodes = nodes
	m.LootLoaded = true
}

func (m Model) lootPaneSizes() (treeWidth, viewportWidth, height int) {
	width := m.WindowWidth
	if width <= 0 {
		width = 100
	}

	height = m.ListHeight
	if height <= 0 {
		height = 20
	}

	treeWidth = width * 30 / 100
	if treeWidth < 20 {
		treeWidth = 20
	}
	if treeWidth > 40 {
		treeWidth = 40
	}

	// The loot tree has a right border, so reserve one column for it.
	viewportWidth = width - treeWidth - 1
	if viewportWidth < 1 {
		viewportWidth = 1
	}
	if treeWidth > width-2 {
		treeWidth = width - 2
		if treeWidth < 1 {
			treeWidth = 1
		}
		viewportWidth = 1
	}

	return treeWidth, viewportWidth, height
}

func (m *Model) resizeLootViewport() {
	_, viewportWidth, height := m.lootPaneSizes()
	m.LootViewport.Width = viewportWidth
	m.LootViewport.Height = height
	if m.LootViewport.PastBottom() {
		m.LootViewport.GotoBottom()
	}
}

func (m *Model) handleLootEnter() tea.Cmd {
	nodes := flattenNodes(m.LootNodes)
	if m.LootCursor >= 0 && m.LootCursor < len(nodes) {
		node := nodes[m.LootCursor]
		if node.IsDir {
			node.Expanded = !node.Expanded
		} else {
			// Convert/Preview
			content, err := loot.ConvertFile(node.Path)
			if err != nil {
				content = "Error reading file: " + err.Error()
			}
			m.LootViewport.SetContent(content)
			m.LootViewport.GotoTop()
			m.Notification = "Loaded " + node.Name
			return m.notify(m.Notification)
		}
	}
	return nil
}

func (m *Model) handleLootExtract() tea.Cmd {
	nodes := flattenNodes(m.LootNodes)
	if m.LootCursor >= 0 && m.LootCursor < len(nodes) {
		node := nodes[m.LootCursor]
		if !node.IsDir {
			// Check extension
			ext := strings.ToLower(filepath.Ext(node.Path))
			switch ext {
			case ".zip", ".tar", ".gz", ".rar", ".7z", ".tgz":
				// Attempt extract
				dest, err := loot.ExtractArchive(node.Path)
				if err != nil {
					m.Notification = "Extract Fail: " + err.Error()
				} else {
					m.Notification = "Extracted to: " + filepath.Base(dest)
					// Reload loot to show new dir
					m.reloadLoot()
				}
				return m.notify(m.Notification)
			}
		}
	}
	return nil
}

func (m Model) lootView() string {
	// Split view: left tree and right file preview sized to the terminal.
	treeWidth, viewportWidth, height := m.lootPaneSizes()
	lootViewport := m.LootViewport
	lootViewport.Width = viewportWidth
	lootViewport.Height = height

	// Render Tree
	treeView := ""
	nodes := flattenNodes(m.LootNodes)

	start := m.LootScroll
	end := start + m.ListHeight
	if end > len(nodes) {
		end = len(nodes)
	}

	for i := start; i < end; i++ {
		node := nodes[i]
		cursor := "  "
		if m.LootCursor == i {
			cursor = "> "
		}

		icon := " "
		if node.IsDir {
			if node.Expanded {
				icon = "📂"
			} else {
				icon = "📁"
			}
		} else {
			icon = "📄"
		}

		pad := strings.Repeat("  ", node.Depth)
		line := fmt.Sprintf("%s%s%s %s", cursor, pad, icon, node.Name)

		// Style
		if m.LootCursor == i {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(line)
		}
		treeView += line + "\n"
	}

	leftBox := lipgloss.NewStyle().
		Width(treeWidth).
		BorderStyle(lipgloss.NormalBorder()).
		BorderRight(true).
		Render(treeView)

	rightBox := lipgloss.NewStyle().
		Width(viewportWidth).
		Render(lootViewport.View())

	return lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
}

func (m Model) queueView() string {
	items := m.Scheduler.GetQueueSnapshot()

	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("63")).Bold(true)
	header := headerStyle.Render(fmt.Sprintf("  %-20s %-10s %-20s %s", "ID", "TYPE", "TARGET", "SCHEDULE")) + "\n"

	content := ""
	if len(items) == 0 {
		content = "Queue is empty."
	} else {
		start := m.QueueScroll
		end := start + m.ListHeight
		if end > len(items) {
			end = len(items)
		}
		if start >= len(items) {
			start = 0 // Safety reset
		}

		for i := start; i < end; i++ {
			item := items[i]
			cursor := "  "
			if m.QueueCursor == i {
				cursor = "> "
			}

			// Format ScheduledTime logic
			schedStr := "NOW"
			if time.Now().Before(item.ScheduledTime) {
				waitTime := time.Until(item.ScheduledTime)
				schedStr = fmt.Sprintf("in %s", waitTime.Round(time.Second))
			} else if item.Priority {
				schedStr = "URGENT"
			}

			// Colorize Action
			actionColor := lipgloss.Color("250")
			if item.ActionType == "PULL" {
				actionColor = lipgloss.Color("205")
			}
			if item.ActionType == "TREE" {
				actionColor = lipgloss.Color("33")
			}

			row := fmt.Sprintf("%s%-20s %-10s %-20s %s",
				cursor,
				truncate(item.ID, 18),
				item.ActionType,
				truncate(item.HostIP, 18),
				schedStr)

			content += lipgloss.NewStyle().Foreground(actionColor).Render(row) + "\n"
		}
	}

	return header + content
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + ".."
	}
	return s
}
