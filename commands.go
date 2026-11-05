package main

import (
	"errors"
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
		{"Docker Hub (pull)", upstream["docker"].URL},
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
			var code int
			var d time.Duration
			var err error
			if ch.URL == upstream["docker"].URL {
				// A 401 from /v2/ isn't enough: a pull also needs a token and a manifest.
				code, d, err = probeDocker(c, ch.URL)
			} else {
				code, _, d, err = httpGet(c, ch.URL, "")
			}
			r := row{name: ch.Name, lat: d.Round(time.Millisecond).String()}
			switch {
			case err != nil:
				r.verdict = "✗ " + shortErr(err)
			case code == 403 || code == 451:
				r.verdict = fmt.Sprintf("✗ HTTP %d (blocked)", code)
			case code < 400:
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
	for _, t := range toolOrder {
		if ov := envOverride(t); ov != "" {
			fmt.Printf("\nnote: %s is set in your environment; it overrides %s's config file.", ov, t)
		}
	}
	fmt.Println("\n\nRun `rahyab fix` to switch your tools to the best working mirror.")
	return nil
}

func runFix(o options) error {
	ml, err := loadMirrors(o.mirrors, o.offline, o.timeout)
	if err != nil {
		return err
	}
	noteAptMirrors(ml)

	implicit := len(o.tools) == 0
	tools := o.tools
	if implicit {
		for _, t := range toolOrder {
			if adapters[t].available() == nil {
				tools = append(tools, t)
			}
		}
		if len(tools) == 0 {
			return fmt.Errorf("none of %s found", strings.Join(toolOrder, ", "))
		}
	}

	// pip/npm/go settings are per-user, docker/apt are system-wide. With no
	// tools named, `rahyab fix` does the first group and `sudo rahyab fix`
	// the second, so neither ends up configured for the wrong account.
	changes := !o.dryRun && !o.print
	sudoUser := isRoot() && os.Getenv("SUDO_USER") != ""
	var needSudo, skippedUser []string

	c := newClient(o.timeout)
	failed := false
	for _, t := range tools {
		if implicit && changes {
			if isSystemTool(t) && needRoot() != nil {
				needSudo = append(needSudo, t)
				continue
			}
			if sudoUser && !isSystemTool(t) {
				skippedUser = append(skippedUser, t)
				continue
			}
		}
		if !implicit && changes && sudoUser && !isSystemTool(t) {
			fmt.Printf("note: running with sudo, so %s will be set up for root, not for you.\n", t)
		}
		if err := fixOne(c, o, ml, t); err != nil {
			fmt.Printf("  %s: %v\n", t, err)
			failed = true
		}
	}
	if len(needSudo) > 0 {
		fmt.Printf("\n%s change system files: run `sudo rahyab fix` for them.\n", strings.Join(needSudo, " and "))
	}
	if len(skippedUser) > 0 {
		fmt.Printf("\nskipped %s under sudo (they're per-user): run `rahyab fix` without sudo.\n", strings.Join(skippedUser, ", "))
	}
	if failed {
		return errors.New("some tools could not be fixed (see above)")
	}
	return nil
}

func fixOne(c *http.Client, o options, ml MirrorList, tool string) error {
	ad, ok := adapters[tool]
	if !ok {
		return fmt.Errorf("unknown tool (use: %s)", strings.Join(toolOrder, ", "))
	}
	// --print is for other machines (e.g. a Dockerfile), so the tool
	// doesn't have to be installed here.
	if !o.print {
		if err := ad.available(); err != nil {
			return err
		}
	}
	kind, codename := ad.kind, ""
	if tool == "apt" {
		var err error
		if kind, codename, err = detectApt(); err != nil {
			return err
		}
	}
	official := officialFor(tool, kind)
	list := append([]Mirror{official}, ml[kind]...)
	res := probeAll(c, kind, list, codename)
	printResults(tool, res)
	best := res[0]
	if !best.OK {
		return fmt.Errorf("no working mirror right now; try again later or pass --mirrors=FILE")
	}

	var urls []string
	if kind == "docker" {
		// Docker Hub isn't a mirror: "use the official one" means no registry-mirrors.
		if best.URL != official.URL {
			urls = pick(res, ad.keep, official.URL)
		}
	} else {
		urls = pick(res, ad.keep, "")
	}

	if o.print {
		fmt.Println(ad.printCmd(urls))
		return nil
	}
	if cur := ad.current(); !(tool == "apt" && cur == "") && sameURL(cur, best.URL, official.URL) {
		fmt.Printf("  ✓ already using the best option (%s)\n", best.Name)
		return nil
	}
	if ov := envOverride(tool); ov != "" {
		return fmt.Errorf("%s is set in your environment and overrides the config file; remove it from your shell profile, then run this again", ov)
	}
	fmt.Printf("  → best: %s  %s\n", best.Name, best.URL)
	if len(urls) > 1 {
		fmt.Printf("    fallback: %s\n", strings.Join(urls[1:], ", "))
	}
	if o.dryRun {
		fmt.Println("  (dry run, nothing changed)")
		return nil
	}
	if !confirm(o, fmt.Sprintf("  switch %s to %s?", tool, best.Name)) {
		return nil
	}
	return applyChange(tool, ad, urls)
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
			val = "(main archive not recognized, see /etc/apt/sources.list.d)"
		default:
			val = ad.current()
			if val == "" {
				val = "(default)"
			}
		}
		if ov := envOverride(t); ov != "" {
			val += "   [overridden by " + ov + "]"
		}
		fmt.Fprintf(w, "  %s\t%s\n", t, val)
	}
	w.Flush()

	var names []string
	for _, sys := range stateStores() {
		st, err := loadState(sys)
		if err != nil {
			fmt.Println("warning:", err)
			continue
		}
		for k := range st {
			names = append(names, k)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		fmt.Printf("\nChanged by rahyab: %s. Run `rahyab undo` to restore.\n", strings.Join(names, ", "))
	}
	return nil
}

