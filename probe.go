package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

//go:embed mirrors.json
var embeddedMirrors []byte

type Mirror struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type MirrorList map[string][]Mirror

var upstream = map[string]Mirror{
	"pypi":   {"Official (pypi.org)", "https://pypi.org/simple/"},
	"npm":    {"Official (npmjs)", "https://registry.npmjs.org/"},
	"go":     {"Official (proxy.golang.org)", "https://proxy.golang.org"},
	"docker": {"Official (Docker Hub)", "https://registry-1.docker.io"},
	"ubuntu": {"Official (archive.ubuntu.com)", "http://archive.ubuntu.com/ubuntu"},
	"debian": {"Official (deb.debian.org)", "http://deb.debian.org/debian"},
}

func loadMirrors(path string) (MirrorList, error) {
	data := embeddedMirrors
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		data = b
	}
	var m MirrorList
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("mirror list: %v", err)
	}
	return m, nil
}

type Result struct {
	Mirror
	OK      bool
	Status  string
	Latency time.Duration
	Version string
	Date    time.Time
	Stale   bool
}

func httpGet(c *http.Client, url, accept string) (int, []byte, time.Duration, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("User-Agent", "rahyab/"+version)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, time.Since(start), err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, body, time.Since(start), err
}

func join(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

var pipFileRe = regexp.MustCompile(`pip-(\d+(?:\.\d+)+)(?:\.tar\.gz|-py)`)

func probe(c *http.Client, kind string, m Mirror, codename string) Result {
	r := Result{Mirror: m}
	var url, accept string
	switch kind {
	case "pypi":
		url = join(m.URL, "pip/")
	case "npm":
		url = join(m.URL, "semver")
		accept = "application/vnd.npm.install-v1+json"
	case "go":
		url = join(m.URL, "golang.org/x/text/@latest")
	case "docker":
		url = join(m.URL, "v2/")
	case "ubuntu", "debian":
		url = join(m.URL, "dists/"+codename+"-updates/Release")
	}

	code, body, d, err := httpGet(c, url, accept)
	r.Latency = d
	if err != nil {
		r.Status = shortErr(err)
		return r
	}
	switch {
	case code == 403 || code == 451:
		r.Status = fmt.Sprintf("HTTP %d (sanctions)", code)
		return r
	case kind == "docker" && (code == 200 || code == 401):
	case code != 200:
		r.Status = fmt.Sprintf("HTTP %d", code)
		return r
	}

	switch kind {
	case "pypi":
		for _, mm := range pipFileRe.FindAllSubmatch(body, -1) {
			if v := string(mm[1]); cmpVer(v, r.Version) > 0 {
				r.Version = v
			}
		}
		if r.Version == "" {
			r.Status = "unexpected response"
			return r
		}
	case "npm":
		var doc struct {
			DistTags map[string]string `json:"dist-tags"`
		}
		if json.Unmarshal(body, &doc) != nil || doc.DistTags["latest"] == "" {
			r.Status = "unexpected response"
			return r
		}
		r.Version = doc.DistTags["latest"]
	case "go":
		var doc struct{ Version string }
		if json.Unmarshal(body, &doc) != nil || doc.Version == "" {
			r.Status = "unexpected response"
			return r
		}
		r.Version = doc.Version
	case "ubuntu", "debian":
		t, ok := releaseDate(body)
		if !ok {
			r.Status = "unexpected response"
			return r
		}
		r.Date = t
	}
	r.OK = true
	r.Status = "ok"
	return r
}

func probeAll(c *http.Client, kind string, list []Mirror, codename string) []Result {
	res := make([]Result, len(list))
	var wg sync.WaitGroup
	for i, m := range list {
		wg.Add(1)
		go func(i int, m Mirror) {
			defer wg.Done()
			res[i] = probe(c, kind, m, codename)
		}(i, m)
	}
	wg.Wait()
	markStale(res)
	sort.SliceStable(res, func(i, j int) bool {
		a, b := res[i], res[j]
		if a.OK != b.OK {
			return a.OK
		}
		if a.Stale != b.Stale {
			return !a.Stale
		}
		return a.Latency < b.Latency
	})
	return res
}

func markStale(res []Result) {
	var newestV string
	var newestD time.Time
	for _, r := range res {
		if !r.OK {
			continue
		}
		if cmpVer(r.Version, newestV) > 0 {
			newestV = r.Version
		}
		if r.Date.After(newestD) {
			newestD = r.Date
		}
	}
	for i := range res {
		r := &res[i]
		if !r.OK {
			continue
		}
		if r.Version != "" && cmpVer(r.Version, newestV) < 0 {
			r.Stale = true
		}
		if !r.Date.IsZero() && newestD.Sub(r.Date) > 48*time.Hour {
			r.Stale = true
		}
	}
}

func printResults(title string, res []Result) {
	fmt.Printf("\n== %s ==\n", title)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range res {
		mark := "✗"
		if r.OK {
			mark = "✓"
			if r.Stale {
				mark = "!"
			}
		}
		extra := r.Version
		if !r.Date.IsZero() {
			extra = "updated " + ago(r.Date)
		}
		if r.Stale {
			extra += " (outdated)"
		}
		lat := "-"
		if r.Latency > 0 {
			lat = r.Latency.Round(time.Millisecond).String()
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", mark, r.Name, lat, r.Status, extra)
	}
	w.Flush()
}

func releaseDate(body []byte) (time.Time, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "Date:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "Date:"))
		for _, layout := range []string{time.RFC1123, "Mon, 2 Jan 2006 15:04:05 MST"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func cmpVer(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return -1
	}
	if b == "" {
		return 1
	}
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(leadingDigits(pa[i]))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(leadingDigits(pb[i]))
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func shortErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "Client.Timeout"), strings.Contains(s, "deadline exceeded"):
		return "timeout"
	case strings.Contains(s, "no such host"):
		return "DNS failed"
	case strings.Contains(s, "connection refused"):
		return "refused"
	case strings.Contains(s, "connection reset"):
		return "reset (filtered?)"
	case strings.Contains(s, "certificate"):
		return "TLS error"
	}
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
