# otata

Quickly install iOS and Android builds over your own network, _wherever you are_!

![otata banner](assets/banner.png)

[![ci](https://github.com/jonathan-lor/otata/actions/workflows/ci.yml/badge.svg)](https://github.com/jonathan-lor/otata/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/jonathan-lor/otata)](https://github.com/jonathan-lor/otata/releases/latest)
[![license](https://img.shields.io/github/license/jonathan-lor/otata)](LICENSE)

Tired of just getting screenshots? otata is a CLI for agents to get the latest build from your computer to your phone during remote sessions, allowing you to truly work on mobile apps from anywhere. Just ask your agent to publish with otata after making some changes, and then install with the provided URL!

otata currently supports building and installing SwiftUI, React Native, Flutter and Kotlin Multiplatform apps on iOS, and native apps on Android.
Windows is not yet supported.

Tailscale is the recommended method for serving, but serving through your own HTTPS proxy is also supported.

```sh
cd ~/path/to/MyApp
otata publish --platform ios       # builds, signs, publishes, prints the URL
# or
otata publish --platform android   
```

## Requirements

Whichever platform you build for, your phone needs **a way to reach the computer**: [Tailscale](https://tailscale.com) is the recommended solution for this (since iOS requires HTTPS with a publicly trusted certificate) and is free for personal use, and bringing [your own HTTPS proxy](docs/manual-transports.md) works too.

**Building for iOS has the following requirements:**

- **A Mac with Xcode.** Apple doesn't allow iOS builds anywhere else (without breaking TOS). If you use a different host machine and only use the Mac to build, otata can be used [over SSH](docs/otata-via-ssh.md).
- **A paid Apple Developer account** ($99/year), with your iOS device registered to
  the team. **iOS outright refuses to install a free personal team's build over
  the air,** so `otata publish` will follow suit and refuse as well.
- **Developer Mode on the phone**: Settings -> Privacy & Security -> Developer Mode.

(The assumption is that if you're committed enough to need otata for remote work with agents, you probably plan to actually ship to the App Store, in which case you'd own or be a part of a paid Apple developer team anyways.)

**Building for Android has the following requirements:**

- **A JDK 17 or newer and the Android SDK**, on a Mac or a Linux machine, with `ANDROID_HOME` set or `sdk.dir` in the project's `local.properties`. The project's Gradle wrapper does the building, and the SDK's build-tools read the APK afterwards.
- **A signed build.** A debug build is signed by the debug keystore every machine has and a release build needs a `signingConfig` in the module, since Android won't install an unsigned APK.
- **Allowing installs from your browser.**

## Install

**You can just give your agent the link to this repo to run the install and setup if you'd like. You'll still have to do the Tailscale steps yourself though.** 

This install will assume that you've chosen to use Tailscale. You should also reference the more detailed step-by-step guide in [Getting started](docs/getting-started.md). 

First, install Tailscale on your computer ([Mac](https://tailscale.com/docs/install/mac), [Linux](https://tailscale.com/docs/install/linux)) and phone ([iPhone](https://tailscale.com/docs/install/ios), [Android](https://tailscale.com/docs/install/android)). Sign into the same account on both, and turn on HTTPS certificates and MagicDNS in the [admin console](https://login.tailscale.com/admin/dns) under DNS. 

Then, install otata:

```sh
# Mac
brew install --cask jonathan-lor/tap/otata
```

```sh
# Linux
curl -fsSL https://raw.githubusercontent.com/jonathan-lor/otata/main/install.sh | sh
```

Or just install with `go`:

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
otata publish --platform ios|android   # build and publish the project in the current directory
otata list                             # what is published
otata doctor                           # verify the server, transport and every URL; --fix repairs first
```

`--platform` is the only required flag. `otata publish` will discover the rest from the project and the directory name.

`--config` defaults to `Release`, and a publish that falls back to it will tell you before the build starts.
Publishing an already-built `.ipa` or `.apk` from any toolchain is `otata publish --artifact <path>`.

A more detailed reference can be found in the [CLI reference](docs/cli-reference.md).

## Built For Agents

The primary caller is almost certainly an agent publishing the latest build for your review after making some changes.
Thus, `--json` is accepted anywhere on the command line. Your agent also doesn't have to be running on the Mac for iOS builds. See [docs/otata-via-ssh.md](docs/otata-via-ssh.md).

```sh
$ otata status --json | jq .data.transport.base_url
"https://your-mac.your-tailnet.ts.net/otata"
```

otata errors carry stable machine codes to make it easier for agents to branch off them.
The exit code is 2 when the command was called wrongly, 1 when it ran and failed, and 128 plus the signal number when a signal stopped it.
The [CLI reference](docs/cli-reference.md#error-codes) also lists every code and what to do about it.

## Current Limitations

**macOS and Linux only.** Windows is not yet supported, and WSL is untested.

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
