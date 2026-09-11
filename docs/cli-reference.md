# CLI Reference

## Commands

| Command | Does |
| --- | --- |
| `otata publish --platform ios [--config Debug] [--scheme S] [--slug NAME] [--builder archive]` | Build and publish the project in the current directory for iOS |
| `otata publish --platform android [--config Debug] [--module M] [--flavor F] [--slug NAME]` | Build and publish the project in the current directory for Android |
| `otata publish --artifact PATH [--slug NAME]` | Publish an already-built payload instead |
| `otata list` | What is published (`otata ls` works too) |
| `otata status` | Everything in one call |
| `otata doctor [--fix]` | Verify the server, transport and every URL, report a signing deadline; `--fix` repairs first |
| `otata forget <slug>` | Drop one app and its payload |
| `otata serve` | Run the file server in the foreground |
| `otata start` / `stop` / `restart` | Server lifecycle |
| `otata autostart on\|off` | Run the server under launchd on a Mac or your own systemd on Linux |
| `otata transport use <name>` | `tailscale` or `manual` |
| `otata version` | Print the version (`--version` and `-V` work too) |
| `otata help` | The usage summary |

`otata <command> --help` will print the flags for that command.

## publish

```sh
otata publish --platform ios [--config Debug] [--scheme S] [--slug NAME] [--builder archive]
otata publish --platform android [--config Debug] [--module M] [--flavor F] [--slug NAME]
otata publish --artifact PATH [--slug NAME]
```

`--platform` is always required. A project that always
builds for one platform can record the flag in its agent instructions. With
`--artifact` the payload's extension specifies its platform, so the flag isn't needed there,
and one that disagrees with the file is refused.

Discovery fills in the rest. On iOS: the workspace or project, a scheme that
archives an app, and the signing team. On Android: the Gradle root, the
application module and its product flavor, and the signing certificate. On
both, the slug from the directory name, with `-android` appended for an
Android build so a cross-platform project's two builds sit side by side.
`--scheme`, `--module`, `--flavor` and `--slug` are for when it asks. Each
platform selects its build in its own toolchain's terms. An inapplicable flag
meant for the other platform is refused.

| Flag | Does |
| --- | --- |
| `--platform` | What to build for: `ios` or `android`. Required for a build; `--artifact` reads it off the file |
| `--config` | Build configuration. Defaults to `Release`, and a publish that falls back to it says so before the build starts. On Android it names the build type as Gradle spells it, `Debug` for `debug`, and a release build type with no signingConfig is refused before the build with `needs_setup`, since Android will not install the unsigned APK it would produce |
| `--scheme` | The Xcode scheme to build, when discovery finds several candidates. iOS only |
| `--module` | The Gradle module to build, as `:app`, when the project has several application modules and none is `:app`. Android only |
| `--flavor` | The Gradle product flavor to build, when the module defines several; with several flavor dimensions, the combination as Gradle names it, `freeArm`. Android only |
| `--slug` | Publish under this name instead of the directory's |
| `--artifact` | Publish an already-built `.ipa` or `.apk` from any toolchain, skipping the build entirely. Reading it needs the platform's tools: plutil on macOS for an `.ipa`, the SDK's build-tools (`ANDROID_HOME`) for an `.apk`, which must also verify, since Android will not install one that does not |
| `--builder` | `build` (the default, incremental) or `archive`. iOS only |

Publishes build incrementally by default. `--builder archive` uses `xcodebuild
archive` + export instead, which rebuilds everything every time and will be
noticeably slower. It does however produce a smaller payload. See [gotchas](gotchas.md).

An Android publish runs the project's own Gradle wrapper: `assemble` for the
selected variant, on its module, with the log at
`~/.otata/build/<slug>/gradle.log`. It needs a JDK 17 or newer and the Android
SDK, found through `ANDROID_HOME` or the project's `local.properties`, whose
build-tools also read the APK afterwards. Anything missing is reported as
`needs_setup` before Gradle runs. Flutter and React Native keep their Android
project in `android/`, where discovery looks.