func runUndo(o options) error {
	found := false
	for _, sys := range stateStores() {
		st, err := loadState(sys)
		if err != nil {
			return err
		}
		canWrite := !sys || needRoot() == nil
		changed := false
		for _, t := range toolOrder {
			ch, ok := st[t]
			if !ok || (len(o.tools) > 0 && !contains(o.tools, t)) {
				continue
			}
			found = true
			ad := adapters[t]
			if !canWrite {
				fmt.Printf("  %s was changed with sudo; run: sudo rahyab undo %s\n", t, t)
				continue
			}
			// Don't clobber a setting the user changed by hand after rahyab.
			if ch.Set != nil && !o.force {
				if cur := ad.current(); normURL(cur) != normURL(*ch.Set) {
					fmt.Printf("  ! %s was changed after rahyab (now %q); skipped. Use --force to restore it anyway.\n", t, cur)
					continue
				}
			}
			if o.dryRun {
				fmt.Printf("  would restore %s\n", t)
				continue
			}
			if err := ad.restore(ch.Prev, ch.Had); err != nil {
				fmt.Printf("  ✗ %s: %v\n", t, err)
				continue
			}
			delete(st, t)
			changed = true
			fmt.Printf("  ✓ %s restored\n", t)
		}
		if changed {
			if err := saveState(sys, st); err != nil {
				return err
			}
		}
	}
	if !found {
		fmt.Println("Nothing to undo.")
	}
	return nil
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

// normURL reduces a setting to its first URL without scheme or trailing
// slash, so "https://a/|https://b,direct" compares equal to "http://a".
func normURL(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, ",|"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	return strings.TrimRight(s, "/")
}

func sameURL(cur, best, official string) bool {
	if strings.TrimSpace(cur) == "" {
		cur = official
	}
	return normURL(cur) == normURL(best)
}

func isRoot() bool {
	return runtime.GOOS != "windows" && os.Geteuid() == 0
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
