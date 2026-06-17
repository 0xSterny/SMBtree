package scanner

import (
	"path/filepath"
	"github.com/0xSterny/SMBtree/pkg/smb"
	"github.com/0xSterny/SMBtree/pkg/utils"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"
)

// ScanHost enumerates shares and starts walking them
func ScanHost(s *smb.Session, h *utils.Host, maxDepth int, safeShares bool, blindMode bool, jitter time.Duration) ([]*utils.FileNode, error) {
	// JIT Liveness Check removed - we have an active session so we are connected.

	shares, err := s.ListShares()
	if err != nil {
		return nil, err
	}

	var rootNodes []*utils.FileNode
	for _, shareName := range shares {
		// Safe Shares Check
		if safeShares {
			upper := strings.ToUpper(shareName)
			if upper == "ADMIN$" || upper == "IPC$" || upper == "C$" {
				continue
			}
		}

		// Create root node for share
		root := &utils.FileNode{
			Name:      shareName,
			Path:      "", // Root of share
			ShareName: shareName,
			IsDir:     true,
			Depth:     0,
			Children:  []*utils.FileNode{},
		}
		rootNodes = append(rootNodes, root)

		// Walk
		// Mount once
		fs, err := s.MountShare(shareName)
		if err != nil {
			// Mark root as unreadable
			root.Permissions.Read = false
			continue // Skip walking
		}

		if blindMode {
			// Optimistically assume readable without checking
			root.Permissions.Read = true
			walk(s, fs, shareName, "", root, 1, maxDepth, blindMode, jitter)
		} else {
			walk(s, fs, shareName, "", root, 1, maxDepth, blindMode, jitter)
		}

		fs.Umount()
	}
	return rootNodes, nil
}

// ScanDir enumerates a specific directory to a relative depth
func ScanDir(s *smb.Session, share, path string, depth int, blindMode bool, jitter time.Duration) ([]*utils.FileNode, error) {
	// Normalize path to OS separator
	path = strings.ReplaceAll(path, "\\", string(filepath.Separator))
	path = strings.ReplaceAll(path, "/", string(filepath.Separator))

	// Create a dummy root
	dummyRoot := &utils.FileNode{
		Path:      path,
		ShareName: share,
		IsDir:     true,
	}

	fs, err := s.MountShare(share)
	if err != nil {
		return nil, err
	}
	defer fs.Umount()

	// If depth is 0, we assume the user processes the request with at least depth 1
	if depth == 0 {
		depth = 1
	}

	walk(s, fs, share, path, dummyRoot, 1, depth, blindMode, jitter)
	return dummyRoot.Children, nil
}

func walk(s *smb.Session, fs *smb2.Share, share, path string, parent *utils.FileNode, currentDepth, maxDepth int, blindMode bool, jitter time.Duration) {
	// Jitter
	if jitter > 0 {
		time.Sleep(jitter)
	}

	if !blindMode {
		files, err := fs.ReadDir(path)
		if err != nil {
			// Cannot list = No Read Access to this directory
			parent.Permissions.Read = false
			return
		}
		// Success = Read Access
		parent.Permissions.Read = true

		if currentDepth > maxDepth {
			return
		}

		for _, f := range files {
			if f.Name() == "." || f.Name() == ".." {
				continue
			}

			nodePath := filepath.Join(path, f.Name())
			canRead := true

			node := &utils.FileNode{
				Name:      f.Name(),
				Path:      nodePath,
				ShareName: share,
				IsDir:     f.IsDir(),
				Size:      f.Size(),
				ModTime:   f.ModTime(),
				Depth:     currentDepth,
				Permissions: utils.PermFlags{
					Read: canRead,
				},
			}

			parent.Children = append(parent.Children, node)

			if f.IsDir() {
				walk(s, fs, share, node.Path, node, currentDepth+1, maxDepth, blindMode, jitter)
			}
		}
	} else {
		// Blind Mode: We assume we can read, but we can't list files to walk them safely unless we brute force names (not implemented)
		// Wait, Blind Mode usually means "Trust that I can access it" but if we want to SEE children, we MUST ReadDir.
		// If "Blind Mode" means "Don't Probe Root if I'm not going deeper", then we rely on maxDepth check.
		// If currentDepth > maxDepth, return.
		// If we are here, we need to list files to populate children.
		// So "Blind Mode" effectively just skips the INITIAL check on the root share in ScanHost.
		// Inside walk, we really do have to ReadDir to get children.
		// UNLESS the user implies "Don't fail if ReadDir fails, just return empty?"
		// Re-reading logic: "We effectively trust the share listing and don't probe the root."
		// So checking ReadDir is necessary to get children.

		// Implementation interpretation:
		// ScanHost skipped the specific "Is share readable" probe?
		// Actually ScanHost just calls walk.
		// In walk, we MUST ReadDir to find children.
		// If BlindMode=True, we might want to suppress the error if it fails?
		// Or maybe the user meant "Don't do the pre-check in ScanHost ONLY".
		// In ScanHost logic I implemented: "If blindMode: root.Permissions.Read = true; walk(...)".
		// Inside walk, if we actually want content, we must ReadDir.

		// Let's stick to: BlindMode only affects the initial permission set, but walk still lists files.
		// Code Reuse: just call normal logic but maybe handle error differently?
		// Actually, if we are in walk, we are enumerating. We have to ReadDir.

		files, err := fs.ReadDir(path)
		if err != nil {
			// In blind mode, if we fail to list, we just mark unreadable?
			parent.Permissions.Read = false
			return
		}
		parent.Permissions.Read = true

		if currentDepth > maxDepth {
			return
		}

		for _, f := range files {
			if f.Name() == "." || f.Name() == ".." {
				continue
			}
			nodePath := filepath.Join(path, f.Name())
			node := &utils.FileNode{
				Name:      f.Name(),
				Path:      nodePath,
				ShareName: share,
				IsDir:     f.IsDir(),
				Size:      f.Size(),
				ModTime:   f.ModTime(),
				Depth:     currentDepth,
				Permissions: utils.PermFlags{
					Read: true,
				},
			}
			parent.Children = append(parent.Children, node)
			if f.IsDir() {
				walk(s, fs, share, node.Path, node, currentDepth+1, maxDepth, blindMode, jitter)
			}
		}
	}
}
