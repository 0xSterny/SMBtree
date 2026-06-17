package smb

import (
	"encoding/hex"
	"net"
	"os"
	"github.com/0xSterny/SMBtree/pkg/utils"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"
	"golang.org/x/net/proxy"
)

// Session wraps the SMB connection
type Session struct {
	conn    net.Conn
	session *smb2.Session
}

func Connect(host string, creds utils.Credential, dialer proxy.Dialer, timeout time.Duration) (*Session, error) {
	// Handle UPN (user@domain) if domain is empty
	if creds.Domain == "" && strings.Contains(creds.Username, "@") {
		parts := strings.Split(creds.Username, "@")
		if len(parts) == 2 {
			creds.Username = parts[0]
			creds.Domain = parts[1]
		}
	}

	target := host + ":445"
	var conn net.Conn
	var err error

	if dialer != nil {
		conn, err = dialer.Dial("tcp", target)
	} else {
		conn, err = net.DialTimeout("tcp", target, timeout)
	}

	if err != nil {
		return nil, err
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:        creds.Username,
			Password:    creds.Password,
			Domain:      creds.Domain,
			Workstation: "WIN-WORKSTATION",
		},
	}

	// Handle Auth Types
	if creds.AuthType == "hash" {
		hBytes, _ := hex.DecodeString(creds.Hash)
		d.Initiator = &smb2.NTLMInitiator{
			User:   creds.Username,
			Domain: creds.Domain,
			Hash:   hBytes,
		}
	} else if creds.AuthType == "guest" || creds.AuthType == "nopass" {
		// Guest usually means empty password? OR specific Guest initiator?
		// NTLMInitiator with empty password often works for guest if server allows.
	}

	s, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return &Session{conn: conn, session: s}, nil
}

func (s *Session) Close() {
	if s.session != nil {
		s.session.Logoff()
	}
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Session) ListShares() ([]string, error) {
	return s.session.ListSharenames()
}

// ListDirectory lists files in a share/path
// path should be clean, e.g. "Users/Admin"
func (s *Session) ListDirectory(share, path string) ([]os.FileInfo, error) {
	fs, err := s.session.Mount(share)
	if err != nil {
		return nil, err
	}
	defer fs.Umount()

	// smb2.Share acts like os.DirFS mostly?
	// We need to ReadDir
	// fs.ReadDir(path) if available methods match
	return fs.ReadDir(path)
}

// CanRead checks if we can open the file or list the directory
func (s *Session) CanRead(share, path string, isDir bool) bool {
	fs, err := s.session.Mount(share)
	if err != nil {
		return false
	}
	defer fs.Umount()

	if isDir {
		// Try listing
		_, err := fs.ReadDir(path)
		return err == nil
	} else {
		// Try opening
		f, err := fs.Open(path)
		if err == nil {
			f.Close()
			return true
		}
		return false
	}
}

// MountShare returns a mounted share, caller is responsible for Unmount()
func (s *Session) MountShare(share string) (*smb2.Share, error) {
	return s.session.Mount(share)
}

// CanReadMounted checks read access using an existing mount
func (s *Session) CanReadMounted(fs *smb2.Share, path string, isDir bool) bool {
	if isDir {
		_, err := fs.ReadDir(path)
		return err == nil
	} else {
		f, err := fs.Open(path)
		if err == nil {
			f.Close()
			return true
		}
		return false
	}
}
