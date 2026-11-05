package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
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

// Mirrors change often; the list in the repo is newer than the one
// built into an old binary.
const remoteMirrorsURL = "https://raw.githubusercontent.com/Ali30mhi/rahyab/main/mirrors.json"

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

// loadMirrors uses --mirrors=FILE if given, otherwise the latest list from
// the repo, falling back to the built-in one.
func loadMirrors(path string, offline bool, timeout time.Duration) (MirrorList, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return parseMirrors(b)
	}
	if !offline {
		m, err := fetchMirrors(timeout)
		if err == nil {
			return m, nil
		}
		fmt.Printf("note: couldn't fetch the latest mirror list (%s), using the built-in one.\n", shortErr(err))
	}
	return parseMirrors(embeddedMirrors)
}

func fetchMirrors(timeout time.Duration) (MirrorList, error) {
	if timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	code, body, _, err := httpGet(newClient(timeout), remoteMirrorsURL, "")
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", code)
	}
	m, err := parseMirrors(body)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, errors.New("empty list")
	}
	return m, nil
}

func parseMirrors(data []byte) (MirrorList, error) {
	var m MirrorList
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("mirror list: %v", err)
	}
	for kind, list := range m {
		for _, mm := range list {
			u, err := url.Parse(mm.URL)
			if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
				return nil, fmt.Errorf("mirror list: bad URL %q in %q", mm.URL, kind)
			}
		}
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

// httpGet returns the time to the first response byte, not the full
// download: all mirrors are probed at once, so on a slow link the total
// time measures shared bandwidth more than the server.
func httpGet(c *http.Client, target, accept string) (int, []byte, time.Duration, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, 0, err
	}
	req.Header.Set("User-Agent", "rahyab/"+version)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	start := time.Now()
	var ttfb time.Duration
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { ttfb = time.Since(start) }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, time.Since(start), err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if ttfb == 0 {
		ttfb = time.Since(start)
	}
	return resp.StatusCode, body, ttfb, err
}

func join(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func httpVerdict(code int, err error) (string, bool) {
	switch {
	case err != nil:
		return shortErr(err), false
	case code == 403 || code == 451:
		return fmt.Sprintf("HTTP %d (blocked)", code), false
	case code != http.StatusOK:
		return fmt.Sprintf("HTTP %d", code), false
	}
	return "ok", true
}

var pipFileRe = regexp.MustCompile(`pip-(\d+(?:\.\d+)+)(?:\.tar\.gz|-py)`)

func probe(c *http.Client, kind string, m Mirror, codename string) Result {
	r := Result{Mirror: m}
	if kind == "docker" {
		code, d, err := probeDocker(c, m.URL)
		r.Latency = d
		r.Status, r.OK = httpVerdict(code, err)
		return r
	}

	var target, accept string
	switch kind {
	case "pypi":
		target = join(m.URL, "pip/")
	case "npm":
		target = join(m.URL, "semver")
		accept = "application/vnd.npm.install-v1+json"
	case "go":
		target = join(m.URL, "golang.org/x/text/@latest")
	case "ubuntu", "debian":
		target = join(m.URL, "dists/"+codename+"-updates/Release")
	default:
		r.Status = "unknown kind " + kind
		return r
	}
	code, body, d, err := httpGet(c, target, accept)
	r.Latency = d
	var ok bool
	if r.Status, ok = httpVerdict(code, err); !ok {
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
		// go checks every module against sum.golang.org, through the proxy
		// only if the proxy says it supports that, otherwise directly.
		if m.URL != upstream["go"].URL {
			sc, _, _, err := httpGet(c, join(m.URL, "sumdb/sum.golang.org/supported"), "")
			if err != nil || sc != http.StatusOK {
				r.Status = "no checksum (sumdb) proxy"
				return r
			}
		}
	case "ubuntu", "debian":
		t, ok := releaseDate(body)
		if !ok {
			r.Status = "unexpected response"
			return r
		}
		r.Date = t
	}
	r.OK, r.Status = true, "ok"
	return r
}

const dockerManifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

// probeDocker does what `docker pull` does: fetch a manifest, getting a
// token first if the registry asks for one. A 401 on /v2/ alone only
// proves the registry answered; the token server can still be blocked.
// HEAD requests don't count toward Docker Hub's pull rate limit.
func probeDocker(c *http.Client, base string) (int, time.Duration, error) {
	start := time.Now()
	target := join(base, "v2/library/alpine/manifests/latest")
	code, hdr, err := headManifest(c, target, "")
	if err != nil || code != http.StatusUnauthorized {
		return code, time.Since(start), err
	}
	tok, code, err := dockerToken(c, hdr.Get("WWW-Authenticate"))
	if err != nil || tok == "" {
		return code, time.Since(start), err
	}
	code, _, err = headManifest(c, target, tok)
	return code, time.Since(start), err
}

func headManifest(c *http.Client, target, token string) (int, http.Header, error) {
	req, err := http.NewRequest(http.MethodHead, target, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "rahyab/"+version)
	req.Header.Set("Accept", dockerManifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header, nil
}

var authParamRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// dockerToken returns ("", code, nil) when the token server answers with
// an error status, so the caller can report e.g. a 403.
func dockerToken(c *http.Client, challenge string) (string, int, error) {
	p := map[string]string{}
	for _, m := range authParamRe.FindAllStringSubmatch(challenge, -1) {
		p[m[1]] = m[2]
	}
	u, err := url.Parse(p["realm"])
	if err != nil || p["realm"] == "" {
		return "", 0, errors.New("bad auth challenge")
	}
	q := u.Query()
	if s := p["service"]; s != "" {
		q.Set("service", s)
	}
	scope := p["scope"]
	if scope == "" {
		scope = "repository:library/alpine:pull"
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()

	code, body, _, err := httpGet(c, u.String(), "")
	if err != nil || code != http.StatusOK {
		return "", code, err
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &t) != nil {
		return "", 0, errors.New("bad token response")
	}
	tok := t.Token
	if tok == "" {
		tok = t.AccessToken
	}
	if tok == "" {
		return "", 0, errors.New("empty token")
	}
	return tok, code, nil
}

// probeAll probes each mirror twice and keeps the faster time: the first
// request also pays for DNS and the TLS handshake.
func probeAll(c *http.Client, kind string, list []Mirror, codename string) []Result {
	res := make([]Result, len(list))
	var wg sync.WaitGroup
	for i, m := range list {
		wg.Add(1)
		go func(i int, m Mirror) {
			defer wg.Done()
			r := probe(c, kind, m, codename)
			if r.OK {
				if r2 := probe(c, kind, m, codename); r2.OK && r2.Latency < r.Latency {
					r.Latency = r2.Latency
				}
			}
			res[i] = r
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

// pick returns up to n URLs: the best result, then working, up-to-date
// fallbacks. skip excludes one URL (Docker Hub isn't a mirror of itself).
func pick(res []Result, n int, skip string) []string {
	var out []string
	for i, r := range res {
		if !r.OK || r.URL == skip || (i > 0 && r.Stale) {
			continue
		}
		out = append(out, r.URL)
		if len(out) == n {
			break
		}
	}
	return out
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
