package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var errManual = errors.New("manual step required")

type adapter struct {
	kind      string
	keep      int // how many mirrors to configure: the best, then fallbacks
	available func() error
	current   func() string
	snapshot  func() (prev string, had bool)
	value     func(urls []string) string // what current() reports after set(urls)
	set       func(urls []string) error
	restore   func(prev string, had bool) error
	printCmd  func(urls []string) string
}

var toolOrder = []string{"pip", "npm", "go", "docker", "apt"}

var adapters = map[string]adapter{
	"pip":    pipAdapter(),
	"npm":    npmAdapter(),
	"go":     goAdapter(),
	"docker": dockerAdapter(),
	"apt":    aptAdapter(),
}

// Environment variables beat config files: if one is set, `pip config set`
// and friends succeed but change nothing.
var overrideEnv = map[string][]string{
	"pip": {"PIP_INDEX_URL"},
	"npm": {"NPM_CONFIG_REGISTRY", "npm_config_registry"},
	"go":  {"GOPROXY"},
}

func envOverride(tool string) string {
	for _, k := range overrideEnv[tool] {
		if v := os.Getenv(k); v != "" {
			return k + "=" + v
		}
	}
	return ""
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %v %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func need(bin string) func() error {
	return func() error {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not installed", bin)
		}
		return nil
	}
}

func first(urls []string) string {
	if len(urls) == 0 {
		return ""
	}
	return urls[0]
}

func describe(urls []string) string {
	if len(urls) == 0 {
		return "the official registry"
	}
	return strings.Join(urls, ", ")
}

func pipBin() string {
	for _, b := range []string{"pip3", "pip"} {
		if _, err := exec.LookPath(b); err == nil {
			return b
		}
	}
	return ""
}

func pipAdapter() adapter {
	get := func() string {
		v, _ := run(pipBin(), "config", "get", "global.index-url")
		return v
	}
	setOne := func(u string) error {
		_, err := run(pipBin(), "config", "--user", "set", "global.index-url", u)
		return err
	}
	return adapter{
		kind: "pypi",
		keep: 1,
		available: func() error {
			if pipBin() == "" {
				return errors.New("pip not installed")
			}
			return nil
		},
		current: get,
		snapshot: func() (string, bool) {
			v := get()
			return v, v != ""
		},
		value: first,
		set:   func(urls []string) error { return setOne(urls[0]) },
		restore: func(prev string, had bool) error {
			if had {
				return setOne(prev)
			}
			_, err := run(pipBin(), "config", "--user", "unset", "global.index-url")
			return err
		},
		printCmd: func(urls []string) string {
			bin := pipBin()
			if bin == "" {
				bin = "pip"
			}
			return bin + " config --user set global.index-url " + urls[0]
		},
	}
}

func npmAdapter() adapter {
	get := func() string {
		v, _ := run("npm", "config", "get", "registry")
		return v
	}
	setOne := func(u string) error {
		_, err := run("npm", "config", "set", "registry", u)
		return err
	}
	return adapter{
		kind:      "npm",
		keep:      1,
		available: need("npm"),
		current:   get,
		snapshot: func() (string, bool) {
			v := get()
			return v, v != ""
		},
		value: first,
		set:   func(urls []string) error { return setOne(urls[0]) },
		restore: func(prev string, had bool) error {
			if had {
				return setOne(prev)
			}
			_, err := run("npm", "config", "delete", "registry")
			return err
		},
		printCmd: func(urls []string) string {
			return "npm config set registry " + urls[0]
		},
	}
}

const goDefaultProxy = "https://proxy.golang.org,direct"

// goProxyValue joins mirrors with "|", so go moves on to the next one on
// any error, not only on 404/410 as with ",".
func goProxyValue(urls []string) string {
	return strings.Join(urls, "|") + ",direct"
}

func goAdapter() adapter {
	get := func() string {
		v, _ := run("go", "env", "GOPROXY")
		return v
	}
	return adapter{
		kind:      "go",
		keep:      2,
		available: need("go"),
		current:   get,
		snapshot: func() (string, bool) {
			v := get()
			return v, v != "" && v != goDefaultProxy
		},
		value: goProxyValue,
		set: func(urls []string) error {
			_, err := run("go", "env", "-w", "GOPROXY="+goProxyValue(urls))
			return err
		},
		restore: func(prev string, had bool) error {
			if had {
				_, err := run("go", "env", "-w", "GOPROXY="+prev)
				return err
			}
			_, err := run("go", "env", "-u", "GOPROXY")
			return err
		},
		printCmd: func(urls []string) string {
			// quoted: "|" means pipe to a shell
			return "go env -w 'GOPROXY=" + goProxyValue(urls) + "'"
		},
	}
}

const dockerDaemon = "/etc/docker/daemon.json"

