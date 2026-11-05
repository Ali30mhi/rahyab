package main

import (
	"strings"
	"testing"
	"time"
)

func TestIsMainArchive(t *testing.T) {
	cases := map[string]bool{
		"http://archive.ubuntu.com/ubuntu":            true,
		"http://ir.archive.ubuntu.com/ubuntu/":        true,
		"http://deb.debian.org/debian":                true,
		"https://mirror.arvancloud.ir/ubuntu":         true,
		"http://security.ubuntu.com/ubuntu":           false,
		"http://deb.debian.org/debian-security":       false,
		"http://ports.ubuntu.com/ubuntu-ports":        false,
		"https://download.docker.com/linux/ubuntu":    false,
		"https://ppa.launchpadcontent.net/a/b/ubuntu": false,
		"cdrom:[Debian":                               false,
	}
	for in, want := range cases {
		if got := isMainArchive(in); got != want {
			t.Errorf("isMainArchive(%q)=%v want %v", in, got, want)
		}
	}
}

func TestRewriteSourcesDeb822(t *testing.T) {
	in := "Types: deb\nURIs: http://archive.ubuntu.com/ubuntu/\nSuites: noble noble-updates\nComponents: main restricted\n\n" +
		"Types: deb\nURIs: http://security.ubuntu.com/ubuntu/\nSuites: noble-security\nComponents: main\n"
	out, n := rewriteSources(in, "https://mirror.arvancloud.ir/ubuntu")
	want := strings.Replace(in, "URIs: http://archive.ubuntu.com/ubuntu/", "URIs: https://mirror.arvancloud.ir/ubuntu", 1)
	if n != 1 || out != want {
		t.Fatalf("n=%d\n%s", n, out)
	}
}

func TestRewriteSourcesClassic(t *testing.T) {
	keep := "deb [signed-by=/k.gpg] https://download.docker.com/linux/debian trixie stable\n" +
		"deb http://deb.debian.org/debian-security trixie-security main\n" +
		"# deb http://deb.debian.org/debian trixie-backports main\n"
	in := "# main\n" +
		"deb http://deb.debian.org/debian trixie main\n" +
		"deb-src http://deb.debian.org/debian trixie main\n" + keep
	want := "# main\n" +
		"deb https://mirror.arvancloud.ir/debian trixie main\n" +
		"deb-src https://mirror.arvancloud.ir/debian trixie main\n" + keep
	out, n := rewriteSources(in, "https://mirror.arvancloud.ir/debian")
	if n != 2 || out != want {
		t.Fatalf("n=%d\n%s", n, out)
	}
}

func TestRewriteSourcesDedupes(t *testing.T) {
	out, n := rewriteSources("URIs: http://archive.ubuntu.com/ubuntu http://ir.archive.ubuntu.com/ubuntu\n", "https://m.example/ubuntu")
	if n != 1 || out != "URIs: https://m.example/ubuntu\n" {
		t.Fatalf("n=%d %q", n, out)
	}
}

func TestAptPrintCmd(t *testing.T) {
	if s := aptPrintCmd([]string{"https://mirror.arvancloud.ir/debian/"}); !strings.Contains(s, `deb\.debian\.org/debian`) ||
		!strings.Contains(s, "#https://mirror.arvancloud.ir/debian\\1#") {
		t.Fatalf("got %s", s)
	}
}

func TestRankPrefersFresherWhenClose(t *testing.T) {
	now := time.Now()
	res := []Result{
		{Mirror: Mirror{Name: "Shatel"}, OK: true, Latency: 63 * time.Millisecond, Date: now.Add(-16 * time.Hour)},
		{Mirror: Mirror{Name: "Official"}, OK: false},
		{Mirror: Mirror{Name: "IranServer"}, OK: true, Latency: 67 * time.Millisecond, Date: now.Add(-40 * time.Hour)},
		{Mirror: Mirror{Name: "Arvan"}, OK: true, Latency: 77 * time.Millisecond, Date: now.Add(-4 * time.Hour)},
	}
	rank(res)
	var got []string
	for _, r := range res {
		got = append(got, r.Name)
	}
	if strings.Join(got, ",") != "Arvan,Shatel,IranServer,Official" {
		t.Fatalf("order: %v", got)
	}
}

func TestRankSpeedStillWinsForSmallLag(t *testing.T) {
	now := time.Now()
	res := []Result{
		{Mirror: Mirror{Name: "slow-fresh"}, OK: true, Latency: 300 * time.Millisecond, Date: now},
		{Mirror: Mirror{Name: "fast"}, OK: true, Latency: 20 * time.Millisecond, Date: now.Add(-10 * time.Hour)},
	}
	rank(res)
	if res[0].Name != "fast" {
		t.Fatalf("got %s first", res[0].Name)
	}
}
