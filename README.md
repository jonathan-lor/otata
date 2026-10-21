# otata

Quickly install iOS and Android builds over your own network, _wherever you are_!

![otata banner](assets/banner.png)

[![ci](https://github.com/jonathan-lor/otata/actions/workflows/ci.yml/badge.svg)](https://github.com/jonathan-lor/otata/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/jonathan-lor/otata)](https://github.com/jonathan-lor/otata/releases/latest)
[![license](https://img.shields.io/github/license/jonathan-lor/otata)](LICENSE)

Tired of just getting screenshots? otata is a CLI for agents to get the latest build from your computer to your phone during remote sessions, so you can truly work on mobile apps from anywhere. Ask your agent to publish with otata after making some changes, and then install from the provided URL!

otata supports SwiftUI, React Native, Flutter and Kotlin Multiplatform apps on iOS, and Gradle-built apps on Android, native or cross-platform.

```sh
cd ~/path/to/MyApp
otata publish --platform ios       # builds, signs, publishes, prints the URL
# or
otata publish --platform android
```

## Requirements

Your phone needs a way to reach your computer over HTTPS with a *publicly trusted certificate*. [Tailscale](https://tailscale.com) is the recommended solution because it provides private access by default and is free for personal use. For advanced users or those who can't use Tailscale for any reason, you can use otata with your own proxy and network.

**iOS**

- A Mac with Xcode. It can be a build machine you drive [over SSH](docs/otata-via-ssh.md).
- A paid Apple Developer account ($99/year) with your phone registered to the team. iOS refuses to install a free personal team's build over the air, so `otata publish` refuses it too.
- Developer Mode on the phone: Settings -> Privacy & Security -> Developer Mode.

**Android**

- A Mac or Linux machine with JDK 17+ and the Android SDK (`ANDROID_HOME`, or `sdk.dir` in the project's `local.properties`).
- A signed build. Debug builds are signed out of the box; release builds need a `signingConfig`.
- A phone that allows installs from your browser.

## Install

You can give your agent the link to this repo to install and set up otata, but you'll still need to do the Tailscale steps. These steps assume Tailscale and [getting started](docs/getting-started.md) has the full walkthrough which you should also read.

Install Tailscale on your phone ([iOS](https://tailscale.com/docs/install/ios), [Android](https://tailscale.com/docs/install/android)) and sign in. In the [admin console](https://login.tailscale.com/admin/dns) under DNS, turn on MagicDNS and HTTPS certificates.

Then install otata:

```sh
# Mac
brew install --cask jonathan-lor/tap/otata
```

```sh
# Linux
curl -fsSL https://raw.githubusercontent.com/jonathan-lor/otata/main/install.sh | sh
```

Or with Go: `go install github.com/jonathan-lor/otata@latest`.

Then:

```sh
otata autostart on
otata transport login
```

Open the printed URL to enroll otata in your phone's tailnet, then wait for `otata status` to report ready. The first certificate can take a minute.

**Upgrading from before 0.5.0?** The Tailscale URL changed; read [upgrading](docs/tailscale.md#upgrading) first.

## Usage

```sh
otata publish --platform ios|android   # build and publish the project in this directory
otata publish --artifact App.ipa       # publish an .ipa or .apk from any toolchain
otata list                             # what is published
otata doctor                           # verify the server, transport and every URL; --fix repairs first
```

`--platform` is the only required flag. otata discovers the rest from the project. See the [CLI reference](docs/cli-reference.md) for everything else.

## Built For Agents

Every command accepts `--json`, and failures carry [stable error codes](docs/cli-reference.md#error-codes) to branch on. The agent doesn't have to run on the build machine ([otata via SSH](docs/otata-via-ssh.md)), and otata includes an [agent skill](skills/otata/SKILL.md).

## Current Limitations

**macOS and Linux only.** Windows is not supported yet, and WSL is untested.

**Access through a custom proxy is your responsibility!** otata hosts builds on your machine, with private access through Tailscale by default. With a custom endpoint, you control who can retrieve builds. otata does not verify or enforce any access policy.

## Documentation

- [Getting started](docs/getting-started.md)
- [CLI reference](docs/cli-reference.md)
- [FAQ](docs/FAQ.md)
- [Tailscale](docs/tailscale.md)
- [otata via SSH](docs/otata-via-ssh.md)
- [Contributing](CONTRIBUTING.md)