func dockerAdapter() adapter {
	return adapter{
		kind:      "docker",
		keep:      3, // dockerd tries registry-mirrors in order
		available: need("docker"),
		current: func() string {
			b, err := os.ReadFile(dockerDaemon)
			if err != nil {
				return ""
			}
			var cfg struct {
				Mirrors []string `json:"registry-mirrors"`
			}
			if json.Unmarshal(b, &cfg) != nil || len(cfg.Mirrors) == 0 {
				return ""
			}
			return cfg.Mirrors[0]
		},
		snapshot: func() (string, bool) {
			b, err := os.ReadFile(dockerDaemon)
			return string(b), err == nil
		},
		value: first,
		set:   dockerSet,
		restore: func(prev string, had bool) error {
			if err := needRoot(); err != nil {
				return err
			}
			var err error
			if had {
				err = writeFileAtomic(dockerDaemon, []byte(prev), 0o644)
			} else {
				err = os.Remove(dockerDaemon)
				if errors.Is(err, os.ErrNotExist) {
					err = nil
				}
			}
			if err != nil {
				return err
			}
			return restartDocker()
		},
		printCmd: dockerHint,
	}
}

func dockerHint(urls []string) string {
	if len(urls) == 0 {
		return "Remove the \"registry-mirrors\" line from Docker's daemon.json (Docker Desktop: Settings → Docker Engine), then restart Docker."
	}
	b, _ := json.Marshal(urls)
	return fmt.Sprintf("Add this to Docker's daemon.json (Docker Desktop: Settings → Docker Engine), then restart Docker:\n  \"registry-mirrors\": %s", b)
}

func dockerSet(urls []string) error {
	if runtime.GOOS != "linux" {
		fmt.Println("  " + dockerHint(urls))
		return errManual
	}
	if err := needRoot(); err != nil {
		return err
	}
	prev, readErr := os.ReadFile(dockerDaemon)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	hadPrev := readErr == nil

	cfg := map[string]interface{}{}
	if hadPrev && len(bytes.TrimSpace(prev)) > 0 {
		if err := json.Unmarshal(prev, &cfg); err != nil {
			return fmt.Errorf("%s is not valid JSON, not touching it: %v", dockerDaemon, err)
		}
	}
	if len(urls) == 0 {
		delete(cfg, "registry-mirrors")
	} else {
		cfg["registry-mirrors"] = urls
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	if err := writeFileAtomic(dockerDaemon, append(out, '\n'), 0o644); err != nil {
		return err
	}
	if err := restartDocker(); err != nil {
		// Put the old file back so Docker comes up again.
		if hadPrev {
			_ = writeFileAtomic(dockerDaemon, prev, 0o644)
		} else {
			_ = os.Remove(dockerDaemon)
		}
		_ = restartDocker()
		return fmt.Errorf("Docker didn't start with the new config, rolled back: %v", err)
	}
	return nil
}

func restartDocker() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		fmt.Println("  restart Docker yourself to apply the change")
		return nil
	}
	if _, err := run("systemctl", "restart", "docker"); err != nil {
		return err
	}
	fmt.Println("  Docker restarted")
	return nil
}

func needRoot() error {
	if runtime.GOOS == "linux" && os.Geteuid() != 0 {
		return errors.New("needs root: run it again with sudo")
	}
	return nil
}

// ---- undo state ----

type change struct {
	Prev string    `json:"prev"`
	Had  bool      `json:"had"`
	Set  *string   `json:"set,omitempty"` // nil in state files from older versions
	When time.Time `json:"when"`
}

// System-wide settings are changed with sudo; keeping their undo info in
// root's home would hide it from a plain `rahyab undo` or `rahyab status`.
func isSystemTool(tool string) bool {
	return runtime.GOOS == "linux" && (tool == "docker" || tool == "apt")
}

func stateStores() []bool {
	if runtime.GOOS == "linux" {
		return []bool{false, true}
	}
	return []bool{false}
}

func statePath(system bool) string {
	if system {
		return "/var/lib/rahyab/state.json"
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "rahyab", "state.json")
}

func loadState(system bool) (map[string]change, error) {
	st := map[string]change{}
	b, err := os.ReadFile(statePath(system))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		// Never start fresh silently: that would lose the original settings.
		return nil, fmt.Errorf("%s is corrupt: %v", statePath(system), err)
	}
	return st, nil
}

func saveState(system bool, st map[string]change) error {
	p := statePath(system)
	if len(st) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return writeFileAtomic(p, b, 0o644)
}

// writeFileAtomic never leaves a half-written file behind, even if the
// process dies mid-write.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".rahyab-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func applyChange(tool string, ad adapter, urls []string) error {
	sys := isSystemTool(tool)
	if sys {
		if err := needRoot(); err != nil {
			return err
		}
	}
	st, err := loadState(sys)
	if err != nil {
		return fmt.Errorf("undo info unreadable, changing nothing: %v", err)
	}
	old, seen := st[tool]
	ch := old
	if !seen {
		prev, had := ad.snapshot()
		ch = change{Prev: prev, Had: had, When: time.Now()}
	}
	v := ad.value(urls)
	ch.Set = &v
	st[tool] = ch
	// Save the undo info before touching anything.
	if err := saveState(sys, st); err != nil {
		return fmt.Errorf("couldn't save undo info, changing nothing: %v", err)
	}
	if err := ad.set(urls); err != nil {
		if seen {
			st[tool] = old
		} else {
			delete(st, tool)
		}
		_ = saveState(sys, st)
		if errors.Is(err, errManual) {
			return nil
		}
		return err
	}
	fmt.Printf("  ✓ %s now uses %s\n", tool, describe(urls))
	return nil
}
