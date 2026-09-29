# Gotchas and troubleshooting

## Gotchas

For enrollment and connectivity, see [Tailscale troubleshooting](tailscale.md#troubleshooting).

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
- **otata is a separate Tailscale device.** The host's enrollment and ACL tags
  do not carry over. Approve otata and allow your phone to reach its TCP 443.
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
otata status --json                 # enrollment, certificate and readiness state
systemctl --user status otata       # Linux: the unit's state and last lines
journalctl --user -u otata -n 50    # Linux: what systemd saw of it
```

If doctor passes but the phone cannot open the page, check that the phone is
connected to the same tailnet and permitted to reach otata's device. Use the
URL from `otata status`; links from the removed host integration change after
[migration](tailscale.md#upgrading-from-the-host-integration).
