# CLI Reference

## Commands

| Command | Does |
| --- | --- |
| `otata publish` | Build and publish the project in the current directory, or a prebuilt `--artifact` |
| `otata list` | What is published (`otata ls` works too) |
| `otata status` | Everything in one call |
| `otata doctor [--fix]` | Verify the server, transport, every URL and signing. `--fix` repairs first |
| `otata forget <slug>` | Drop one app and its payload |
| `otata serve` | Run the file server in the foreground |
| `otata start` / `stop` / `restart` | Server lifecycle |
| `otata autostart on\|off` | Run the server under launchd on a Mac or your own systemd on Linux |
| `otata transport use <name>` | `tailscale` (default) or `manual` |
| `otata transport login` | Enroll the running embedded node or retry preparation |
| `otata version` | Print the version (`--version` and `-V` work too) |
| `otata help` | The usage summary |

`otata <command> --help` prints a command's flags.

## publish

```sh
otata publish --platform ios [--config Debug] [--scheme S] [--slug NAME] [--builder archive]
otata publish --platform android [--config Debug] [--module M] [--flavor F] [--slug NAME]
otata publish --artifact PATH [--slug NAME]
```

`--platform` is required for a build, because a Mac builds for both and otata won't guess. With `--artifact`, the file's extension says which platform it is, and a `--platform` that disagrees is refused.

otata discovers the rest:

- **Project**: on iOS, a `.xcworkspace`, else a `.xcodeproj`, in the current directory or `ios/`, `iosApp/` or `apps/ios/`. On Android, the Gradle root: the directory holding `settings.gradle(.kts)`, at the top or in `android/`.
- **Scheme** (iOS): only schemes that archive an app count. otata picks the one named after the project, else a lone scheme, else it asks for `--scheme` and lists the candidates.
- **Module and flavor** (Android): the module named with `--module`, else the lone application module, else `:app`. The flavor named with `--flavor`, else the lone one, else none when the module has none. Anything else asks and lists the candidates. otata asks Gradle itself through an init script, so it never parses build scripts, convention plugins or version catalogs.
- **Signing**: the team automatic signing chose on iOS, or the signing certificate on Android, read off the built payload. `otata list` shows it.
- **Slug**: the directory name, with `-android` appended for an Android build so a cross-platform project's two builds sit side by side. A slug another project owns is refused, not overwritten.

A flag meant for the other platform, like `--scheme` on Android or `--module` on iOS, is refused rather than ignored. A project that always builds one way (`--config Debug`, or `--config Profile` for Flutter on iOS) can record its flags in its agent instructions, like AGENTS.md or CLAUDE.md.

| Flag | Does |
| --- | --- |
| `--platform` | `ios` or `android`. Required unless `--artifact` is given |
| `--config` | Build configuration, `Release` by default. A publish that falls back to the default says so before building. On Android it names the build type, `Debug` for `debug`, and a release build type with no `signingConfig` is refused with `needs_setup` before the build |
| `--scheme` | The Xcode scheme, when discovery finds several. iOS only |
| `--module` | The Gradle module, as `:app`, when the project has several application modules and none is `:app`. Android only |
| `--flavor` | The Gradle product flavor, when the module has several. With several flavor dimensions, the combination as Gradle names it, like `freeArm`. Android only |
| `--slug` | Publish under this name instead of the directory's |
| `--artifact` | Publish an already-built `.ipa` or `.apk` from any toolchain, skipping the build. Reading an `.ipa` needs plutil (macOS), and an `.apk` needs the SDK's build-tools (`ANDROID_HOME`) and a signature that verifies |
| `--builder` | `build` (the default, incremental) or `archive`. iOS only |

`--builder archive` runs `xcodebuild archive` and an export instead of an incremental build. It rebuilds everything on every publish, so it's much slower, but the payload is smaller. The default build keeps the binary's symbol table, around 40% of a Swift-heavy app.

An Android publish runs the project's own Gradle wrapper, `assemble` for the selected variant. A missing JDK 17+ or Android SDK is reported as `needs_setup` before Gradle runs. Build logs go to `~/.otata/build/<slug>/`, as `xcodebuild.log` or `gradle.log`.

## transport use

