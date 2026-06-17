package loot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mholt/archiver/v3"
)

// ExtractArchive attempts to extract the given archive file to a directory of the same name.
// Supported formats include zip, tar, rar, 7z, tgz, tar.gz, gz (if it's a tarball).
// If successful, returns the path to the extracted directory.
func ExtractArchive(srcPath string) (string, error) {
	// Check if file exists
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", srcPath)
	}

	// Create a destination directory based on the filename (without extension)
	// We handle multiple extensions like .tar.gz
	baseName := filepath.Base(srcPath)
	ext := filepath.Ext(baseName)

	// Handle .tar.gz explicitly to remove both extensions
	if strings.HasSuffix(strings.ToLower(baseName), ".tar.gz") {
		baseName = strings.TrimSuffix(baseName, ".tar.gz")
	} else if strings.HasSuffix(strings.ToLower(baseName), ".tgz") {
		baseName = strings.TrimSuffix(baseName, ".tgz")
	} else {
		baseName = strings.TrimSuffix(baseName, ext)
	}

	destDir := filepath.Join(filepath.Dir(srcPath), baseName+"_extracted")

	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination dir: %v", err)
	}

	// Attempt to unarchive
	// archiver.Unarchive will automatically detect format by extension/header
	err := archiver.Unarchive(srcPath, destDir)
	if err != nil {
		// Clean up empty dir if failed (optional, but good practice)
		// os.RemoveAll(destDir)
		return "", fmt.Errorf("extraction failed: %v", err)
	}

	return destDir, nil
}
