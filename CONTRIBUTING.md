# Contributing

otata is a relatively small Go project with no dependencies outside the standard library.
This doc should give you everything you need to get started with contributing.

Until otherwise noted, **the steps below are intended for macOS and Linux only.**

## Building

`git clone`, then:

```sh
make build     # bin/otata
make install   # copy to ~/.local/bin and refresh the launch agent or systemd unit
make test
make vet
```

`make install` copies instead of symlinking on purpose. A symlink into a
TCC-protected checkout will hit the launchd hang described in
[docs/gotchas.md](docs/gotchas.md). It writes beside the target and renames,
because overwriting a running binary corrupts its mapped image and macOS kills
the process, and Linux refuses the write outright.

## Repository layout

| Package | What it handles |
| --- | --- |
| `main.go` | Command dispatch and flag parsing only |
| `internal/cli` | How commands talk to their caller: JSON vs text, the error taxonomy |
| `internal/app` | Orchestration: the only place that knows about the others at once, one file per command |
| `internal/config` | The little that cannot be discovered: port, serve path, the selected transport |
| `internal/storage` | The on-disk layout, and the **only** definition of it |
| `internal/atomicfile` | Stage-and-rename writes, so a crash never leaves a torn file |
| `internal/artifact` | The record: what a published build is, and the platform it runs on |
| `internal/builder` | Turning a project into a payload; the only place that knows what Xcode and Gradle are |
| `internal/appmeta` | Reading identity, icon and signing out of a built payload, one reader per platform |
| `internal/transport` | Making the loopback server reachable; the only place that knows what Tailscale is |
| `internal/server` | The install surface over HTTP |
| `internal/render` | The pages, with templates embedded in the binary |
| `internal/version` | This binary's version, read from the build's VCS stamp |

## Testing

```sh
go test ./...              # everything
go test ./internal/server/ -v
```

The suite passes on Linux as well as macOS, and CI runs it on both, each
also vetting the other's build. A test that shells out to a macOS tool
(`ditto`, `plutil`, `pngcrush`) will skip where the tool is absent instead
of failing. Code that drives the tailscale transport is tested against the
fake CLI in `internal/transport/transporttest`, which answers from canned
JSON and counts its invocations; putting its directory on `PATH` is what
makes the transport find it.

Five `*_manual_test.go` files run against real local projects and artifacts
and **skip unless told where they are**, so the test suite stays hermetic on
a fresh machine:

```sh
OTATA_TEST_PROJECT=~/path/to/MyApp OTATA_TEST_SCHEME=MyApp go test ./internal/builder/ -run Real -v
OTATA_TEST_GRADLE=~/path/to/AndroidApp OTATA_TEST_FLAVOR=free go test ./internal/builder/ -run Real -v
OTATA_TEST_IPA=~/.otata/public/myapp/MyApp.ipa go test ./internal/appmeta/ -run Real -v
OTATA_TEST_APK=~/path/to/app-debug.apk go test ./internal/appmeta/ -run Real -v
```

The Gradle one builds a Debug APK of the project through the real builder,
with `ANDROID_HOME` set and whatever the project's wrapper and plugin need
downloaded on the way; `OTATA_TEST_FLAVOR` is only for a module with several
flavors. The APK readers need the SDK's build-tools through `ANDROID_HOME` too.

## A few important details

- **Human output goes through `cli.Line`**, which strips control characters. A
  display name out of an Info.plist is untrusted for `--artifact`.
- **Files reach `public/` through the store's atomic writes**, never a direct
  write. The server reads the tree the publisher is writing.
- **Values interpolated into a plist go through `xmlText`.** Manifests, the
  launch agent and export options are assembled by hand from untrusted values.
  The systemd unit has its own quoting rules, kept beside it in
  `internal/app/supervisor_linux.go`.
