# FAQ

## How does the iOS install actually work?

`itms-services://` is Apple's method for installing a signed app from a web-hosted manifest.
The browser hands the manifest URL to the iOS install daemon, which downloads the `.ipa` and installs it.
Both the manifest and the payload must be served over HTTPS with a publicly trusted certificate; self-signed is refused.
On the tailscale transport that certificate is a real Let's Encrypt one, obtained by `tailscale cert` for a name that resolves only inside your tailnet.

```
phone (on the tailnet)
  └── https://<host>.<tailnet>.ts.net/otata/     trusted cert, tailnet-only
        └── transport (tailscale serve / your proxy)
              └── 127.0.0.1:8787       loopback only
                    └── ~/.otata/public/<slug>/
```

## Is using `itms-services://` against Apple's rules?

No. The scheme installs any non-App-Store build, not just in-house
enterprise apps. Apple documents the manifest mechanism and its HTTPS
requirement in the [Apple Platform Deployment
guide](https://support.apple.com/guide/deployment/depce7cefc4d/web), and
Xcode generates the same manifest for `release-testing` and `debugging`
exports. `xcodebuild -help` describes the `manifest` export option as for
"non-App Store exports", not enterprise ones. What the [Apple Developer
Program License
Agreement](https://developer.apple.com/support/terms/apple-developer-program-license-agreement/)
restricts is *who* may receive a signed build (§7.3, "Distribution on
Registered Devices (Ad Hoc Distribution)"): your registered devices, up
to 100 per device type per membership year, for testing. It doesn't enforce
anything about how the bytes travel. otata serves builds signed under your own account
to your own registered devices, which is that permitted distribution. The
"employees only" restriction belongs to the separate Enterprise Program
agreement, which otata does not involve.

## How does `otata publish` decide what to build?

- **Platform**: never discovered. `--platform ios|android` is required, because a
  Mac builds for both and a default there would be a guess. With `--artifact`
  the payload's extension says which platform it is.
- **Project**: the single `.xcworkspace` in the current directory, else the
  single `.xcodeproj`; `ios/`, `iosApp/` and `apps/ios/` are searched too.
- **Scheme**: only schemes that archive an app count — each `.xcscheme` is
  read for a `.app` buildable marked `buildForArchiving="YES"`, which drops
  package schemes. It prefers a scheme named after the project, else a lone
  scheme, else it asks for `--scheme` and lists the candidates.
- **Team**: whichever automatic signing chose, read off the payload's
  profile; `list` and `status` name it.
- **Slug**: the directory name; a slug another project owns is refused, not
  overwritten.

## What does a cross-platform project need before its first publish?

Flutter, React Native and Kotlin Multiplatform keep an Xcode project in
`ios/` or `iosApp/`, where discovery already looks, so `otata publish` builds them
like a native app. Missing prerequisites are reported before the archive as
`needs_setup` with the command to run. otata has been verified on Xcode 26.2 with Flutter
3.47, React Native 0.87 and the JetBrains KMP template. The iOS half of a
KMP project needs no Android SDK.

| Framework | Needed before the first publish | Worth knowing |
| --- | --- | --- |
| Flutter | nothing after `flutter create`; `flutter build ios --config-only` in a fresh clone | `ios/Flutter/Generated.xcconfig` is generated and gitignored, so no clone has one. Iterate with `--config Profile` and not `Debug` |
| React Native | `pod install` in `ios/` | until it runs there is no `.xcworkspace`, and the `.xcodeproj` beside it cannot link |
| Kotlin Multiplatform | a JDK 17+, and a team in `iosApp/Configuration/Config.xcconfig` | the first build downloads the Kotlin/Native toolchain: twelve minutes here, four on a warm cache |

## How do I make a project always build another configuration?

A project that should always build another way (`Debug` for its `#if DEBUG`
tooling; `Profile` on Flutter, where `Debug` cannot launch from the home
screen) should record the flag in its agent instructions (AGENTS.md, CLAUDE.md, etc.) so it
travels with every publish. Each build records the configuration it was
built with, and `list` shows it.

## How do I know the build I tap is the one just published?

iOS installs without confirmation, so tapping before the latest build lands will silently
install the *previous* one. What closes that:

- Every entry shows its age ("40 seconds ago") beside its commit, computed on
  the phone from the build's timestamp; without script it shows the absolute
  time.
- An app mid-build says so and cannot be tapped; the page refreshes itself
  while any build runs.
- The build marker records its process, so `doctor --fix` can clear one whose
  process is gone or older than any build could be.
- The marker is also a lock: a second publish of the same slug is refused
  with `build_in_progress`.

## What exactly does `doctor` check?

It checks the whole path: server up and serving this root, transport
wired, no build marker left by a dead process, the index and every manifest
and payload answering over the real URL, signing unexpired. It exits
non-zero if anything is wrong, and every failing check says how to fix it.
Under `--json`, unhealthy is `ok: false` with the `unhealthy` code, and
`data.checks` still carries every check.

`otata doctor --fix` is what you should reach for remotely and after a reboot.
It repairs first (reloads the launch agent, wires the transport, clears stale
markers, regenerates the pages, restarts a server serving a `public/` since
recreated), then runs the same checks, so the exit code reports what is
still broken. It will not replace a server belonging to a different
`OTATA_ROOT`. `otata restart` does that explicitly.

A build stops being installable when its profile or certificate expires,
whichever is earlier. `doctor` warns inside 30 days and fails past the deadline.

```sh
$ otata doctor
==> Checks
    OK    myapp payload
    WARN  myapp signing: certificate expires 2026-11-11, in 12 days
```

## When is the server up?

Only while the Mac is awake and logged in. It runs under launchd: `otata
autostart on` installs the launch agent once. It returns at login and
restarts if it exits. `otata stop` unloads it but leaves it installed (`otata status`
says so); `otata start`, `otata publish` and `otata doctor --fix` bring it back through
launchd, and with no agent installed they refuse and name the command. The
only other server is `otata serve` in a foreground terminal, alive as long
as the terminal.

One agent exists per user, embedding the `OTATA_ROOT`, `OTATA_PORT` and
`OTATA_PATH` it was installed with: a shell with a scratch root sees
`autostart off`, cannot stop it, and is refused `autostart on`. Nothing
returns before login: FileVault keeps the volume encrypted until someone
types the password. What to set up before leaving the Mac — Tailscale at
login, sleep, the keychain prompt — is in
[otata-via-ssh.md](otata-via-ssh.md#initial-setup-on-the-mac).

## What's in `~/.otata`?

| Path | Holds |
| --- | --- |
| `public/` | The only tree ever served: the index, and a directory per app with its page, manifest, icon and payload |
| `state/` | One record per app, naming local paths, and the marker of a build in flight |
| `build/` | Archives, exports and the build log, per app |
| `tmp/` | Where publishing stages a file before renaming it into `public/`, so a client can never fetch a half-written one |
| `bin/` | A copy of the otata binary, present only when the installed one sits where launchd cannot read it |
| `config.json` | The port, serve path and selected transport |
| `server.log` | The background server's own log and its access log |

## Why not TestFlight or Firebase App Distribution?

otata is much faster than TestFlight (even TestFlight Internal Testing!) and Firebase App Distribution, because those are intended
as distribution channels for testing and involve additional costs of archiving, auth, uploads to Apple/Google servers, and their respective processing.
Most notably, the required app archive for these paths *always* recompiles every app target, and is significantly slower than the incremental build that otata defaults to.
For a real nine-target app, archiving took ~100s while incremental rebuild took ~4s.

Of course, YMMV depending on your specific circumstances, so benchmarking the speed difference yourself is encouraged!

