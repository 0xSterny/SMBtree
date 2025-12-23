package utils

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
)

// ParseInput determines if the input is a file, CIDR, or single host
func ParseInput(input string) ([]Host, error) {
	// 1. Check if file
	if _, err := os.Stat(input); err == nil {
		return parseFile(input)
	}

	// 2. Check if CIDR
	if strings.Contains(input, "/") {
		_, ipNet, err := net.ParseCIDR(input)
		if err == nil {
			return expandCIDR(ipNet)
		}
	}

	// 3. Assume Single Host
	h, err := parseHostString(input)
	if err != nil {
		return nil, err
	}
	return []Host{h}, nil
}

func parseFile(path string) ([]Host, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hosts []Host
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Recursive check? No, file usually contains list of hosts/CIDRs
		// For simplicity, assume file contains lines of host strings or CIDRs
		// But parseHostString doesn't handle CIDRs.
		// Let's keep it simple for now: File = list of hosts.
		// If we want to support CIDR in file, we need logic here.
		if strings.Contains(line, "/") {
			_, ipNet, err := net.ParseCIDR(line)
			if err == nil {
				cidrHosts, _ := expandCIDR(ipNet)
				hosts = append(hosts, cidrHosts...)
				continue
			}
		}

		h, err := parseHostString(line)
		if err == nil {
			hosts = append(hosts, h)
		}
	}
	fmt.Printf("[DEBUG] Parsed %d hosts from file\n", len(hosts))
	return hosts, scanner.Err()
}

func parseHostString(s string) (Host, error) {
	// Format: IP or IP:User:Pass or IP:User:Pass:Domain
	parts := strings.Split(s, ":")
	h := Host{IP: parts[0]}

	if len(parts) >= 3 {
		h.Creds.Username = parts[1]
		h.Creds.Password = parts[2]
		h.Creds.AuthType = "password"
	}
	if len(parts) >= 4 {
		h.Creds.Domain = parts[3]
	}
	return h, nil
}

func expandCIDR(ipNet *net.IPNet) ([]Host, error) {
	var hosts []Host
	ip := ipNet.IP.Mask(ipNet.Mask)

	for ipNet.Contains(ip) {
		// Skip Network and Broadcast?
		// Usually .0 and .255 in /24
		// But let's just add everything for now.

		// We need a copy of IP because Incr modifies it?
		// Logic:
		// inc(ip)
		// check contains

		// Standard loop:

		hosts = append(hosts, Host{IP: ip.String()})

		inc(ip)
	}
	// Remove network address and broadcast if desired, but for now allow all.
	// Usually the loop includes network address as first item.

	// Fix: The loop above adds the network address first, which is fine.
	// But verify the logic.

	return hosts, nil
}

func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// ApplyGlobalCreds applies command-line credentials to hosts that don't have them
func ApplyGlobalCreds(hosts []Host, global Credential) []Host {
	for i := range hosts {
		h := &hosts[i]

		// If global user is set, override or fill
		if global.Username != "" {
			h.Creds.Username = global.Username
		}
		if global.Password != "" {
			h.Creds.Password = global.Password
		}
		if global.Domain != "" {
			h.Creds.Domain = global.Domain
		}
		if global.Hash != "" {
			h.Creds.Hash = global.Hash
		}
		if global.AuthType != "" {
			h.Creds.AuthType = global.AuthType
		}
	}
	return hosts
}
