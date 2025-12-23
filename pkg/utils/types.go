package utils

import (
	"time"
)

// ScanStatus represents the current state of a host scan
type ScanStatus int

const (
	StatusPending ScanStatus = iota
	StatusAuthenticating
	StatusScanning
	StatusComplete
	StatusError
)

type Credential struct {
	Username string
	Password string
	Domain   string
	Hash     string
	AuthType string // "password", "hash", "guest", "kerberos"
}

// Host represents a target machine
type Host struct {
	IP            string
	Hostname      string
	Creds         Credential
	Status        ScanStatus
	Shares        []*FileNode // Root level shares are FileNodes
	ErrorMsg      string
	ScheduledTime time.Time // Initial Auth/Tree Schedule
	JitterDelay   time.Duration
	Expanded      bool // For UI Tree View
}

// PermFlags represents file permissions
type PermFlags struct {
	Read  bool
	Write bool
	Admin bool
}

// FileNode represents a file or directory
type FileNode struct {
	Name        string
	Path        string // Full path valid for SMB calls (e.g. C$\Windows)
	ShareName   string // The share this file belongs to
	IsDir       bool
	Size        int64
	ModTime     time.Time
	Permissions PermFlags
	Depth       int
	Children    []*FileNode
	Expanded    bool
	Selected    bool
}

// QueueItem represents an action to be performed
type QueueItem struct {
	ID            string
	ActionType    string // "TREE", "PULL", "EXFIL"
	HostIP        string
	Host          *Host     // Pointer to host for creds
	Target        string    // File path or Share
	DepthParam    int       // For TREE actions
	Priority      bool      // If true, skip jitter/scheduling delay
	ScheduledTime time.Time // When this job should run
	Status        string
}

type JobResult struct {
	QueueID    string
	HostIP     string
	Success    bool
	Error      string
	Shares     []*FileNode
	ActionType string
	Target     string // for EXPAND_DIR correlation
}
