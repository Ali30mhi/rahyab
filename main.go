package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

var version = "dev"

// One shared reader, so piped input (e.g. `yes | rahyab fix`) isn't lost between questions.
var stdin = bufio.NewReader(os.Stdin)

type options struct {
	yes     bool
	dryRun  bool
	print   bool
	force   bool
	offline bool
	help    bool
	timeout time.Duration
	mirrors string
	tools   []string
}

const usage = `rahyab - fix package downloads in Iran with one command

Usage:
  rahyab doctor            check what is blocked (changes nothing)
  rahyab fix [tool...]     test mirrors and switch to the best one
                           tools: pip npm go docker apt (default: all installed)
  rahyab status            show current settings
  rahyab undo [tool...]    restore what rahyab changed
  rahyab version

Flags:
  -y, --yes          don't ask for confirmation
  --dry-run          show what would change, change nothing
  --print            print the commands instead of running them (scripts, Dockerfiles)
  --force            undo: restore even if you changed the setting yourself since
  --no-proxy         ignore the proxy/VPN and test the direct connection
  --offline          don't fetch the latest mirror list, use the built-in or cached one
  --timeout=10s      timeout per request (--timeout 10s works too)
  --mirrors=FILE     use a custom mirrors.json
`

func parseArgs(args []string) (options, error) {
	o := options{timeout: 10 * time.Second}
	for i := 0; i < len(args); i++ {
		a := args[i]
		// "--timeout 5s" means the same as "--timeout=5s".
		if a == "--timeout" || a == "--mirrors" {
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s needs a value", a)
			}
			i++
			a += "=" + args[i]
		}
		switch {
		case a == "-h" || a == "--help":
			o.help = true
		case a == "-y" || a == "--yes":
			o.yes = true
		case a == "--no-proxy":
			noProxy = true
		case a == "--dry-run":
			o.dryRun = true
		case a == "--print":
			o.print = true
		case a == "--force":
			o.force = true
		case a == "--offline":
			o.offline = true
		case strings.HasPrefix(a, "--timeout="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--timeout="))
			if err != nil {
				return o, fmt.Errorf("bad --timeout: %v", err)
			}
			if d <= 0 {
				return o, fmt.Errorf("--timeout must be greater than zero")
			}
			o.timeout = d
		case strings.HasPrefix(a, "--mirrors="):
			o.mirrors = strings.TrimPrefix(a, "--mirrors=")
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %s", a)
		default:
			o.tools = append(o.tools, strings.ToLower(a))
		}
	}
	return o, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	o, err := parseArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if o.help {
		fmt.Print(usage)
		return
	}
	switch os.Args[1] {
	case "doctor":
		err = runDoctor(o)
	case "fix":
		err = runFix(o)
	case "status":
		err = runStatus()
	case "undo":
		err = runUndo(o)
	case "version", "--version", "-v":
		fmt.Println("rahyab", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func confirm(o options, question string) bool {
	if o.yes {
		return true
	}
	fmt.Printf("%s [y/N] ", question)
	line, _ := stdin.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
