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
	available func() error
	current   func() string
	snapshot  func() (prev string, had bool)
	set       func(url string) error
	restore   func(prev string, had bool) error
}

var toolOrder = []string{"pip", "npm", "go", "docker", "apt"}

var adapters = map[string]adapter{
	"pip":    pipAdapter(),
	"npm":    npmAdapter(),
	"go":     goAdapter(),
	"docker": dockerAdapter(),
	"apt":    aptAdapter(),
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
	set := func(url string) error {
		_, err := run(pipBin(), "config", "--user", "set", "global.index-url", url)
		return err
	}
	return adapter{
		kind: "pypi",
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
		set: set,
		restore: func(prev string, had bool) error {
			if had {
				return set(prev)
			}
			_, err := run(pipBin(), "config", "--user", "unset", "global.index-url")
			return err
		},
	}
}

func npmAdapter() adapter {
	get := func() string {
		v, _ := run("npm", "config", "get", "registry")
		return v
	}
	set := func(url string) error {
		_, err := run("npm", "config", "set", "registry", url)
		return err
	}
	return adapter{
		kind:      "npm",
		available: need("npm"),
		current:   get,
		snapshot: func() (string, bool) {
			v := get()
			return v, v != ""
		},
		set: set,
		restore: func(prev string, had bool) error {
			if had {
				return set(prev)
			}
			_, err := run("npm", "config", "delete", "registry")
			return err
		},
	}
}

const goDefaultProxy = "https://proxy.golang.org,direct"

func goAdapter() adapter {
	get := func() string {
		v, _ := run("go", "env", "GOPROXY")
		return v
	}
	return adapter{
		kind:      "go",
		available: need("go"),
		current:   get,
		snapshot: func() (string, bool) {
			v := get()
			return v, v != "" && v != goDefaultProxy
		},
		set: func(url string) error {
			_, err := run("go", "env", "-w", "GOPROXY="+url+",direct")
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
	}
}

const dockerDaemon = "/etc/docker/daemon.json"

func dockerAdapter() adapter {
	return adapter{
		kind:      "docker",
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
		set: dockerSet,
		restore: func(prev string, had bool) error {
			if err := needRoot(); err != nil {
				return err
			}
			var err error
			if had {
				err = os.WriteFile(dockerDaemon, []byte(prev), 0o644)
			} else {
				err = os.Remove(dockerDaemon)
				if errors.Is(err, os.ErrNotExist) {
					err = nil
				}
			}
			if err != nil {
				return err
			}
			restartDocker()
			return nil
		},
	}
}

func dockerSet(url string) error {
	if runtime.GOOS != "linux" {
		fmt.Printf("  Docker Desktop: open Settings → Docker Engine, add this line and click \"Apply & Restart\":\n    \"registry-mirrors\": [\"%s\"]\n", url)
		return errManual
	}
	if err := needRoot(); err != nil {
		return err
	}
	cfg := map[string]interface{}{}
	if b, err := os.ReadFile(dockerDaemon); err == nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("%s is not valid JSON, not touching it: %v", dockerDaemon, err)
		}
	}
	cfg["registry-mirrors"] = []string{url}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.MkdirAll(filepath.Dir(dockerDaemon), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dockerDaemon, append(out, '\n'), 0o644); err != nil {
		return err
	}
	restartDocker()
	return nil
}

func restartDocker() {
	if _, err := run("systemctl", "restart", "docker"); err != nil {
		fmt.Println("  couldn't restart Docker automatically; run: sudo systemctl restart docker")
		return
	}
	fmt.Println("  Docker restarted")
}

func needRoot() error {
	if runtime.GOOS == "linux" && os.Geteuid() != 0 {
		return errors.New("needs root: run it again with sudo")
	}
	return nil
}

func aptAdapter() adapter {
	return adapter{
		kind: "apt",
		available: func() error {
			if runtime.GOOS != "linux" {
				return errors.New("Linux only")
			}
			return need("apt-get")()
		},
		current:  func() string { return "" },
		snapshot: func() (string, bool) { return "", false },
		set: func(url string) error {
			fmt.Printf("  To use it, replace the mirror address in /etc/apt/sources.list.d/*.sources\n  (or /etc/apt/sources.list) with:\n    %s\n  then run: sudo apt-get update\n  (automatic apt switching is coming in v0.2)\n", url)
			return errManual
		},
		restore: func(string, bool) error { return nil },
	}
}

type change struct {
	Prev string    `json:"prev"`
	Had  bool      `json:"had"`
	When time.Time `json:"when"`
}

func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "rahyab", "state.json")
}

func loadState() map[string]change {
	st := map[string]change{}
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func saveState(st map[string]change) error {
	p := statePath()
	if len(st) == 0 {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(p, b, 0o644)
}

func applyChange(tool string, ad adapter, url string) error {
	st := loadState()
	if _, seen := st[tool]; !seen {
		prev, had := ad.snapshot()
		st[tool] = change{Prev: prev, Had: had, When: time.Now()}
	}
	if err := ad.set(url); err != nil {
		if errors.Is(err, errManual) {
			return nil
		}
		return err
	}
	if err := saveState(st); err != nil {
		return fmt.Errorf("changed, but couldn't save undo info: %v", err)
	}
	fmt.Printf("  ✓ %s now uses %s\n", tool, url)
	return nil
}
