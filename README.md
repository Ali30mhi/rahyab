# rahyab 🧭

**One command, and pip, npm, go and docker work again in Iran.**

rahyab tests every known mirror *for real* (not just ping), checks that the
mirror is up to date, picks the best one and configures your tools.
Everything is reversible with `rahyab undo`.

- Works on **Windows, macOS and Linux**: a single binary, no Python needed
- Measures real requests, not ping
- Detects **outdated** mirrors
- If the official source works best (e.g. on a VPN), it keeps it
- `undo` restores your original settings

## Usage
```
rahyab doctor          # what's blocked? (changes nothing)
rahyab fix             # fix all installed tools
rahyab fix pip npm     # only these
sudo rahyab fix docker # docker on Linux needs root
rahyab status
rahyab undo
```

## Install
Download the file for your OS from [Releases](../../releases).

## Contributing
Know a mirror? Edit `mirrors.json` and open a PR. No Go required.
Mirror data is based on the community-maintained [Mirava](https://github.com/MiravaOrg/Mirava) project. Thank you!

## License
MIT
