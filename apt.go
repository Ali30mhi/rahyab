package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Hosts of known Ubuntu/Debian mirrors. Used to tell the main archive apart
// from third-party repos whose path also ends in /ubuntu or /debian
// (Docker's repo, PPAs), which must never be rewritten.
var aptMirrorHosts = map[string]bool{}

func init() {
	if ml, err := parseMirrors(embeddedMirrors); err == nil {
		noteAptMirrors(ml)
	}
}

func noteAptMirrors(ml MirrorList) {
	for _, kind := range []string{"ubuntu", "debian"} {
		for _, m := range ml[kind] {
			if u, err := url.Parse(m.URL); err == nil {
				aptMirrorHosts[strings.ToLower(u.Hostname())] = true
			}
		}
	}
}

func isOfficialAptHost(h string) bool {
	switch h {
	case "archive.ubuntu.com", "deb.debian.org", "ftp.debian.org", "httpredir.debian.org", "http.debian.net":
		return true
	}
	return strings.HasSuffix(h, ".archive.ubuntu.com") ||
		(strings.HasPrefix(h, "ftp.") && strings.HasSuffix(h, ".debian.org"))
}

// isMainArchive keeps the distro's main archive (official or a known
// mirror) and skips security and third-party repos.
func isMainArchive(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	p := strings.TrimRight(strings.ToLower(u.Path), "/")
	if strings.Contains(host, "security") || strings.Contains(p, "security") {
		return false
	}
	if !strings.HasSuffix(p, "/ubuntu") && !strings.HasSuffix(p, "/debian") {
		return false
	}
	return isOfficialAptHost(host) || aptMirrorHosts[host]
}

func aptFiles() []string {
	files := []string{"/etc/apt/sources.list"}
	a, _ := filepath.Glob("/etc/apt/sources.list.d/*.sources")
	b, _ := filepath.Glob("/etc/apt/sources.list.d/*.list")
	return append(append(files, a...), b...)
}

// aptCurrent finds the main Debian/Ubuntu mirror the system uses right now.
func aptCurrent() string {
	for _, f := range aptFiles() {
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

func isURIsLine(s string) bool {
	return len(s) >= 5 && strings.EqualFold(s[:5], "URIs:")
}

// aptURIs returns the mirror address(es) on one line of a sources file.
// Handles both formats:
//
//	deb822:  URIs: http://deb.debian.org/debian
//	classic: deb [arch=amd64] http://deb.debian.org/debian trixie main
func aptURIs(line string) []string {
	if isURIsLine(line) {
		return strings.Fields(line[5:])
	}
	f := strings.Fields(line)
	if len(f) == 0 || f[0] != "deb" {
		return nil
	}
	if i := uriIndex(f); i >= 0 {
		return f[i : i+1]
	}
	return nil
}

// uriIndex returns the position of the URI on a classic deb/deb-src line,
// skipping an optional [options] block, or -1.
func uriIndex(f []string) int {
	if len(f) < 2 || (f[0] != "deb" && f[0] != "deb-src") {
		return -1
	}
	i := 1
	if strings.HasPrefix(f[i], "[") {
		for i < len(f) && !strings.HasSuffix(f[i], "]") {
			i++
		}
		i++ // skip the token that closes the options
	}
	if i >= len(f) {
		return -1
	}
	return i
}

// rewriteLine points one line at newURL if it uses the main archive.
func rewriteLine(line, newURL string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return line, false
	}
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	if isURIsLine(trimmed) {
		var out []string
		seen := map[string]bool{}
		changed := false
		for _, u := range strings.Fields(trimmed[5:]) {
			if isMainArchive(u) {
				u, changed = newURL, true
			}
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
		if !changed {
			return line, false
		}
		return indent + "URIs: " + strings.Join(out, " "), true
	}
	f := strings.Fields(trimmed)
	i := uriIndex(f)
	if i < 0 || !isMainArchive(f[i]) {
		return line, false
	}
	f[i] = newURL
	return indent + strings.Join(f, " "), true
}

// rewriteSources rewrites one sources file and reports how many lines
// changed. Lines that don't use the main archive are kept byte for byte.
func rewriteSources(content, newURL string) (string, int) {
	lines := strings.SplitAfter(content, "\n")
	n := 0
	for i, l := range lines {
		body := strings.TrimRight(l, "\r\n")
		if out, ok := rewriteLine(body, newURL); ok {
			lines[i] = out + l[len(body):]
			n++
		}
	}
	return strings.Join(lines, ""), n
}

func filePerm(p string) os.FileMode {
	if fi, err := os.Stat(p); err == nil {
		return fi.Mode().Perm()
	}
	return 0o644
}

// aptSnapshot saves the full original content of every file that rahyab
// would touch, as JSON {path: content}.
func aptSnapshot() (string, bool) {
	orig := map[string]string{}
	for _, p := range aptFiles() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if _, n := rewriteSources(string(b), "http://probe.invalid/ubuntu"); n > 0 {
			orig[p] = string(b)
		}
	}
	if len(orig) == 0 {
		return "", false
	}
	b, _ := json.Marshal(orig)
	return string(b), true
}

func aptSet(urls []string) error {
	if err := needRoot(); err != nil {
		return err
	}
	newURL := strings.TrimRight(urls[0], "/")
	written := map[string][]byte{} // originals of files already changed, for rollback
	for _, p := range aptFiles() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out, n := rewriteSources(string(b), newURL)
		if n == 0 {
			continue
		}
		if err := writeFileAtomic(p, []byte(out), filePerm(p)); err != nil {
			for wp, wb := range written {
				_ = writeFileAtomic(wp, wb, filePerm(wp))
			}
			return err
		}
		written[p] = b
	}
	if len(written) == 0 {
		return errors.New("couldn't find the main Ubuntu/Debian archive in /etc/apt; nothing changed")
	}
	fmt.Println("  now run: sudo apt-get update")
	return nil
}

