---
name: otata
description: Publish the latest iOS build to the user's phone over the air. Use when the user wants to see, test, or install the current build on their device ("get this on my phone", "publish the build", "let me try it"). Runs on the user's Mac, directly or over SSH.
---

# Publishing iOS builds with otata

otata builds the project in the current directory, signs it, publishes it to a
server on the user's Mac, and prints a URL. The user opens the URL on their
phone and taps Install. Your job ends at handing over the URL; the install
happens on the phone.

## The one command

```sh
cd /path/to/MyApp        # the project root
otata publish --json
```

- `--json` is accepted anywhere on any otata command. Success and failure both
  print one JSON envelope on stdout; progress goes to stderr.
- A publish is synchronous and a build can take minutes. Give it a generous
  timeout and do not kill it for being slow.
- `--config Debug` builds much faster than the default Release when the user
  just wants to see a change — except on Flutter, where a Debug build cannot
  launch from the home screen: use `--config Profile` there.
- `otata publish --artifact path/to/App.ipa` publishes an already-built .ipa
  from any toolchain.
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
| `ambiguous_scheme` | Re-run with `--scheme`; the candidates are in `details` |
| `needs_setup` | Run `details.command` in `details.dir`, then retry |
| `build_failed` | Read the log at the path in `details` |
| `signing_failed` | Needs a human with Apple portal access; do not retry |
| `free_profile` | iOS refuses free-team builds over the air; a paid team is the only fix. Do not retry |
| `server_down` | `otata doctor --fix`; if autostart was never set up, `otata autostart on` once |
| `transport_down` | Machine-side (Tailscale logged out, or the route is public: Funnel on); `otata doctor` names it. Do not retry until it is fixed |
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

Doctor repairs what it can (launch agent, transport wiring, stale build
markers, pages), then verifies every URL and exits non-zero with each failing
check naming its remedy. Logs on the Mac:

- `~/.otata/server.log` — server and access log
- `~/.otata/build/<slug>/xcodebuild.log` — the build that failed

## Not on the Mac?

otata runs only on the Mac; drive it over SSH from anywhere:

```sh
ssh mac 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --json'
```

- Spell the binary's full path: non-interactive zsh reads only `~/.zshenv`,
  so `command not found` over SSH while otata works in a terminal on the Mac
  is a PATH problem, not a broken install.
- The envelope, codes, and exit codes are unchanged.
- An SSH connection that dies mid-build kills the publish (it cleans up like
  a Ctrl-C). On a flaky link, run it under `tmux` or `nohup`.
- Keep the project at one path on the Mac: the same project at a new location
  is refused with `slug_conflict`.

## Rules

- Never background the server as a workaround — no `otata serve &`, no
  `nohup` spawn. The server runs under launchd (`otata autostart on`), and
  `server_down` means `doctor --fix`, not a hand-rolled server. The exception
  is a headless Mac with no console session, which cannot host the launch
  agent: there `otata serve` inside a tmux session the user knows about is
  the supported mode.
- Never write into `~/.otata`; every mutation goes through the CLI.
- The printed URL is for the user's phone. Hand it over rather than fetching
  it yourself; when in doubt, `otata doctor` verifies every URL.
- After the user's signing identity changes, the first publish must happen
  locally on the Mac (a keychain prompt needs **Always Allow**); a remote
  publish under a new identity hangs mid-build instead.
