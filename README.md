# rahyab 🧭

[فارسی](README.fa.md)

**One command, and pip, npm, go and docker work again in Iran.**

rahyab tests every known mirror *for real*, checks that each one is up to date,
picks the best one and configures your tools. Everything is reversible with `rahyab undo`.

## Example

```
$ rahyab fix --no-proxy

== pip ==
  ✓  Runflare             315ms   ok       26.2.2
  ✓  Liara                497ms   ok       26.2.2
  ✗  Official (pypi.org)  10.01s  timeout
  → best: Runflare  https://mirror-pypi.runflare.com/simple/

== apt ==
  ✓  Shatel                     213ms    ok       updated 13h ago
  ✓  ArvanCloud                 227ms    ok       updated 1h ago
  !  IranServer                 291ms    ok       updated 11d ago (outdated)
  ✗  Official (deb.debian.org)  10.008s  timeout
```

`!` means the mirror answers fast but is **11 days behind**. Latency alone can't tell you that.

## Why rahyab

- **Real requests, not ping.** It asks each mirror for exactly what your package manager would, and checks the answer is valid.
- **Freshness check.** Compares package versions and apt release dates, and flags mirrors that are behind.
- **No change for the sake of change.** If the official source is the best (e.g. on a VPN), it keeps it.
- **Single binary** for Linux, macOS and Windows. No Python. No root, except for docker on Linux.
- **`undo`** restores your original settings exactly.

## Install

**Linux**
```
curl -L -o rahyab https://github.com/Ali30mhi/rahyab/releases/latest/download/rahyab-linux-amd64
chmod +x rahyab && sudo mv rahyab /usr/local/bin/
```

**macOS:** download `rahyab-darwin-arm64` (Apple Silicon) or `rahyab-darwin-amd64` (Intel) from
[Releases](../../releases), then `chmod +x` it and run `xattr -d com.apple.quarantine rahyab`.

**Windows:** download `rahyab-windows-amd64.exe` from [Releases](../../releases) and run it in PowerShell.

## Usage

```
rahyab doctor            # what's blocked? (changes nothing)
rahyab fix               # fix all installed tools
rahyab fix pip npm       # only these
rahyab fix --dry-run     # show what would change
sudo rahyab fix docker   # docker on Linux needs root
rahyab status            # current settings
rahyab undo              # restore everything
```

**On a VPN?** By default rahyab tests the same route your tools use (including `HTTP(S)_PROXY`),
so the result works for you right now. Add `--no-proxy` to test the direct Iranian network instead.

## Supported

| Tool   | Automatic | Notes |
|--------|-----------|-------|
| pip    | ✅ | user config, no root |
| npm    | ✅ | user config, no root |
| go     | ✅ | `GOPROXY` |
| docker | ✅ Linux | Docker Desktop: shows the setting to paste |
| apt    | suggests | automatic switching planned |

## Contributing

Know a mirror? Add it to `mirrors.json` and open a PR. No Go knowledge needed.
Test it first with `rahyab fix --dry-run --mirrors=mirrors.json`.

Mirror data is based on the community-maintained [Mirava](https://github.com/MiravaOrg/Mirava) project. Thank you!

## License

MIT
