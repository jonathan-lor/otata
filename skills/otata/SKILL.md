---
name: otata
description: Publish the latest iOS or Android build to the user's phone over the air. Use when the user wants to see, test, or install the current build on their device ("get this on my phone", "publish the build", "let me try it"). Runs on the user's Mac or Linux machine, directly or over SSH.
---

# Publishing builds with otata

otata builds the project in the current directory, signs it, publishes it to a server on the user's computer, and prints a URL. The user opens the URL on their phone and taps Install. Your job ends at handing over the URL. The install happens on the phone.

## First-time setup

Tailscale is built in and selected by default. Run `otata autostart on`, then `otata transport login --json`, and give the user the `transport.auth_url` to enroll otata in their phone's tailnet. Wait for `transport.ready` in `otata status --json` before publishing. Don't install host Tailscale or configure Serve for this.

## Publishing

```sh
cd /path/to/MyApp                     # the project root
otata publish --platform ios --json   # or --platform android
```

- `--platform` is required (`ios` or `android`), since a Mac builds for both. A project that always builds one way records its flags in its agent instructions.
- `--json` works anywhere on any otata command. Success and failure both print one JSON envelope on stdout, and progress goes to stderr.
- A publish is synchronous and a build can take minutes. Give it a generous timeout and don't kill it for being slow.
- `--config Debug` builds much faster than the default `Release` when the user just wants to see a change. For Flutter on iOS, use `--config Profile` instead, since a Debug build can't launch from the home screen.
- Android builds need a JDK 17+ and the Android SDK (`ANDROID_HOME`, or `sdk.dir` in the project's `local.properties`), and anything missing comes back as `needs_setup` before Gradle runs. Pick the build with `--module` (`:app` by default) and `--flavor` when there are several. A release build with no signingConfig is refused, because Android won't install an unsigned APK, so use `--config Debug` unless the user has set up release signing.
- `otata publish --artifact path/to/App.ipa` publishes an `.ipa` or `.apk` built by any toolchain, with no `--platform` needed. An `.apk` needs the SDK's build-tools (`ANDROID_HOME`), and an unsigned one is refused with `signing_failed`.
- `otata status --json` reports the server, transport and base URL in one call, changing nothing. `otata list` shows what is published.

## Reading the result

Every command prints `{ok, command, data, error}`. On failure, `error` carries a stable `code`, a `message`, and often a `hint` and `details`. Branch on the code, never on message text. Exit 2 means the command was called wrongly, exit 1 means it ran and failed, and 128 plus a signal number means a signal stopped it.

| `error.code` | What to do |
| --- | --- |
| `no_project` | Nothing buildable here. Check the directory, or pass `--artifact` |
| `ambiguous_scheme` | Re-run with the flag in `details.flag` (`--scheme`, `--module` or `--flavor`), choosing from `details.candidates` |
| `needs_setup` | Run `details.command` in `details.dir`, then retry. With no command, the hint names the setting to fix |
| `build_failed` | Read the log at the path in `details` |
| `signing_failed` | On iOS, needs a human with Apple portal access, so don't retry. On Android the APK is unsigned or doesn't verify: a debug build is signed, and a release build needs a signingConfig |
| `free_profile` | iOS refuses free-team builds over the air, and a paid team is the only fix. Don't retry |
| `server_down` | `otata doctor --fix`. If autostart was never set up, `otata autostart on` once |
| `transport_down` | Read `otata status` and `otata doctor`. Enrollment uses `transport login`, approval and DNS/HTTPS settings need the tailnet administrator, and certificate preparation needs time |
| `no_transport` | Run the `otata transport use` command the hint names |
| `slug_conflict` | Another path owns this name. Pass `--slug`, or `otata forget <slug>` |
| `build_in_progress` | Another publish holds the slug, so wait. `doctor --fix` clears a marker whose process is gone |
| `not_found` | Check `otata list` |
| `unhealthy` | Read `data.checks`. Each failing check names its remedy |
| `interrupted` | A signal stopped the publish (your timeout, a dropped connection). The build was killed and nothing is half-done. Retry when ready |
| `invalid_args` | Fix the arguments |
| `internal` | Unclassified. Read the message |

## When something is wrong

```sh
otata doctor --fix --json
```

Doctor repairs what it can (the launch agent or systemd unit, stale build markers, pages), then verifies every URL and exits non-zero with each failing check naming its remedy. Logs on the machine:

- `~/.otata/server.log`: server and access log
- `~/.otata/build/<slug>/xcodebuild.log`: the build that failed, or `gradle.log` there for an Android build

## Not on the machine that builds?

otata runs on the machine that builds: a Mac for iOS, a Mac or Linux machine for Android. Drive it over SSH from anywhere:

```sh
ssh mac 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --platform ios --json'
ssh box 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --platform android --json'
```

- Spell out the binary's full path. A non-interactive SSH shell skips the files where PATH edits live, so `command not found` over SSH is a PATH problem, not a broken install.
- The envelope, codes and exit codes are unchanged.
- An SSH connection that dies mid-build kills the publish, which cleans up like a Ctrl-C. On a flaky link, run it under `tmux` or `nohup`.
- Keep the project at one path on that machine. The same project at a new location is refused with `slug_conflict`.
- An Android build over SSH needs the SDK in the command's environment: `ANDROID_HOME` in the shell the command gets, or `sdk.dir` in the project's `local.properties`, which needs no shell at all.

## Upgrading

Let running publishes finish and run `otata stop` before replacing the binary. After installing, run `otata autostart on` and give the user the new URLs from `otata status`. Published builds and an existing enrollment are kept.

Coming from before 0.4.0, first remove otata's old route from the host's Tailscale Serve, or it keeps exposing the server through the host's URL. Find the handler in `tailscale serve status --json` that points at otata's loopback port and turn off only that one, with its own HTTPS port and path. Never run `tailscale serve reset` or touch other routes, and leave host Tailscale installed. Afterwards, enroll the new embedded node with `otata transport login`.

## Rules

- Never background the server as a workaround (no `otata serve &`, no `nohup` spawn). It runs under launchd or systemd through `otata autostart on`, and `server_down` means `doctor --fix`, not a hand-rolled server. The one exception is a headless Mac with no console session, where `otata serve` in a tmux session the user knows about is the supported mode. On Linux, a server that vanishes at logout needs `sudo loginctl enable-linger $USER`, which the user runs once.
- Never write into `~/.otata`. Every change goes through the CLI.
- The printed URL is for the user's phone. Hand it over instead of fetching it yourself. When in doubt, `otata doctor` verifies every URL.
- On iOS, after the user's signing identity changes, the first publish must happen at the Mac so the user can answer the keychain prompt with **Always Allow**. A remote publish under a new identity hangs mid-build instead. Android signing is a keystore file and never prompts.
