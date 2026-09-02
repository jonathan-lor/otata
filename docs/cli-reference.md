# CLI Reference

## Commands

| Command | Does |
| --- | --- |
| `otata publish [--config Debug] [--scheme S] [--slug NAME] [--artifact PATH] [--builder archive]` | Build and publish the project in the current directory |
| `otata list` | What is published (`otata ls` works too) |
| `otata status` | Everything in one call |
| `otata doctor [--fix]` | Verify the server, transport and every URL, report a signing deadline; `--fix` repairs first |
| `otata forget <slug>` | Drop one app and its payload |
| `otata serve` | Run the file server in the foreground |
| `otata start` / `stop` / `restart` | Server lifecycle |
| `otata autostart on\|off` | Run the server under launchd: at login, restarted if it exits |
| `otata transport use <name>` | `tailscale` or `manual` |
| `otata version` | Print the version (`--version` and `-V` work too) |
| `otata help` | The usage summary |

`otata <command> --help` will print the flags for that command.

## publish

```sh
otata publish [--config Debug] [--scheme S] [--slug NAME] [--artifact PATH] [--builder archive]
```

Discovery will fill in what you don't pass: the workspace or project, a scheme that
archives an app, the signing team, and the slug from the directory name.
`--scheme` and `--slug` are for when it asks.

| Flag | Does |
| --- | --- |
| `--config` | Build configuration. Defaults to `Release`, and a publish that falls back to it says so before the build starts |
| `--scheme` | The scheme to build, when discovery finds several candidates |
| `--slug` | Publish under this name instead of the directory's |
| `--artifact` | Publish an already-built `.ipa` from any toolchain, skipping the build entirely |
| `--builder` | `build` (the default, incremental) or `archive` |

Publishes build incrementally by default. `--builder archive` uses `xcodebuild
archive` + export instead, which rebuilds everything every time and will be
noticeably slower. It does however produce a smaller payload. See [gotchas](gotchas.md).

## transport use

```sh
otata transport use <tailscale|manual> [--base-url URL] [--keep-prefix] [--visibility private|public]
```

`tailscale` is reachable by the devices on your tailnet and is private.
`manual` is reachable by whatever your proxy serves, with a visibility you
declare:

```sh
otata transport use tailscale
otata transport use manual --base-url https://builds.example.com/otata
```

`--base-url`, `--keep-prefix` and `--visibility` apply to `manual` only.
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

The exit code is 0 on success, 2 when the command was called wrongly, and 1 when
it ran and failed.

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
