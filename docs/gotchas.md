# Gotchas and troubleshooting

## Gotchas

These are some surprises you might hit.

- **Developer Mode must be on** for iOS: Settings -> Privacy & Security -> Developer Mode.
- **A differently-signed build cannot replace an installed one** on either
  platform. Delete the existing copy first. On Android that includes debug
  builds from two computers: each generates its own debug keystore, so copy
  `~/.android/debug.keystore` between them if both publish the same app.
- **A React Native `--config Debug` build carries the Mac's LAN address**,
  written beside the bundle so the app can find Metro. The JS is bundled in,
  so the app runs, but the reload loop only works on that network and the
  build is nearly three times the size.
- **Published payloads are larger than exported ones.** The default builder
  packages the signed build products directly, so the binary keeps its symbol
  table — around 40% on a Swift-heavy app. `--builder archive` produces the
  stripped payload, at the cost of a full rebuild on every publish.
- **On a Mac, `tailscale` is usually just a shell alias** to the binary inside
  the app bundle, so it vanishes inside scripts. otata resolves it explicitly.
- **On Linux, `tailscale serve` needs your user to be the operator**:
  `sudo tailscale set --operator=$USER`, once. `otata transport use` cannot
  detect a missing operator; the first publish fails with `transport_down`.
- **On Linux, the server stops when you log out**, unless lingering is on:
  `sudo loginctl enable-linger $USER`, once. That also brings it back after a
  reboot before anyone logs in.
- **The signing team comes from the project, and a template may already have
  chosen it.** otata passes none of its own. `flutter create` embeds a
  `DEVELOPMENT_TEAM`, and with no terminal attached it silently takes the
  first identity `security find-identity` reports — often the free personal
  team. Settle it once with `flutter config --select-ios-signing-settings`,
  per project in Xcode's Signing & Capabilities, or `TEAM_ID` for a KMP
  template.
- **Selecting or publishing through Tailscale is refused while Funnel is on for `:443`.** Funnel is
  granted per listener instead of per path, so anything funnelled there makes
  every handler on that port reachable from the whole internet.
  No access guard ships yet, so otata refuses instead of serving your
  builds publicly. Turn Funnel off for that listener, or serve through your own proxy.
- **`brew upgrade` stops the launch agent.** Homebrew runs the old cask's
  uninstall stanzas during an upgrade, which boots the agent out. It returns
  at next login, or immediately with `otata start`.
- **Do not keep the otata binary in `~/Documents`, `~/Desktop` or `~/Downloads` on macOS.**
  launchd cannot read a binary inside a TCC-protected directory, and it does
  not fail cleanly. The process starts and hangs in dyld while `launchctl print` reports the job as running. 
  `otata autostart on` detects this by waiting for an actual port bind, falls back to a staged copy, and reports when that copy has drifted from the installed binary.

## Troubleshooting

```sh
otata doctor                        # verifies every URL; --fix repairs first
tail -f ~/.otata/server.log         # server and access log
tail -50 ~/.otata/build/<slug>/xcodebuild.log   # gradle.log for an Android build
tailscale serve status              # confirm the path is wired
systemctl --user status otata       # Linux: the unit's state and last lines
journalctl --user -u otata -n 50    # Linux: what systemd saw of it
```

A `502` from the tailnet on a URL under the mount path means the file server
is down (`otata doctor --fix`). Tailscale also answers `502` for a path no
handler serves at all (`/style.css` where the mount is `/otata`), so check
which URL failed before restarting anything. Anything else means the
transport is down. otata only ever adds its own `tailscale serve` path;
other handlers on `:443` are left alone.