```sh
otata transport use tailscale [--hostname NAME]
otata transport use manual --base-url URL [--keep-prefix]
```

`tailscale` is the default: an embedded node that runs inside `otata serve` and serves at `https://<hostname>.<tailnet>.ts.net/`. `--hostname` names the device and defaults to `otata`. Selecting it only saves the choice, and the server enrolls and prepares HTTPS afterwards. See [Tailscale](tailscale.md).

`manual` serves through your own HTTPS proxy at `--base-url`, a plain `https://host/path`. Add `--keep-prefix` if the proxy forwards the base URL's path unchanged instead of stripping it. otata doesn't verify who can reach the proxy, so use it only for routes you consider private. See [Serving over your own proxy](manual-transports.md).

## transport login

```sh
otata transport login
```

Enrolls the running embedded node, or retries HTTPS preparation. It prints the login URL when enrollment is needed, or the next step when approval, DNS settings or the certificate are pending. Printing a URL doesn't mean enrollment is done, so check `otata status` after logging in. Running it again on an enrolled node keeps its identity. With `--json` it returns the same payload as `status --json`, under the command name `transport`.

## Environment

| Variable | Default | Does |
| --- | --- | --- |
| `OTATA_ROOT` | `~/.otata` | Where the store lives |
| `OTATA_PORT` | `8787` | Loopback port the file server binds |
| `NO_COLOR` | unset | Any value turns off ANSI color |

`OTATA_PORT` overrides `config.json` for one invocation without changing it. Only `otata transport use` writes the config. `otata autostart on` embeds the current `OTATA_ROOT` and `OTATA_PORT` in the unit it installs.

## Output

Text by default, in color when stdout is a terminal and `NO_COLOR` is unset. `--json`, accepted anywhere on the command line, prints one envelope on stdout instead:

```json
{
  "ok": true,
  "command": "publish",
  "data": { "...": "the command's result" },
  "error": null
}
```

On failure `ok` is `false` and `error` carries a stable `code`, a `message`, and often a `hint` and structured `details`. Build progress goes to stderr.

## Error codes

The exit code is 0 on success, 2 when the command was called wrongly, 1 when it ran and failed, and 128 plus the signal number when a signal stopped it (130 for Ctrl-C, 143 for SIGTERM).

| Code | Means | What to do |
| --- | --- | --- |
| `no_project` | Nothing buildable here | Check the directory, or pass `--artifact` |
| `ambiguous_scheme` | Several candidates for a choice discovery could not make: a scheme, an application module or a product flavor | Re-run with the flag in `details.flag`, choosing from `details.candidates` |
| `needs_setup` | A prerequisite of the project's toolchain is missing | Run `details.command` in `details.dir` when there is one, otherwise follow the hint |
| `build_failed` | The toolchain returned non-zero | Read the log path in `details` |
| `signing_failed` | On iOS, the certificate, profile or device registration. On Android, an APK that is unsigned or does not verify | Needs a human with Apple portal access, or a signed APK: a debug build, or a release signingConfig |
| `free_profile` | Signed by a free personal team, which iOS will not install over the air | Sign with a paid team. Nothing else fixes it |
| `server_down` | Local server not running, or the port is held by something else | `otata autostart on` once, then `otata doctor --fix` and retry |
| `transport_down` | Transport not ready: enrollment, approval, HTTPS preparation, or connectivity needs attention | Check `otata status` and `otata doctor`. Enroll with `transport login` |
| `no_transport` | Unknown transport in config, or `manual` has no base URL | Run the `otata transport use` command the hint names |
| `slug_conflict` | Another project owns this name, or its record is unreadable | Pass `--slug`, or `otata forget` it |
| `build_in_progress` | A live publish already holds this slug | Wait. `otata doctor --fix` clears a marker whose process is gone |
| `not_found` | No app published under that slug | Check `otata list` |
| `unhealthy` | `doctor` found something wrong | Read `data.checks`. Each failing check says what to do, often `--fix` |
| `interrupted` | A signal stopped the publish (Ctrl-C, a timeout, a dropped SSH session). The build was killed and its marker cleared | Retry when ready |
| `invalid_args` | The command was called wrongly | Fix the arguments |
| `internal` | Anything unclassified | Read the message. It is not expected |