func aptRestore(prev string, had bool) error {
	if !had {
		return nil
	}
	if err := needRoot(); err != nil {
		return err
	}
	var orig map[string]string
	if err := json.Unmarshal([]byte(prev), &orig); err != nil {
		return fmt.Errorf("bad undo info: %v", err)
	}
	for p, content := range orig {
		if err := writeFileAtomic(p, []byte(content), filePerm(p)); err != nil {
			return err
		}
	}
	fmt.Println("  now run: sudo apt-get update")
	return nil
}

// aptPrintCmd is for Dockerfiles: official images use the default hosts.
func aptPrintCmd(urls []string) string {
	u := strings.TrimRight(first(urls), "/")
	from := `archive\.ubuntu\.com/ubuntu`
	if strings.HasSuffix(u, "/debian") {
		from = `deb\.debian\.org/debian`
	}
	return `find /etc/apt/ \( -name '*.list' -o -name '*.sources' \) -exec sed -i -E ` +
		`'s#https?://` + from + `([/ ]|$)#` + u + `\1#g' {} +`
}

func aptAdapter() adapter {
	return adapter{
		kind: "apt",
		keep: 1,
		available: func() error {
			if runtime.GOOS != "linux" {
				return errors.New("Linux only")
			}
			return need("apt-get")()
		},
		current:  aptCurrent,
		snapshot: aptSnapshot,
		value:    func(urls []string) string { return strings.TrimRight(first(urls), "/") },
		set:      aptSet,
		restore:  aptRestore,
		printCmd: aptPrintCmd,
	}
}

// officialFor returns the official source to test against. For apt it uses
// the scheme this system already has: on some networks plain http to the
// official archive is filtered while https works.
func officialFor(tool, kind string) Mirror {
	m := upstream[kind]
	if tool != "apt" {
		return m
	}
	cur := aptCurrent()
	u, err := url.Parse(cur)
	up, _ := url.Parse(m.URL)
	if cur == "" || err != nil || up == nil || !strings.EqualFold(u.Hostname(), up.Hostname()) {
		return m
	}
	m.URL = strings.TrimRight(cur, "/")
	return m
}
