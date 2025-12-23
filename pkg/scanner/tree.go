package scanner

import (
	"fmt"
	"path/filepath"
	"smbtree/pkg/smb"
	"smbtree/pkg/utils"
	"strings"

	"github.com/hirochachacha/go-smb2"
)

// ScanHost enumerates shares and starts walking them
func ScanHost(s *smb.Session, h *utils.Host, maxDepth int) ([]*utils.FileNode, error) {
	// JIT Liveness Check
	if !CheckHostLive(h.IP) {
		return nil, fmt.Errorf("host unreachable (port 445)")
	}

	shares, err := s.ListShares()
	if err != nil {
		return nil, err
	}

	var rootNodes []*utils.FileNode
	for _, shareName := range shares {
		// Create root node for share
		root := &utils.FileNode{
			Name:      shareName,
			Path:      shareName, // "C$"
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

		walk(s, fs, shareName, "", root, 1, maxDepth)
		fs.Umount()
	}
	return rootNodes, nil
}

// ScanDir enumerates a specific directory to a relative depth
// ScanDir enumerates a specific directory to a relative depth
func ScanDir(s *smb.Session, share, path string, depth int) ([]*utils.FileNode, error) {
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

	walk(s, fs, share, path, dummyRoot, 1, depth)
	return dummyRoot.Children, nil
}

func walk(s *smb.Session, fs *smb2.Share, share, path string, parent *utils.FileNode, currentDepth, maxDepth int) {
	if currentDepth > maxDepth {
		return
	}

	files, err := fs.ReadDir(path)
	if err != nil {
		// Cannot list = No Read Access to this directory
		parent.Permissions.Read = false
		return
	}
	// Success = Read Access
	parent.Permissions.Read = true

	for _, f := range files {
		if f.Name() == "." || f.Name() == ".." {
			continue
		}

		nodePath := filepath.Join(path, f.Name())
		// OPSEC: Do NOT check access for every file. Opening thousands of handles
		// is noisy and slow. Assume if we can list the directory, we can read the files.
		// Detailed access checks should be done on-demand (e.g. trying to Pull).
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
			walk(s, fs, share, node.Path, node, currentDepth+1, maxDepth)
		}
	}
}
