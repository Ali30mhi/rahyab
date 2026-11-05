package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- is the international internet reachable? ----

// The official sources themselves: if none of them answers at all, the
// international internet is down for our purposes (a shutdown, or the
// "national network" mode), and only Iranian mirrors can work.
var intlChecks = []string{
	"https://pypi.org/simple/",
	"https://registry.npmjs.org/-/ping",
	"https://proxy.golang.org/",
	"https://deb.debian.org/debian/",
	"https://registry-1.docker.io/v2/",
}

var (
	intlOnce sync.Once
	intlOK   bool
)

// intlUp checks once per run, in parallel, within 3 seconds. Any HTTP
// answer counts: we only care whether the route exists.
func intlUp() bool {
	intlOnce.Do(func() {
		c := newClient(3 * time.Second)
		ok := make(chan bool, len(intlChecks))
		for _, u := range intlChecks {
			go func(u string) { ok <- reachable(c, u) }(u)
		}
		for range intlChecks {
			if <-ok {
				intlOK = true
				return
			}
		}
	})
	return intlOK
}

func reachable(c *http.Client, target string) bool {
	req, err := http.NewRequest(http.MethodHead, target, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "rahyab/"+version)
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// ---- mirror list: built-in + cached copy of the repo's list ----

const remoteMirrorsURL = "https://raw.githubusercontent.com/Ali30mhi/rahyab/main/mirrors.json"

const mirrorCacheTTL = 24 * time.Hour

func mirrorCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "rahyab", "mirrors.json")
}

// loadMirrors returns the built-in list merged with the newest list from
// the repo. The repo list is cached for a day and never fetched while the
// international internet is down, so a shutdown costs no waiting.
func loadMirrors(path string, offline bool, timeout time.Duration) (MirrorList, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return parseMirrors(b)
	}
	builtin, err := parseMirrors(embeddedMirrors)
	if err != nil {
		return nil, err
	}

	cachePath := mirrorCachePath()
	var cached MirrorList
	fresh := false
	if cachePath != "" {
		if fi, err := os.Stat(cachePath); err == nil {
			if b, err := os.ReadFile(cachePath); err == nil {
				if m, err := parseMirrors(b); err == nil {
					cached = m
					fresh = time.Since(fi.ModTime()) < mirrorCacheTTL
				}
			}
		}
	}
	if !fresh && !offline && intlUp() {
		if m, raw, err := fetchMirrors(timeout); err == nil {
			cached = m
			if cachePath != "" {
				_ = writeFileAtomic(cachePath, raw, 0o644)
			}
		}
	}
	if cached == nil {
		return builtin, nil
	}
	return mergeMirrors(cached, builtin), nil
}

func fetchMirrors(timeout time.Duration) (MirrorList, []byte, error) {
	if timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	code, body, _, err := httpGet(newClient(timeout), remoteMirrorsURL, "")
	if err != nil {
		return nil, nil, err
	}
	if code != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d", code)
	}
	m, err := parseMirrors(body)
	if err != nil {
		return nil, nil, err
	}
	if len(m) == 0 {
		return nil, nil, errors.New("empty list")
	}
	return m, body, nil
}

// mergeMirrors keeps every mirror from both lists, a's first, without
// duplicates, so a stale or partial download can never lose a mirror.
func mergeMirrors(a, b MirrorList) MirrorList {
	out := MirrorList{}
	for _, src := range []MirrorList{a, b} {
		for kind, list := range src {
			for _, m := range list {
				if !hasURL(out[kind], m.URL) {
					out[kind] = append(out[kind], m)
				}
			}
		}
	}
	return out
}

func hasURL(list []Mirror, u string) bool {
	for _, m := range list {
		if normURL(m.URL) == normURL(u) {
			return true
		}
	}
	return false
}

// ---- DNS ----

const dnsHint = "DNS fails even for Iranian mirrors. Your DNS server is probably outside Iran\n" +
	"  (like 8.8.8.8 or 1.1.1.1) and can't be reached during a shutdown.\n" +
	"  Switch to your ISP's DNS or an Iranian DNS service, then run rahyab again."

// domesticHost is an Iranian mirror from the built-in list, used to test
// whether DNS works for Iranian names at all.
func domesticHost() string {
	ml, err := parseMirrors(embeddedMirrors)
	if err != nil {
		return ""
	}
	for _, kind := range []string{"debian", "ubuntu", "docker"} {
		for _, m := range ml[kind] {
			if u, err := url.Parse(m.URL); err == nil && strings.HasSuffix(u.Hostname(), ".ir") {
				return u.Hostname()
			}
		}
	}
	return ""
}

func dnsWorks(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	return err == nil && len(addrs) > 0
}

func allDNSFailed(res []Result) bool {
	seen := false
	for _, r := range res {
		if r.Skipped {
			continue
		}
		if r.Status != "DNS failed" {
			return false
		}
		seen = true
	}
	return seen
}

// ---- shutdown-specific choices and notes ----

// preferSumDB puts Go mirrors that serve the checksum database first: while
// sum.golang.org is unreachable, `go get` fails through any other mirror.
func preferSumDB(res []Result) {
	sort.SliceStable(res, func(i, j int) bool {
		a, b := res[i], res[j]
		if a.OK != b.OK {
			return a.OK
		}
		return !a.NoSumDB && b.NoSumDB
	})
}

func shutdownNotes(p plan, best Result, online bool) {
	if online {
		return
	}
	if p.kind == "go" && best.NoSumDB {
		if v, _ := run("go", "env", "GOSUMDB"); v != "off" {
			fmt.Println("    note: this mirror can't serve Go's checksum database and sum.golang.org is unreachable,")
			fmt.Println("    so `go get` will fail until you turn checksum checks off:  go env -w GOSUMDB=off")
			fmt.Println("    (less safe; turn them back on later with:  go env -u GOSUMDB)")
		}
	}
	if strings.Contains(best.URL, "/repository/") {
		fmt.Println("    note: this mirror looks like a cache, so during a shutdown it may only have")
		fmt.Println("    packages that someone downloaded before.")
	}
}
