# SMBtree

SMBtree is a high-speed, TUI-based SMB enumeration and exfiltration tool designed for efficiency and ease of use. It handles large-scale network scans, deep directory recursion, and automated data retrieval with a modern, interactive console interface.

## Why Use SMBtree?

-   **Interactive TUI**: Navigate hosts, shares, and files visually with a global tree view.
-   **High Performance**: Multi-threaded scanner with configurable worker pools (default: 50 threads).
-   **Smart Expansion**: Support for CIDR ranges (`10.0.0.0/24`) and iterative depth expansion.
-   **Resilient**: Includes "Jitter" logic to evade detection and robust error handling for connection limits.
-   **Exfiltration**: Automated recursive downloading and "Loot" management.

## Installation

SMBtree is written in Go. To build it from source:

```bash
go build -o smbtree cmd/smbtree/main.go
# Linux Cross-compile
GOOS=linux go build -o smbtree-linux cmd/smbtree/main.go
```

## Usage

Run the tool with a target IP, CIDR range, or file containing targets.

### Basic Scan
```bash
./smbtree 192.168.1.0/24 -u "DOMAIN\User" -p "Password"
```

### Key Bindings
-   **Arrows / j, k**: Navigate lists.
-   **Tab**: Switch tabs (HOSTS, TREE, LOOT, QUEUE).
-   **Enter**: Expand/Collapse nodes.
-   **D**: Expand/Scan current directory (Depth + 1).
-   **Space**: Select file/folder for batch operations.
-   **p**: Pull (download) selected files.
-   **f**: Force re-scan or force execution.
-   **Ctrl+C**: Quit.

### Advanced Flags
-   `--no-limit`: Disable jitter for maximum speed.
-   `-t 100`: Set worker threads to 100.
-   `--loot "my_loot"`: Set custom download directory.
