---
name: otata
description: Publish the latest iOS or Android build to the user's phone over the air. Use when the user wants to see, test, or install the current build on their device ("get this on my phone", "publish the build", "let me try it"). Runs on the user's Mac or Linux machine, directly or over SSH.
---

# Publishing builds with otata

otata builds the project in the current directory, signs it, publishes it to a
server on the user's computer, and prints a URL. The user opens the URL on
their phone and taps Install. Your job ends at handing over the URL; the
install happens on the phone.

## The one command

```sh
cd /path/to/MyApp        # the project root
otata publish --platform ios --json       # or --platform android
```

- `--platform` is required: `ios` or `android`. Nothing is discovered, because
  a Mac builds for both. A project that always builds for one platform records
  the flag in its agent instructions.
- `--json` is accepted anywhere on any otata command. Success and failure both
  print one JSON envelope on stdout; progress goes to stderr.
- A publish is synchronous and a build can take minutes. Give it a generous
  timeout and do not kill it for being slow.
- `--config Debug` builds much faster than the default Release when the user
  just wants to see a change — except on Flutter, where a Debug build cannot
  launch from the home screen: use `--config Profile` there.
- An Android publish runs the project's Gradle wrapper and needs a JDK 17 or
  newer and the Android SDK (`ANDROID_HOME`, or `sdk.dir` in the project's
  `local.properties`); whatever is missing comes back as `needs_setup` before
  Gradle runs. The build is selected with `--module` (the Gradle module,
  `:app` by default) and `--flavor` (the product flavor, when the module has
  several); `--scheme` is iOS's and is refused here. The default `Release` is
  refused with `needs_setup` on a module whose release build type has no
  signingConfig, because Android will not install an unsigned APK: use
  `--config Debug`, which the debug keystore signs, unless the user has set
  up release signing.
- `otata publish --artifact path/to/App.ipa` publishes an already-built .ipa
  or .apk from any toolchain. The file says which platform it is, so
  `--platform` is not needed there. An .apk needs the Android SDK's
  build-tools on the machine (`ANDROID_HOME`), and one that is unsigned is
  refused with `signing_failed`: build it with `--config Debug`, or add a
  release signingConfig.
- `otata status --json` reports the server, transport, and base URL in one
  call, changing nothing. `otata list` shows what is published.

## Reading the result

Every command prints `{ok, command, data, error}`. On failure, `error` carries
a stable `code`, a `message`, and often a `hint` and `details`. Branch on the
code, never on message text. Exit 2 means the command was called wrongly;
exit 1 means it ran and failed; 128 plus a signal number means a signal
stopped it.

| `error.code` | What to do |
| --- | --- |
| `no_project` | Nothing buildable here: check the directory, or pass `--artifact` |
| `ambiguous_scheme` | Re-run with the flag in `details.flag` (`--scheme`, `--module` or `--flavor`); the candidates are in `details.candidates` |
| `needs_setup` | Run `details.command` in `details.dir`, then retry; with no command, the hint names the setting to fix |
| `build_failed` | Read the log at the path in `details` |
| `signing_failed` | On iOS, needs a human with Apple portal access; do not retry. On Android the APK is unsigned or does not verify: a debug build is signed, a release build needs a signingConfig |
| `free_profile` | iOS refuses free-team builds over the air; a paid team is the only fix. Do not retry |
| `server_down` | `otata doctor --fix`; if autostart was never set up, `otata autostart on` once |
| `transport_down` | Machine-side (Tailscale logged out, the route is public with Funnel on, or on Linux the user is not Tailscale's operator: `sudo tailscale set --operator=$USER`); `otata doctor` names it. Do not retry until it is fixed |
| `no_transport` | Run the `otata transport use` command the hint names |
| `slug_conflict` | Another path owns this name: pass `--slug`, or `otata forget <slug>` |
| `build_in_progress` | Another publish holds the slug: wait; `doctor --fix` clears a marker whose process is gone |
| `not_found` | Check `otata list` |
| `unhealthy` | Read `data.checks`; each failing check names its remedy |
| `interrupted` | A signal stopped the publish (your timeout, a dropped connection); the build was killed and nothing is half-done. Retry when ready |
| `invalid_args` | Fix the arguments |
| `internal` | Unclassified; read the message |

## When something is wrong

```sh
otata doctor --fix --json
```

Doctor repairs what it can (the launch agent or systemd unit, transport
wiring, stale build markers, pages), then verifies every URL and exits
non-zero with each failing check naming its remedy. Logs on the machine:

- `~/.otata/server.log` — server and access log
- `~/.otata/build/<slug>/xcodebuild.log` — the build that failed;
  `gradle.log` there for an Android build

## Not on the machine that builds?

otata runs on the machine that builds: a Mac for iOS, a Mac or a Linux
machine for Android. Drive it over SSH from anywhere:

```sh
ssh mac 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --platform ios --json'
ssh box 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --platform android --json'
```

- Spell the binary's full path: a non-interactive shell over SSH skips the
  files where PATH edits live (zsh reads only `~/.zshenv`; a Linux `~/.bashrc`
  usually returns early), so `command not found` over SSH while otata works
  in a terminal there is a PATH problem, not a broken install.
- The envelope, codes, and exit codes are unchanged.
- An SSH connection that dies mid-build kills the publish (it cleans up like
  a Ctrl-C). On a flaky link, run it under `tmux` or `nohup`.
- Keep the project at one path on that machine: the same project at a new
  location is refused with `slug_conflict`.
- An Android build over SSH needs the SDK in the command's environment:
  `ANDROID_HOME` set in the shell the command gets, or `sdk.dir` in the
  project's `local.properties`, which needs no shell at all.

## Rules

- Never background the server as a workaround — no `otata serve &`, no
  `nohup` spawn. The server runs under launchd on a Mac or the user's systemd
  on Linux (`otata autostart on`), and `server_down` means `doctor --fix`, not
  a hand-rolled server. The exception is a headless Mac with no console
  session, which cannot host the launch agent: there `otata serve` inside a
  tmux session the user knows about is the supported mode. Linux has no such
  exception: a server that vanishes at logout wants `sudo loginctl
  enable-linger $USER`, which the user runs once.
- Never write into `~/.otata`; every mutation goes through the CLI.
- The printed URL is for the user's phone. Hand it over rather than fetching
  it yourself; when in doubt, `otata doctor` verifies every URL.
- On iOS, after the user's signing identity changes, the first publish must
  happen locally on the Mac (a keychain prompt needs **Always Allow**); a
  remote publish under a new identity hangs mid-build instead. Android
  signing is a keystore file and never prompts.
