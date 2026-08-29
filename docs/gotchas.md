# Gotchas and troubleshooting

Some common gotchas outside of otata's scope:

- **Developer Mode must be on**: Settings -> Privacy & Security -> Developer Mode.
- **A differently-signed build cannot replace an installed one.** Delete the
  existing copy first.
- **A React Native `--config Debug` build carries this Mac's LAN address**,
  written beside the bundle so the app can find Metro. The JS is bundled in,
  so the app runs, but the reload loop only works on that network and the
  build is nearly three times the size.
- **Published payloads are larger than exported ones.** The default builder
  packages the signed build products directly, so the binary keeps its symbol
  table — around 40% on a Swift-heavy app. `--builder archive` produces the
  stripped payload, at the cost of a full rebuild on every publish.
- **`--builder archive` alternating configs costs a full rebuild**: archives
  share one `ArchiveIntermediates` tree per scheme, and rebuild it every run
  regardless. The default builder keeps separate incremental state per
  configuration.
- **`tailscale` is usually just a shell alias** to the binary inside the app
  bundle, so it vanishes inside scripts. otata resolves it explicitly.
- **The signing team comes from the project, and a template may already have
  chosen it.** otata passes none of its own. `flutter create` embeds a
  `DEVELOPMENT_TEAM`, and with no terminal attached it silently takes the
  first identity `security find-identity` reports — often the free personal
  team. Settle it once with `flutter config --select-ios-signing-settings`,
  per project in Xcode's Signing & Capabilities, or `TEAM_ID` for a KMP
  template.
- **`brew upgrade` stops the launch agent.** Homebrew runs the old cask's
  uninstall stanzas during an upgrade, which boots the agent out. It returns
  at next login, or immediately with `otata start`.
- **Do not keep the otata binary in `~/Documents`, `~/Desktop` or `~/Downloads`.**
  launchd cannot read a binary inside a TCC-protected directory, and it does
  not fail cleanly. The process starts and hangs in dyld while `launchctl
  print` reports the job as running. `otata autostart on` detects this by
  waiting for an actual port bind, falls back to a staged copy, and reports
  when that copy has drifted from the installed binary.

## Troubleshooting

```sh
otata doctor                        # verifies every URL; --fix repairs first
tail -f ~/.otata/server.log         # server and access log
tail -50 ~/.otata/build/<slug>/xcodebuild.log
tailscale serve status              # confirm the path is wired
```

A `502` from the tailnet on a URL under the mount path means the file server
is down (`otata doctor --fix`). Tailscale also answers `502` for a path no
handler serves at all (`/style.css` where the mount is `/otata`), so check
which URL failed before restarting anything. Anything else means the
transport is down. otata only ever adds its own `tailscale serve` path;
other handlers on `:443` are left alone.

### Proving an install actually happened

A `200` in `server.log` proves bytes were sent, not that iOS installed them.
The bundle container changes on every reinstall, so compare it before and
after:

```sh
xcrun devicectl list devices          # find the identifier
xcrun devicectl device info apps --device <id> --json-output before.json
# ... install from the phone ...
xcrun devicectl device info apps --device <id> --json-output after.json
```

A changed container UUID in the `url` field for your bundle id means it
genuinely reinstalled. The device must be on the same local network;
`devicectl` does not work over the tailnet.