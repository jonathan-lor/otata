# otata

[![ci](https://github.com/jonathan-lor/otata/actions/workflows/ci.yml/badge.svg)](https://github.com/jonathan-lor/otata/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/jonathan-lor/otata)](https://github.com/jonathan-lor/otata/releases/latest)
[![license](https://img.shields.io/github/license/jonathan-lor/otata)](LICENSE)

otata is a tool for quickly installing iOS builds over your own network. It's a CLI designed for your agent to use during remote control sessions to get the latest build to your phone, wherever you are. Simply ask your agent to publish with otata after making some changes, and then install from the provided URL!

otata currently supports building SwiftUI, React Native, Flutter, and Kotlin Multiplatform projects on macOS and installing on iOS.
Android build support on Linux/Windows/WSL and installation on Android devices is not yet supported, but is a work in progress.

Tailscale is the recommended method for serving, but serving through your own HTTPS proxy is also supported.

```sh
cd ~/path/to/MyApp
otata publish                # builds, signs, publishes, prints the URL
```

A shared server publishes every app you build, so every app you publish will be listed at that URL.

## Install

```sh
brew install --cask jonathan-lor/tap/otata
```

Or `go install github.com/jonathan-lor/otata@latest`, or clone and `make
install`.

Then, once:

```sh
otata transport use tailscale   # how your phone reaches this machine
otata autostart on              # optional; the server runs under launchd from then on
```

otata requires `tailscale serve` and Tailscale to have HTTPS certificates enabled to work with it out of the box. See [Transports](#transports) for more details.
For some examples of serving through your own HTTPS proxy, see [docs/manual-transports.md](docs/manual-transports.md) 

otata also requires macOS with Xcode and a **paid** Apple developer team with the target device registered.
Trying to install a free personal team's builds OTA is outright refused by iOS, so `otata publish` will follow suit and error as well if you try this.
This is Apple's unavoidable restriction on the free developer profile and is completely out of otata's hands.

The assumption is that if you're committed enough to need otata for remote work with agents, you probably plan to actually ship to the App Store (in which case you'd own or be a part of a paid Apple developer team anyways).

## Commands

| Command | Does |
| --- | --- |
| `otata publish [--config Debug] [--scheme S] [--slug NAME] [--builder archive]` | Build and publish the project in the current directory |
| `otata publish --artifact <path>` | Publish an already-built `.ipa` from any toolchain |
| `otata list` | What is published |
| `otata status` | Everything in one call |
| `otata doctor [--fix]` | Verify the server, transport and every URL, report a signing deadline; `--fix` repairs first |
| `otata forget <slug>` | Drop one app and its payload |
| `otata serve` | Run the file server in the foreground |
| `otata start` / `stop` / `restart` | Server lifecycle |
| `otata autostart on\|off` | Run the server under launchd: at login, restarted if it exits |
| `otata transport use <name>` | `tailscale` or `manual` |

`otata publish` discovers the workspace or project, a scheme that archives an app, the
signing team, and the slug from the directory name. `--scheme` and `--slug` are for when it asks.
`--config` defaults to `Release`, and a publish that falls back to it will tell you before the build starts.

Publishes will build incrementally by default. `--builder archive` will use `xcodebuild archive` + export instead, which rebuilds
everything every time and will be noticeably slower.

Environment: `OTATA_ROOT` (default `~/.otata`), `OTATA_PORT`, `OTATA_PATH`, and
`NO_COLOR`. `OTATA_PORT` and `OTATA_PATH` override the stored config for one
invocation without persisting.

## Built For Agents

The primary caller is almost certainly an agent publishing the latest build for your review after making some changes.
Thus, `--json` is accepted anywhere on the command line. Your agent also does not have to be running on the Mac. See [docs/otata-via-ssh.md](docs/otata-via-ssh.md).

```sh
$ otata status --json | jq .data.transport.base_url
"https://your-mac.your-tailnet.ts.net/otata"
```

otata errors carry stable machine codes so agents can branch on them.
The exit code is 2 when the command was called wrongly and 1 when it ran and failed:

| Code | Means | What to do |
| --- | --- | --- |
| `no_project` | Nothing buildable here | Check the directory, or pass `--artifact` |
| `ambiguous_scheme` | Several candidates | Re-run with `--scheme`; the candidates are in `details` |
| `needs_setup` | A step the project's own toolchain owns has not been run | Run `details.command` in `details.dir`, then retry |
| `build_failed` | The toolchain returned non-zero | Read the log path in `details` |
| `signing_failed` | Certificate, profile or device registration | Needs a human with Apple portal access |
| `free_profile` | Signed by a free personal team, which iOS will not install over the air | Sign with a paid team; nothing else fixes it |
| `server_down` | Local server not running, or the port is held by something else | `otata autostart on` once; after that, `otata doctor --fix` and retry |
| `transport_down` | Transport present but unusable | Needs the machine, e.g. Tailscale logged out |
| `no_transport` | No transport selected, or `manual` has no base URL | Run the `otata transport use` command the hint names |
| `slug_conflict` | Another project owns this name, or its record is unreadable | Pass `--slug`, or `otata forget` it |
| `build_in_progress` | A live publish already holds this slug | Wait; `otata doctor --fix` clears a marker whose process is gone |
| `not_found` | No app published under that slug | Check `otata list` |
| `unhealthy` | `doctor` found something wrong | Read `data.checks`; each failing one says what to do, often `--fix` |
| `invalid_args` | The command was called wrongly | Fix the arguments |
| `internal` | Anything unclassified | Read the message; it is not expected |

otata also includes an [agent skill](skills/otata/SKILL.md).

## Transports

iOS requires a URL the phone can reach over HTTPS with a **publicly trusted
certificate**.

| Transport | Reachable by | Visibility | Setup |
| --- | --- | --- | --- |
| `tailscale` | Devices on your tailnet | Private | `otata transport use tailscale`, once |
| `manual` | Whatever your proxy serves | You declare it | `--base-url`, optionally `--keep-prefix` |

```sh
otata transport use manual --base-url https://builds.example.com/otata
```

The transport is selected once and validated then. `tailscale`
refuses unless the CLI answers and the tailnet has HTTPS certificates enabled
(admin console -> DNS. A new tailnet has them off, and `tailscale serve` needs them).
Funnel makes a listener public per listener, not per path, so publishing is
also refused while anything on `:443` is funnelled.

Verified walkthroughs for a Cloudflare quick tunnel, Caddy on your own domain, and ngrok are in [docs/manual-transports.md](docs/manual-transports.md).

## Current Limitations

**iOS and macOS only (for now).** Android builds and support for Linux/Windows/WSL are planned but not yet implemented.
If you're only using a Mac to build, otata can be used from a non-Mac host via SSH.

**Private transports only (for now).** A public transport would need an access guard
before it is safe, and none is implemented yet.

## Documentation

- [FAQ](docs/FAQ.md)
- [Serving over your own proxy](docs/manual-transports.md)
- [otata via SSH](docs/otata-via-ssh.md)
- [Gotchas and troubleshooting](docs/gotchas.md)
- [Contributing](CONTRIBUTING.md)
