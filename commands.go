package main

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

func runDoctor(o options) error {
	fmt.Println("Checking direct access (nothing will be changed)...")
	c := newClient(o.timeout)
	checks := []Mirror{
		{"PyPI (pip)", "https://pypi.org/simple/pip/"},
		{"npm registry", "https://registry.npmjs.org/-/ping"},
		{"Go proxy", "https://proxy.golang.org/golang.org/x/text/@latest"},
		{"Docker Hub", "https://registry-1.docker.io/v2/"},
		{"GitHub", "https://github.com/"},
		{"Google", "https://www.google.com/"},
	}
	type row struct{ name, verdict, lat string }
	rows := make([]row, len(checks))
	var wg sync.WaitGroup
	for i, ch := range checks {
		wg.Add(1)
		go func(i int, ch Mirror) {
			defer wg.Done()
			code, _, d, err := httpGet(c, ch.URL, "")
			r := row{name: ch.Name, lat: d.Round(time.Millisecond).String()}
			switch {
			case err != nil:
				r.verdict = "✗ " + shortErr(err)
			case code == 403 || code == 451:
				r.verdict = fmt.Sprintf("✗ HTTP %d (blocked by sanctions)", code)
			case code < 400 || code == 401:
				r.verdict = "✓ ok"
			default:
				r.verdict = fmt.Sprintf("? HTTP %d", code)
			}
			rows[i] = r
		}(i, ch)
	}
	wg.Wait()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", r.name, r.verdict, r.lat)
	}
	w.Flush()
	fmt.Println("\nRun `rahyab fix` to switch your tools to the best working mirror.")
	return nil
}

func runFix(o options) error {
	ml, err := loadMirrors(o.mirrors)
	if err != nil {
		return err
	}
	tools := o.tools
	if len(tools) == 0 {
		for _, t := range toolOrder {
			if adapters[t].available() == nil {
				tools = append(tools, t)
			}
		}
		if len(tools) == 0 {
			return fmt.Errorf("none of %s found", strings.Join(toolOrder, ", "))
		}
	}
	if isRoot() && os.Getenv("SUDO_USER") != "" {
		for _, t := range tools {
			if t == "pip" || t == "npm" || t == "go" {
				fmt.Println("note: running with sudo, so pip/npm/go settings will be set for root, not for you.")
				fmt.Println("      Use sudo only for `rahyab fix docker` and `rahyab fix apt`.")
				break
			}
		}
	}
	c := newClient(o.timeout)
	for _, t := range tools {
		if err := fixOne(c, o, ml, t); err != nil {
			fmt.Printf("  %s: %v\n", t, err)
		}
	}
	return nil
}

func fixOne(c *http.Client, o options, ml MirrorList, tool string) error {
	ad, ok := adapters[tool]
	if !ok {
		return fmt.Errorf("unknown tool (use: %s)", strings.Join(toolOrder, ", "))
	}
	if err := ad.available(); err != nil {
		return err
	}
	kind, codename := ad.kind, ""
	if tool == "apt" {
		var err error
		if kind, codename, err = detectApt(); err != nil {
			return err
		}
	}
	list := append([]Mirror{upstream[kind]}, ml[kind]...)
	res := probeAll(c, kind, list, codename)
	printResults(tool, res)

	best := res[0]
	if !best.OK {
		return fmt.Errorf("no working mirror right now; try again later or pass --mirrors=FILE")
	}
	if cur := ad.current(); !(tool == "apt" && cur == "") && sameURL(cur, best.URL, upstream[kind].URL) {
		fmt.Printf("  ✓ already using the best option (%s)\n", best.Name)
		return nil
	}
	fmt.Printf("  → best: %s  %s\n", best.Name, best.URL)
	if o.dryRun {
		fmt.Println("  (dry run, nothing changed)")
		return nil
	}
	if tool != "apt" && !confirm(o, fmt.Sprintf("  switch %s to %s?", tool, best.Name)) {
		return nil
	}
	return applyChange(tool, ad, best.URL)
}

func runStatus() error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, t := range toolOrder {
		ad := adapters[t]
		var val string
		switch {
		case ad.available() != nil:
			val = "not available"
		case t == "apt" && ad.current() == "":
			val = "(see /etc/apt/sources.list.d)"
		default:
			val = ad.current()
			if val == "" {
				val = "(default)"
			}
		}
		fmt.Fprintf(w, "  %s\t%s\n", t, val)
	}
	w.Flush()
	if st := loadState(); len(st) > 0 {
		names := make([]string, 0, len(st))
		for k := range st {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Printf("\nChanged by rahyab: %s. Run `rahyab undo` to restore.\n", strings.Join(names, ", "))
	}
	return nil
}

func runUndo(o options) error {
	st := loadState()
	if len(st) == 0 {
		fmt.Println("Nothing to undo.")
		return nil
	}
	for _, t := range toolOrder {
		ch, ok := st[t]
		if !ok {
			continue
		}
		if o.dryRun {
			fmt.Printf("  would restore %s\n", t)
			continue
		}
		if err := adapters[t].restore(ch.Prev, ch.Had); err != nil {
			fmt.Printf("  ✗ %s: %v\n", t, err)
			continue
		}
		delete(st, t)
		fmt.Printf("  ✓ %s restored\n", t)
	}
	if o.dryRun {
		return nil
	}
	return saveState(st)
}

func detectApt() (kind, codename string, err error) {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "", "", err
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[k] = strings.Trim(v, `"'`)
		}
	}
	like := kv["ID"] + " " + kv["ID_LIKE"]
	switch {
	case kv["UBUNTU_CODENAME"] != "":
		kind, codename = "ubuntu", kv["UBUNTU_CODENAME"]
	case strings.Contains(like, "ubuntu"):
		kind, codename = "ubuntu", kv["VERSION_CODENAME"]
	case strings.Contains(like, "debian"):
		kind, codename = "debian", kv["VERSION_CODENAME"]
	default:
		return "", "", fmt.Errorf("unsupported distro %q", kv["ID"])
	}
	if codename == "" {
		return "", "", fmt.Errorf("couldn't detect release codename")
	}
	// Ubuntu on ARM etc. lives on ports.ubuntu.com, which these mirrors don't carry.
	if kind == "ubuntu" && runtime.GOARCH != "amd64" && runtime.GOARCH != "386" {
		return "", "", fmt.Errorf("ubuntu on %s uses ports.ubuntu.com, which these mirrors don't carry", runtime.GOARCH)
	}
	return kind, codename, nil
}

func sameURL(cur, best, official string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		if i := strings.Index(s, ","); i >= 0 {
			s = s[:i]
		}
		return strings.TrimRight(s, "/")
	}
	if strings.TrimSpace(cur) == "" {
		cur = official
	}
	return norm(cur) == norm(best)
}

func isRoot() bool {
	return runtime.GOOS != "windows" && os.Geteuid() == 0
}
