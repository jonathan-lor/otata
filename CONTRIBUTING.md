# Contributing

otata is a relatively small Go project with no dependencies outside the standard library.
This doc should give you everything you need to get started with contributing.

Until otherwise noted, **the steps below are for macOS only.**

## Building

```sh
make build     # bin/otata
make install   # copy to ~/.local/bin and refresh the launch agent
make test
make vet
```

`make install` copies instead of symlinking on purpose. A symlink into a
TCC-protected checkout results in the launchd hang described in
[docs/gotchas.md](docs/gotchas.md). It writes beside the target and renames,
because overwriting a running binary corrupts its mapped image and macOS kills
the process.

## Repository layout

| Package | What it handles |
| --- | --- |
| `main.go` | Command dispatch and flag parsing only |
| `internal/cli` | How commands talk to their caller: JSON vs text, the error taxonomy |
| `internal/app` | Orchestration: the only place that knows about the others at once |
| `internal/storage` | The on-disk layout, and the **only** definition of it |
| `internal/artifact` | The record: what a published build is |
| `internal/builder` | Turning a project into a payload; the only place that knows what Xcode is |
| `internal/appmeta` | Reading identity and icon out of a built app or an `.ipa` |
| `internal/transport` | Making the loopback server reachable; the only place that knows what Tailscale is |
| `internal/server` | The install surface over HTTP |
| `internal/render` | The pages, with templates embedded in the binary |

## Testing

```sh
go test ./...              # everything
go test ./internal/server/ -v
```

Three `*_manual_test.go` files run against real local artifacts and **skip
unless told where they are**, so the test suite stays hermetic on a fresh machine:

```sh
OTATA_TEST_PROJECT=~/path/to/MyApp OTATA_TEST_SCHEME=MyApp go test ./internal/builder/ -run Real -v
OTATA_TEST_IPA=~/.otata/public/myapp/MyApp.ipa go test ./internal/appmeta/ -run Real -v
```

## Some important things to keep in mind

- **`storage` validates every slug it turns into a path.** A slug becomes a
  path component and a URL segment, and the validator accepts exactly what
  `Slugify` produces.
- **Every value interpolated into a plist is XML-escaped.** Manifests, the
  launch agent and export options are assembled by hand from untrusted values.
- **Files are staged in `tmp/` and renamed into place.** The server reads the
  same tree the publisher writes. A rename is atomic and a copy is not.
- **`public/` is the only directory the server exposes.** `state/` names local paths
  and `build/` holds archives.
- **`os.Root` confines file serving.** Do not replace it with `filepath.Join`
  plus string checks! It is what refuses a symlink escape, and the traversal
  tests pass without it.
- **Binding the port is the only proof the server started.** `launchctl` reports
  a job stuck in dyld as `running`, so asking launchd proves nothing.
- **Port occupancy is not process identity.** The server sets `X-Otata` and
  `X-Otata-Root` (a digest of the store it serves) on every response, and
  "running" means both match. Otherwise a publish writes into its own tree
  while the server on the port serves another, and reports a URL that 404s.
- **Values in human output are stripped of control characters.** A display name
  comes out of an Info.plist that is untrusted for `--artifact`; `cli.Line`
  cleans every string argument, and the format string (ours) keeps its color.
- **Derived data is rebuilt where derived data is rebuilt.** Manifests embed the
  base URL, so `Reindex` regenerates them alongside the pages.
- **The pages are server-rendered truth.** The install latchin the web UI has one allowed
  shape: the `href` is rendered, and script can only take the link away after a
  tap. The age is the other: the HTML carries the build's timestamp as an
  absolute time, and script rewrites it to "40 seconds ago" on the phone.
- **Visibility is decided for the listener, before wiring.** Funnel is per
  `host:port`, so the check cannot wait for our handler to exist. It did once,
  and the first publish onto an already-funnelled `:443` was public until the
  next command looked.
- **One thing says if a build marker is live.** The publish lock, `forget`
  and `doctor` all ask `markerStale`.
- **The launch agent belongs to the root and port it embeds.** `stop`, `status`
  and `start` consult it only when those match the current invocation. Matched
  by label alone, `otata stop` under a scratch `OTATA_ROOT` boots the real
  agent out.
- **A background server exists only under launchd.** `StartServer` reloads the
  installed launch agent or refuses with the command to run.
  `otata serve` in a foreground terminal or tmux are the exceptions.
