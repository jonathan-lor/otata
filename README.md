# otata

[![ci](https://github.com/jonathan-lor/otata/actions/workflows/ci.yml/badge.svg)](https://github.com/jonathan-lor/otata/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/jonathan-lor/otata)](https://github.com/jonathan-lor/otata/releases/latest)
[![license](https://img.shields.io/github/license/jonathan-lor/otata)](LICENSE)

otata is a tool for quickly installing iOS builds over your own network. It's a CLI designed for a coding agent to use during remote control sessions to get the latest build from your computer to your phone, wherever you are.

Simply ask your favorite agent to publish with otata after making some changes, and then install from the provided URL!

otata currently supports building SwiftUI, React Native, Flutter, and Kotlin Multiplatform projects on macOS and installing on iOS.
Android and Linux/Windows/WSL support is a work in progress.

Tailscale is the recommended method for serving, but serving through your own HTTPS proxy is also supported.

```sh
cd ~/path/to/MyApp
otata publish                # builds, signs, publishes, prints the URL
```

## Requirements

Building for iOS has the following requirements:

- **A Mac with Xcode.** Apple doesn't allow iOS builds anywhere else (without breaking TOS). If you use a different host machine and only use the Mac to build, otata can be used [over SSH](docs/otata-via-ssh.md).
- **A paid Apple Developer account** ($99/year), with your iOS device registered to
  the team. **iOS outright refuses to install a free personal team's build over
  the air,** so `otata publish` will follow suit and refuse as well.
- **Developer Mode on the phone**: Settings -> Privacy & Security -> Developer Mode.
- **A way for your phone to reach the Mac.** iOS will only install from a URL
  served over HTTPS with a publicly trusted certificate. [Tailscale](https://tailscale.com)
  is the recommended solution and is free for personal use.
  Bringing [your own HTTPS proxy](docs/manual-transports.md) works too.

The assumption is that if you're committed enough to need otata for remote work with agents, you probably plan to actually ship to the App Store, in which case you'd own or be a part of a paid Apple developer team anyways. ;)

## Install

**You can just give your agent the link to this repo to run the install and setup if you'd like. You'll still have to do the Tailscale steps yourself though.** 

This install will assume that you've chosen to use Tailscale. You should also reference the more detailed step-by-step guide in [Getting started](docs/getting-started.md). 

Install Tailscale on both [your Mac](https://tailscale.com/docs/install/mac) and [your phone](https://tailscale.com/docs/install/ios), sign into the same account on both,
and turn on HTTPS certificates and MagicDNS in the [admin console](https://login.tailscale.com/admin/dns) under DNS. 

Then, install otata on the Mac:

```sh
brew install --cask jonathan-lor/tap/otata
```

Or

```sh
go install github.com/jonathan-lor/otata@latest
```

Then, once:

```sh
otata transport use tailscale
otata autostart on
```

otata also includes an [agent skill](skills/otata/SKILL.md).

## Usage 

Three commands cover nearly everything:

```sh
otata publish     # build and publish the project in the current directory
otata list        # what is published
otata doctor      # verify the server, transport and every URL; --fix repairs first
```

`otata publish` discovers the workspace or project, an archiving scheme, the
signing team, and the slug from the directory name. `--scheme` and `--slug` are for when it asks.
`--config` defaults to `Release`, and a publish that falls back to it will tell you before the build starts.
Publishing an already-built `.ipa` from any toolchain is `otata publish --artifact <path>`.

A more detailed reference can be found in the [CLI reference](docs/cli-reference.md).

## Built For Agents

The primary caller is almost certainly an agent publishing the latest build for your review after making some changes.
Thus, `--json` is accepted anywhere on the command line. Your agent also does not have to be running on the Mac. See [docs/otata-via-ssh.md](docs/otata-via-ssh.md).

```sh
$ otata status --json | jq .data.transport.base_url
"https://your-mac.your-tailnet.ts.net/otata"
```

otata errors carry stable machine codes to make it easier for agents to branch off them.
The exit code is 2 when the command was called wrongly, 1 when it ran and failed, and 128 plus the signal number when a signal stopped it.
The [CLI reference](docs/cli-reference.md#error-codes) also lists every code and what to do about it.

## Current Limitations

**iOS and macOS only (for now).** Android builds and support for Linux/Windows/WSL are planned but not yet implemented.
If you're only using a Mac to build, otata can be used from a non-Mac host via SSH.

**Private transports only (for now).** A public transport would need an access guard
before it's safe, and none is implemented yet.

## Documentation

- [Getting started](docs/getting-started.md)
- [CLI reference](docs/cli-reference.md)
- [FAQ](docs/FAQ.md)
- [Serving over your own proxy](docs/manual-transports.md)
- [otata via SSH](docs/otata-via-ssh.md)
- [Gotchas and troubleshooting](docs/gotchas.md)
- [Contributing](CONTRIBUTING.md)
