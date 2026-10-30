package main

import (
	"os"
	"path/filepath"
	"strings"
)

// aptCurrent finds the main Debian/Ubuntu mirror the system uses right now.
func aptCurrent() string {
	files, _ := filepath.Glob("/etc/apt/sources.list.d/*.sources")
	more, _ := filepath.Glob("/etc/apt/sources.list.d/*.list")
	files = append(append([]string{"/etc/apt/sources.list"}, files...), more...)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			for _, u := range aptURIs(strings.TrimSpace(line)) {
				if isMainArchive(u) {
					return u
				}
			}
		}
	}
	return ""
}

// aptURIs returns the mirror address(es) on one line of a sources file.
// Handles both formats:
//
//	deb822:  URIs: http://deb.debian.org/debian
//	classic: deb [arch=amd64] http://deb.debian.org/debian trixie main
func aptURIs(line string) []string {
	switch {
	case strings.HasPrefix(line, "URIs:"):
		return strings.Fields(strings.TrimPrefix(line, "URIs:"))
	case strings.HasPrefix(line, "deb "):
		f := strings.Fields(line)[1:]
		if len(f) > 0 && strings.HasPrefix(f[0], "[") {
			for len(f) > 0 && !strings.HasSuffix(f[0], "]") {
				f = f[1:]
			}
			if len(f) > 0 {
				f = f[1:]
			}
		}
		if len(f) > 0 {
			return f[:1]
		}
	}
	return nil
}

// isMainArchive keeps the main distro archive and skips security
// and third-party repos (like Docker's or Chrome's).
func isMainArchive(u string) bool {
	u = strings.TrimRight(strings.ToLower(u), "/")
	if strings.Contains(u, "security") {
		return false
	}
	return strings.HasSuffix(u, "/debian") || strings.HasSuffix(u, "/ubuntu")
}
