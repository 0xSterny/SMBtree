package loot

import (
	"os"
	"path/filepath"
	"github.com/0xSterny/SMBtree/pkg/utils"
)

// ScanLootDir recurses through the loot directory and builds a FileNode tree
func ScanLootDir(rootPath string) ([]*utils.FileNode, error) {
	info, err := os.Stat(rootPath)
	if os.IsNotExist(err) {
		// Loot dir might not exist yet, just return empty
		return []*utils.FileNode{}, nil
	}
	if err != nil {
		return nil, err
	}

	// Create a virtual root or just scan contents?
	// TUI likely expects a list of shares/roots.
	// Since loot is organized by Host IP, root children will be Host Folders.

	// But wait, ScanLootDir needs to return something TUI can consume.
	// TUI expects `[]*utils.Host` usually for the main view.
	// But for Loot Tab, we might want a new tree structure.
	// Let's reuse FileNode as the fundamental unit.

	root := &utils.FileNode{
		Name:     filepath.Base(rootPath),
		Path:     rootPath,
		IsDir:    info.IsDir(),
		ModTime:  info.ModTime(),
		Expanded: true, // Auto expand root?
	}

	// Walk function
	err = walk(root, rootPath)
	return root.Children, err
}

func walk(parent *utils.FileNode, currentPath string) error {
	entries, err := os.ReadDir(currentPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fullPath := filepath.Join(currentPath, entry.Name())
		node := &utils.FileNode{
			Name:     entry.Name(),
			Path:     fullPath,
			IsDir:    entry.IsDir(),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			Depth:    parent.Depth + 1,
			Children: []*utils.FileNode{},
		}

		parent.Children = append(parent.Children, node)

		if entry.IsDir() {
			walk(node, fullPath)
		}
	}
	return nil
}
