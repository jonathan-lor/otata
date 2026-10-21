# FAQ

## How does the iOS install actually work?

The install link is an `itms-services://` URL, Apple's way to install a signed app from a web-hosted manifest. The browser hands the manifest URL to iOS, which downloads the `.ipa` and installs it. Both need to be served over HTTPS with a publicly trusted certificate, which the embedded Tailscale node gets for its tailnet hostname.

```
phone (on the tailnet)
  └── https://<node>.<tailnet>.ts.net/          trusted cert, tailnet-only
        └── otata's embedded Tailscale HTTPS listener
              └── ~/.otata/public/<slug>/
```

## How does the Android install work?

The app's page links the `.apk` itself. The browser downloads it, and tapping the download hands it to Android's package installer. The first time, Android asks you to allow installs from that browser.

Android identifies an app by its package name and signing certificate. A build signed with the same certificate replaces the installed one, and one signed with any other is refused until you uninstall the old copy. Each machine generates its own debug keystore, so debug builds from two computers can't replace each other unless you copy `~/.android/debug.keystore` between them. otata verifies an APK's signature before serving it, since an unsigned one would fail on every tap.

## Is using `itms-services://` against Apple's rules?

No. Apple documents the manifest install in its [Platform Deployment guide](https://support.apple.com/guide/deployment/depce7cefc4d/web), and Xcode writes the same manifest for its `release-testing` and `debugging` exports. The [Developer Program License Agreement](https://developer.apple.com/support/terms/apple-developer-program-license-agreement/) limits who may receive a signed build (§7.3): your registered devices, up to 100 per device type per membership year, for testing. otata serves builds signed by your account to your registered devices, which is exactly that. The "employees only" rule belongs to the separate Enterprise Program, which otata doesn't use.

## What does a cross-platform project need before its first publish?

Flutter, React Native and Kotlin Multiplatform keep their Xcode project in `ios/` or `iosApp/` and their Android project in `android/` or at the root, where discovery already looks. Anything missing is reported before the build as `needs_setup`, with the command to run. The iOS side has been verified on Xcode 26.2 with Flutter 3.47, React Native 0.87 and the JetBrains KMP template.

| Framework | Needed before the first publish | Worth knowing |
| --- | --- | --- |
| Flutter | Nothing after `flutter create`. A fresh clone needs `flutter build ios --config-only` for iOS and `flutter pub get` for Android | Iterate on iOS with `--config Profile`, since a `Debug` build can't launch from the home screen |
| React Native | `pod install` in `ios/`. For Android, `npm install` if there's no `node_modules` | Until `pod install` runs there is no `.xcworkspace`, and the `.xcodeproj` beside it can't link |
| Kotlin Multiplatform | A JDK 17+, and a team in `iosApp/Configuration/Config.xcconfig` | The first build downloads the Kotlin/Native toolchain: twelve minutes here, four on a warm cache. The iOS side needs no Android SDK |

## How do I know the build I tap is the one just published?

iOS installs without asking, so tapping too early silently installs the *previous* build. To prevent that, every app shows its age ("40 seconds ago") beside its commit, and an app mid-build says so and can't be tapped. The page refreshes itself while a build runs. A second publish of the same app is refused with `build_in_progress` until the first one finishes.

## What exactly does `doctor` check?

The whole path: the server is up and serving this root, the transport is ready, no build marker is left by a dead process, the index and every manifest and payload answer over the real URL, and signing hasn't expired. It exits non-zero if anything fails, and every failing check says how to fix it. Under `--json`, a failure is `ok: false` with the `unhealthy` code, and `data.checks` still lists every check.

`otata doctor --fix` is the one to reach for remotely and after a reboot. It repairs first (reloads the launch agent or systemd unit, waits for the transport, clears stale markers, regenerates the pages, restarts a server whose `public/` was recreated), then runs the same checks. It won't replace a server for a different `OTATA_ROOT`. `otata restart` does that.

An iOS build stops installing when its profile or certificate expires, whichever comes first. `doctor` warns 30 days ahead and fails past the deadline. An Android certificate is good for decades, so `doctor` just names the signer.

```sh
$ otata doctor
==> Checks
    OK    myapp payload
    WARN  myapp signing: certificate expires 2026-11-11, in 12 days
```

## When is the server up?

While the computer is awake and you're logged in. `otata autostart on` installs a launch agent on a Mac or a systemd user unit on Linux, which starts the server at login and restarts it if it crashes. Nothing starts before login. FileVault keeps a Mac's disk locked until someone types the password, and a Linux user's systemd waits for a login unless [lingering](getting-started.md#3-enroll-otata) is on.

`otata stop` unloads the unit but leaves it installed. `otata start`, `otata publish` and `otata doctor --fix` bring it back. Without autostart, the only server is a foreground `otata serve`, which lives as long as its terminal.

There's one unit per user, tied to the `OTATA_ROOT` and `OTATA_PORT` it was installed with. A shell with a different root sees autostart off, can't stop that unit, and is refused `autostart on`.

To leave a machine serving unattended, see [otata via SSH](otata-via-ssh.md).

## What's in `~/.otata`?

| Path | Holds |
| --- | --- |
| `public/` | The only tree ever served: the index, and a directory per app with its page, manifest, icon and payload |
| `state/` | App records and build markers, plus `tsnet/` with the private Tailscale identity and certificate cache |
| `build/` | Archives, exports and the build log, per app |
| `tmp/` | Where publishing stages a file before renaming it into `public/`, so a client can never fetch a half-written one |
| `bin/` | A copy of the otata binary, present only when the installed one sits where the service manager cannot run it: a TCC-protected directory on a Mac, a path systemd refuses on Linux |
| `config.json` | The port, the selected transport, the Tailscale hostname and the manual proxy settings |
| `server.log` | The background server's own log and its access log |

## Why not TestFlight or Firebase App Distribution?

otata is much faster than TestFlight (even Internal Testing!) and Firebase App Distribution. Those are distribution channels that archive, upload to Apple's or Google's servers, and process every build. The archive alone recompiles every target. On a real nine-target app it took ~100s, against ~4s for otata's default incremental build. YMMV, so benchmarking it yourself is encouraged!