## transport use

```sh
otata transport use <tailscale|manual> [--base-url URL] [--keep-prefix]
```

`tailscale` is reachable by the devices on your tailnet and is private.
`manual` is reachable by whatever your proxy serves, which otata does not
verify; it is for routes you consider private:

```sh
otata transport use tailscale
otata transport use manual --base-url https://builds.example.com/otata
```

`--base-url` and `--keep-prefix` apply to `manual` only.
`--keep-prefix` says your proxy forwards the base URL's path unchanged instead
of stripping it. The transport is selected once and validated then. Walkthroughs
for the manual transport are in [Serving over your own proxy](manual-transports.md).

## Environment

| Variable | Default | Does |
| --- | --- | --- |
| `OTATA_ROOT` | `~/.otata` | Where the store lives |
| `OTATA_PORT` | `8787` | Loopback port the file server binds |
| `OTATA_PATH` | `/otata` | Path the transport serves otata under |
| `NO_COLOR` | unset | Any value turns off ANSI color |

`OTATA_PORT` and `OTATA_PATH` override the stored config for one invocation
without persisting it. `otata transport use` is what writes config to disk.

## Output

Human-readable text by default and color when stdout is a terminal and `NO_COLOR`
is unset.

`--json` is accepted anywhere on the command line for better agent parsing:

```json
{
  "ok": true,
  "command": "publish",
  "data": { "...": "the command's result" },
  "error": null
}
```

On failure `ok` is `false` and `error` carries a stable `code`, a `message`, and
often a `hint` and structured `details`. Build progress goes to stderr.

## Error codes

The exit code is 0 on success, 2 when the command was called wrongly, 1 when
it ran and failed, and 128 plus the signal number when a signal stopped it
(130 for Ctrl-C, 143 for SIGTERM).

| Code | Means | What to do |
| --- | --- | --- |
| `no_project` | Nothing buildable here | Check the directory, or pass `--artifact` |
| `ambiguous_scheme` | Several candidates for a choice discovery could not make: a scheme, an application module or a product flavor | Re-run with the flag in `details.flag`; the candidates are in `details.candidates` |
| `needs_setup` | A step the project's own toolchain owns has not been run, or a setting its build needs is missing | Run `details.command` in `details.dir` when there is one, then retry; otherwise the hint names the setting |
| `build_failed` | The toolchain returned non-zero | Read the log path in `details` |
| `signing_failed` | Certificate, profile or device registration on iOS; an APK that is unsigned or does not verify on Android | Needs a human with Apple portal access, or a signed APK: a debug build, or a release signingConfig |
| `free_profile` | Signed by a free personal team, which iOS will not install over the air | Sign with a paid team; nothing else fixes it |
| `server_down` | Local server not running, or the port is held by something else | `otata autostart on` once; after that, `otata doctor --fix` and retry |
| `transport_down` | Transport present but unusable: Tailscale logged out or without HTTPS certificates, or the route is publicly reachable (Funnel on, or a proxy declared public) | Needs the machine; `otata doctor` names it |
| `no_transport` | No transport selected, or `manual` has no base URL | Run the `otata transport use` command the hint names |
| `slug_conflict` | Another project owns this name, or its record is unreadable | Pass `--slug`, or `otata forget` it |
| `build_in_progress` | A live publish already holds this slug | Wait; `otata doctor --fix` clears a marker whose process is gone |
| `not_found` | No app published under that slug | Check `otata list` |
| `unhealthy` | `doctor` found something wrong | Read `data.checks`; each failing one says what to do, often `--fix` |
| `interrupted` | A signal stopped the publish (Ctrl-C, a timeout, a dropped SSH session); the build was killed and its marker cleared | Retry when ready |
| `invalid_args` | The command was called wrongly | Fix the arguments |
| `internal` | Anything unclassified | Read the message; it is not expected |
