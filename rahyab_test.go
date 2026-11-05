package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedMirrorsValid(t *testing.T) {
	m, err := parseMirrors(embeddedMirrors)
	if err != nil {
		t.Fatal(err)
	}
	for kind, list := range m {
		if _, ok := upstream[kind]; !ok {
			t.Errorf("unknown kind %q in mirrors.json", kind)
		}
		seen := map[string]bool{}
		for _, mm := range list {
			if mm.Name == "" {
				t.Errorf("%s: mirror %q has no name", kind, mm.URL)
			}
			if seen[mm.URL] {
				t.Errorf("%s: duplicate %q", kind, mm.URL)
			}
			seen[mm.URL] = true
		}
	}
}

func TestParseMirrorsRejectsBadURL(t *testing.T) {
	if _, err := parseMirrors([]byte(`{"npm":[{"name":"x","url":"registry.example"}]}`)); err == nil {
		t.Fatal("want error for URL without scheme")
	}
}

func TestCmpVer(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"24.3", "24.2.1", 1},
		{"v0.21.0", "v0.9.0", 1},
		{"1.0", "1.0.0", 0},
		{"", "1.0", -1},
		{"2.0", "", 1},
	}
	for _, c := range cases {
		if got := cmpVer(c.a, c.b); got != c.want {
			t.Errorf("cmpVer(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAptURIs(t *testing.T) {
	cases := map[string][]string{
		"URIs: http://deb.debian.org/debian":                                            {"http://deb.debian.org/debian"},
		"deb [arch=amd64 signed-by=/k.gpg] http://archive.ubuntu.com/ubuntu noble main": {"http://archive.ubuntu.com/ubuntu"},
		"deb [arch=amd64] http://archive.ubuntu.com/ubuntu noble main":                  {"http://archive.ubuntu.com/ubuntu"},
		"# deb http://archive.ubuntu.com/ubuntu noble main":                             nil,
		"deb-src http://archive.ubuntu.com/ubuntu noble main":                           nil,
	}
	for in, want := range cases {
		if got := aptURIs(in); !reflect.DeepEqual(got, want) {
			t.Errorf("aptURIs(%q)=%v want %v", in, got, want)
		}
	}
}

func TestSameURL(t *testing.T) {
	off := "https://proxy.golang.org"
	cases := []struct {
		cur, best string
		want      bool
	}{
		{"", off, true},
		{"https://goproxy.cn|https://goproxy.io,direct", "https://goproxy.cn", true},
		{"https://goproxy.cn,direct", "https://goproxy.cn/", true},
		{"https://goproxy.io", "https://goproxy.cn", false},
	}
	for _, c := range cases {
		if got := sameURL(c.cur, c.best, off); got != c.want {
			t.Errorf("sameURL(%q,%q)=%v want %v", c.cur, c.best, got, c.want)
		}
	}
}

func TestPick(t *testing.T) {
	res := []Result{
		{Mirror: Mirror{URL: "a"}, OK: true},
		{Mirror: Mirror{URL: "hub"}, OK: true},
		{Mirror: Mirror{URL: "b"}, OK: true, Stale: true},
		{Mirror: Mirror{URL: "c"}, OK: false},
		{Mirror: Mirror{URL: "d"}, OK: true},
	}
	if got := pick(res, 3, "hub"); !reflect.DeepEqual(got, []string{"a", "d"}) {
		t.Errorf("pick=%v", got)
	}
	if got := pick(res, 1, ""); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("pick n=1 =%v", got)
	}
}

func TestMarkStale(t *testing.T) {
	v := []Result{{OK: true, Version: "24.3"}, {OK: true, Version: "24.2"}, {OK: false}}
	markStale(v)
	if v[0].Stale || !v[1].Stale || v[2].Stale {
		t.Errorf("versions: %+v", v)
	}
	now := time.Now()
	d := []Result{{OK: true, Date: now}, {OK: true, Date: now.Add(-72 * time.Hour)}}
	markStale(d)
	if d[0].Stale || !d[1].Stale {
		t.Errorf("dates: %+v", d)
	}
}

func TestReleaseDate(t *testing.T) {
	got, ok := releaseDate([]byte("Origin: Ubuntu\nDate: Thu, 05 Nov 2026 09:12:01 UTC\nSuite: noble-updates\n"))
	if !ok || got.Year() != 2026 || got.Month() != time.November {
		t.Fatalf("got %v %v", got, ok)
	}
}

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"pip", "--print", "--force", "--offline", "--timeout=3s", "NPM"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.print || !o.force || !o.offline || o.timeout != 3*time.Second || !reflect.DeepEqual(o.tools, []string{"pip", "npm"}) {
		t.Errorf("got %+v", o)
	}
	for _, bad := range []string{"--timeout=0s", "--timeout=x", "--bogus"} {
		if _, err := parseArgs([]string{bad}); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func dockerTestServer(t *testing.T, authStatus int) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if authStatus != http.StatusOK {
				w.WriteHeader(authStatus)
				return
			}
			w.Write([]byte(`{"token":"t0k"}`))
		case "/v2/library/alpine/manifests/latest":
			if r.Header.Get("Authorization") != "Bearer t0k" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeDocker(t *testing.T) {
	c := &http.Client{Timeout: 5 * time.Second}
	if r := probe(c, "docker", Mirror{URL: dockerTestServer(t, http.StatusOK).URL}, ""); !r.OK {
		t.Fatalf("token flow: want ok, got %q", r.Status)
	}
	r := probe(c, "docker", Mirror{URL: dockerTestServer(t, http.StatusForbidden).URL}, "")
	if r.OK || !strings.Contains(r.Status, "403") {
		t.Fatalf("blocked auth server: got ok=%v %q", r.OK, r.Status)
	}
}

func goTestServer(t *testing.T, sumdb bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/golang.org/x/text/@latest":
			w.Write([]byte(`{"Version":"v0.20.0"}`))
		case r.URL.Path == "/sumdb/sum.golang.org/supported" && sumdb:
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeGoNeedsSumdb(t *testing.T) {
	c := &http.Client{Timeout: 5 * time.Second}
	if r := probe(c, "go", Mirror{URL: goTestServer(t, true).URL}, ""); !r.OK || r.Version != "v0.20.0" {
		t.Fatalf("with sumdb: got ok=%v %q %q", r.OK, r.Status, r.Version)
	}
	if r := probe(c, "go", Mirror{URL: goTestServer(t, false).URL}, ""); r.OK {
		t.Fatal("without sumdb: want not ok")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	for _, s := range []string{"one", "two"} {
		if err := writeFileAtomic(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(p)
	if string(b) != "two" {
		t.Fatalf("got %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %d entries", len(entries))
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)

	v := "https://mirror/"
	if err := saveState(false, map[string]change{"pip": {Prev: "x", Had: true, Set: &v}}); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(false)
	if err != nil || st["pip"].Prev != "x" || st["pip"].Set == nil || *st["pip"].Set != v {
		t.Fatalf("got %+v, %v", st, err)
	}
	if err := saveState(false, map[string]change{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath(false)); !os.IsNotExist(err) {
		t.Fatal("empty state should remove the file")
	}

	if err := writeFileAtomic(statePath(false), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(false); err == nil {
		t.Fatal("corrupt state: want error")
	}
}
