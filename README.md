# rahyab 🧭

[فارسی](README.fa.md)

**One command, and pip, npm, go, docker and apt work again in Iran.**

rahyab tests every known mirror *for real*, checks that each one is up to date, picks the best one and configures your tools. Everything is reversible with `rahyab undo`.

## Example

[![rahyab fix --no-proxy](docs/demo.svg)](docs/demo.svg)

Here IranServer is the **fastest** apt mirror, but it's **11 days behind**. A tool that only measures speed would pick it. rahyab skips it and picks ArvanCloud, updated 2 hours ago.

## Why rahyab

- **Real requests, not ping.** It asks each mirror for exactly what your package manager would, and checks the answer. For Docker it does the real pull handshake (token, then manifest). For Go it also checks the checksum database, without which `go get` fails.
- **Freshness, not just speed.** Compares package versions and apt release dates. Mirrors more than 2 days behind are skipped, and between two close mirrors the fresher one wins.
- **Fallbacks.** Docker gets up to 3 mirrors and Go gets 2, so one mirror going down doesn't break your build.
- **No change for the sake of change.** If the official source is the best (e.g. on a VPN), it keeps it.
- **Safe.** Undo info is saved before anything is touched, files are written atomically, and if Docker won't start with the new config it's rolled back. Security repos and third-party apt repos (Docker's, PPAs) are never touched.
- **Single binary** for Linux, macOS and Windows. No Python.

## Install

**Linux and macOS**

```
curl -fsSL https://raw.githubusercontent.com/Ali30mhi/rahyab/main/install.sh | sh
```

It picks the right build for your system and verifies its checksum.

**Windows:** download `rahyab-windows-amd64.exe` from [Releases](https://github.com/Ali30mhi/rahyab/releases) and run it in PowerShell.

## Usage

```
rahyab doctor            # what's blocked? (changes nothing)
rahyab fix               # pip, npm, go (your user settings)
sudo rahyab fix          # docker, apt (system settings)
rahyab fix pip npm       # only these
rahyab fix --dry-run     # show what would change
rahyab status            # current settings
rahyab undo              # restore (sudo rahyab undo for docker/apt)
rahyab undo npm          # restore just one tool
```

**On a VPN?** By default rahyab tests the same route your tools use (including `HTTP(S)_PROXY`), so the result works for you right now. Add `--no-proxy` to test the direct Iranian network instead.

**Dockerfiles and scripts:** `--print` shows the commands instead of running them. Run it on your machine and put the output in a `RUN` line:

```
rahyab fix pip apt --print --no-proxy
```

**Settings not taking effect?** `PIP_INDEX_URL`, `NPM_CONFIG_REGISTRY` and `GOPROXY` in your shell override config files. rahyab tells you when one is set.

## Supported

| Tool   | Automatic | Notes                                                   |
|--------|-----------|---------------------------------------------------------|
| pip    | ✅        | user config, no root                                    |
| npm    | ✅        | user config, no root                                    |
| go     | ✅        | `GOPROXY`, with a fallback mirror                       |
| docker | ✅ Linux  | up to 3 mirrors; Docker Desktop: shows the setting to paste |
| apt    | ✅ Linux  | Ubuntu/Debian main archive, needs sudo                  |

## Contributing

Know a mirror? Add it to `mirrors.json` and open a PR. No Go knowledge needed. Test it first with `rahyab fix --dry-run --mirrors=mirrors.json`. rahyab fetches the latest `mirrors.json` from this repo on every run, so once it's merged everyone gets your mirror without updating.

Mirror data is based on the community-maintained [Mirava](https://github.com/MiravaOrg/Mirava) project. Thank you!

## License

MIT
