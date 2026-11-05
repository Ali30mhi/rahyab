package main

import (
	"os"
	"testing"
	"time"
)

func TestMergeMirrors(t *testing.T) {
	a := MirrorList{"npm": {{"New", "https://new.example/"}, {"Liara", "https://package-mirror.liara.ir/repository/npm/"}}}
	b := MirrorList{"npm": {{"Liara", "https://package-mirror.liara.ir/repository/npm"}}, "pypi": {{"X", "https://x.example/simple/"}}}
	m := mergeMirrors(a, b)
	if len(m["npm"]) != 2 || m["npm"][0].Name != "New" || len(m["pypi"]) != 1 {
		t.Fatalf("%+v", m)
	}
}

func TestLoadMirrorsUsesStaleCacheOffline(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{"XDG_CACHE_HOME", "HOME", "LocalAppData"} {
		t.Setenv(k, dir)
	}
	p := mirrorCachePath()
	if p == "" {
		t.Skip("no cache dir on this system")
	}
	if err := writeFileAtomic(p, []byte(`{"npm":[{"name":"Cached","url":"https://cached.example/"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(p, old, old)

	// offline=true: must use the stale cache and never touch the network.
	ml, err := loadMirrors("", true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !hasURL(ml["npm"], "https://cached.example/") {
		t.Fatal("cached mirror missing")
	}
	builtin, _ := parseMirrors(embeddedMirrors)
	for _, m := range builtin["npm"] {
		if !hasURL(ml["npm"], m.URL) {
			t.Fatalf("built-in %s missing", m.URL)
		}
	}
}

func TestAllDNSFailed(t *testing.T) {
	if !allDNSFailed([]Result{{Status: "DNS failed"}, {Status: "skipped", Skipped: true}}) {
		t.Error("want true")
	}
	if allDNSFailed([]Result{{Status: "DNS failed"}, {Status: "timeout"}}) {
		t.Error("want false: one failed differently")
	}
	if allDNSFailed([]Result{{Skipped: true}}) {
		t.Error("want false: nothing probed")
	}
}

func TestPreferSumDB(t *testing.T) {
	res := []Result{
		{Mirror: Mirror{Name: "nosum"}, OK: true, NoSumDB: true},
		{Mirror: Mirror{Name: "broken"}},
		{Mirror: Mirror{Name: "sum"}, OK: true},
	}
	preferSumDB(res)
	if res[0].Name != "sum" || res[1].Name != "nosum" || res[2].Name != "broken" {
		t.Fatalf("order: %s %s %s", res[0].Name, res[1].Name, res[2].Name)
	}
}

func TestShortErrDNS(t *testing.T) {
	err := &mockErr{`Get "https://x.ir/": dial tcp: lookup x.ir on 8.8.8.8:53: read udp: i/o timeout`}
	if got := shortErr(err); got != "DNS failed" {
		t.Fatalf("got %q", got)
	}
}

type mockErr struct{ s string }

func (e *mockErr) Error() string { return e.s }

func TestDomesticHost(t *testing.T) {
	if h := domesticHost(); h == "" {
		t.Fatal("no Iranian host in the built-in list")
	}
}
