package utils

import (
	"strings"
)

// SplitSharePath splits "Share\Path" into "Share" and "Path"
func SplitSharePath(fullPath string) (string, string) {
	fullPath = strings.ReplaceAll(fullPath, "/", "\\")
	parts := strings.SplitN(fullPath, "\\", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}
