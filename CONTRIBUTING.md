# Contributing

otata needs Go 1.26.6 or newer, on macOS or Linux.

## Building

```sh
make build     # bin/otata
make install   # copy to ~/.local/bin and refresh the launch agent or systemd unit
make test
make vet
```

To test without touching an installed release, use a scratch root and a foreground server, since there's only one autostart unit per user:

```sh
make build
export OTATA_ROOT="$(mktemp -d /tmp/otata-dev.XXXXXX)"
export OTATA_PORT=18877
./bin/otata transport use tailscale --hostname otata-dev
./bin/otata serve
# In another terminal with the same environment:
./bin/otata transport login
./bin/otata doctor
```

## Repository layout

| Package | What it handles |
| --- | --- |
| `main.go` | Command dispatch and flag parsing |
| `internal/cli` | JSON and text output, and the error codes |
| `internal/app` | Orchestration, one file per command. The only package that knows about the others |
| `internal/config` | Port, selected transport, node hostname and manual proxy settings |
| `internal/storage` | The on-disk layout, defined nowhere else |
| `internal/atomicfile` | Stage-and-rename writes, so a crash never leaves a torn file |
| `internal/artifact` | The record of a published build and its platform |
| `internal/builder` | Turning a project into a payload. The only package that knows Xcode and Gradle |
| `internal/appmeta` | Reading identity, icon and signing out of a built payload, one reader per platform |
| `internal/transport` | Embedded Tailscale and manual HTTPS adapters |
| `internal/tsnetnode` | The embedded node's lifecycle and its authenticated local control API |
| `internal/server` | The install surface over HTTP |
| `internal/render` | The pages, with templates embedded in the binary |
| `internal/version` | This binary's version, from the build's VCS stamp |

## Testing

```sh
go test ./...
go test ./internal/server/ -v
```

CI runs the suite with the race detector on macOS and Linux. Tests that need a macOS tool (`ditto`, `plutil`, `pngcrush`) skip without it, and the embedded node tests use a fake backend, so nothing enrolls a real device.

The `*_manual_test.go` files run against real projects and artifacts, and skip unless told where they are:

```sh
OTATA_TEST_PROJECT=~/path/to/MyApp OTATA_TEST_SCHEME=MyApp go test ./internal/builder/ -run Real -v
OTATA_TEST_GRADLE=~/path/to/AndroidApp OTATA_TEST_FLAVOR=free go test ./internal/builder/ -run Real -v
OTATA_TEST_IPA=~/.otata/public/myapp/MyApp.ipa go test ./internal/appmeta/ -run Real -v
OTATA_TEST_APK=~/path/to/app-debug.apk go test ./internal/appmeta/ -run Real -v
```

The Gradle test builds a Debug APK through the real builder, so it needs `ANDROID_HOME` and downloads whatever the project's wrapper needs. `OTATA_TEST_FLAVOR` is only for a module with several flavors. The APK readers need the SDK's build-tools through `ANDROID_HOME` too.

## A few important details

- **Human output goes through `cli.Line`**, which strips control characters, because a display name from an `--artifact`'s Info.plist is untrusted.
- **Files reach `public/` only through the store's atomic writes**, because the server reads the tree while publish writes it.
- **Values interpolated into a plist are XML-escaped.** Manifests, the launch agent and export options are built by hand from untrusted values. The systemd unit has its own quoting rules, in `internal/app/supervisor_linux.go`.
