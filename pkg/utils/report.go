package utils

import (
	"fmt"
	"os"
	"time"
)

// GenerateReport creates a markdown report of the findings
func GenerateReport(hosts []Host) error {
	f, err := os.Create("looting_report.md")
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "# Looting Report - %s\n\n", time.Now().Format(time.RFC822))

	for _, h := range hosts {
		fmt.Fprintf(f, "# Target: %s (%s)\n", h.IP, h.Hostname)
		status := "Unknown"
		switch h.Status {
		case StatusComplete:
			status = "Success"
		case StatusError:
			status = "Failed: " + h.ErrorMsg
		default:
			status = "Pending/Scanning"
		}
		fmt.Fprintf(f, "**Scan Status:** %s\n", status)
		fmt.Fprintf(f, "**Creds:** %s\n\n", h.Creds.Username)

		if len(h.Shares) > 0 {
			fmt.Fprintln(f, "## Shares / File Tree")
			for _, share := range h.Shares {
				printNode(f, share, "")
			}
		}
		fmt.Fprintln(f, "\n---")
	}
	return nil
}

func printNode(f *os.File, node *FileNode, indent string) {
	icon := "-"
	if node.IsDir {
		icon = "+"
	}
	mark := "[ ]"
	if node.Selected {
		mark = "[x]"
	}

	fmt.Fprintf(f, "%s%s %s %s\n", indent, icon, mark, node.Name)

	if node.IsDir && len(node.Children) > 0 {
		for _, child := range node.Children {
			printNode(f, child, indent+"  ")
		}
	}
}
