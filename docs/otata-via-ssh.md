# otata via SSH

otata can also be used over SSH! The likely use case for this is a Mac being used exclusively for building iOS apps, while being orchestrated by an agent on another host.
How your source code gets there is up to you because otata just reads whatever working tree it's pointed at. 
The examples write `mac` for the builder's name in `~/.ssh/config`.

```
agent's machine      edits source; runs no otata
  └── ssh
        └── mac      otata publish; the server; the store
              └── transport (tailscale serve / your proxy)
                    └── phone taps Install
```

The phone should talk directly to the mac in this setup, so the machine running the agent needs no otata install. The Mac keeps the same transport as when publishing locally.

## Publishing

```sh
ssh mac 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --platform ios --json'
ssh box 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --platform android --json'
```

Spell the binary's full path. A non-interactive shell over SSH reads few of
the files where installers put PATH edits: zsh on a Mac reads only
`~/.zshenv`, not `~/.zprofile` or `~/.zshrc`, and a Linux `~/.bashrc`
usually returns before its PATH lines when the shell is not interactive.
`command not found` over SSH while `otata` works in a terminal is this, not
a broken install.

An SSH connection that dies mid-build will kill the publish. On an unstable connection, run otata under `tmux` or `nohup`.

Each publish records the commit, branch and dirty flag of the working tree
*on the builder*, however the source got there. Keep the project at one path
there. The same project at a new location is refused with `slug_conflict`.

## Initial setup on the Mac

- `otata transport use tailscale` (or `manual`), and `otata autostart on`.
- Publish once locally after any change of signing identity, answering the
  keychain prompt with **Always Allow**. `codesign` blocks on that dialog,
  so the first remote publish under a new identity will hang mid-build if you don't do this.
- Tick **Launch Tailscale at login**, `caffeinate -s` or disable sleep before leaving, and
  consider disabling automatic macOS installs. That reboot lands at the
  FileVault screen, where nothing runs until the password is entered.

### Headless

A Mac being used only over SSH needs four substitutions:

- **Enable automatic login** (requires FileVault off). The launch agent
  lives in launchd's `gui` domain, which exists only while a user is logged
  into the console, so without a console session `otata autostart on`
  cannot load it. Auto-login also unlocks the login keychain that signing
  reads. The no-console alternative is `otata serve` under `tmux`.
- `sudo pmset -a sleep 0` in place of `caffeinate`: it persists across
  reboots with no terminal to hold open.
- The keychain prompt appears on the console, where nobody is there to
  click it, so a publish that needs it hangs mid-build. Either answer it
  once over Screen Sharing, or pre-authorize codesign so it never appears:

  ```sh
  security unlock-keychain ~/Library/Keychains/login.keychain-db
  security set-key-partition-list -S apple-tool:,apple: -s \
    -k <login password> ~/Library/Keychains/login.keychain-db
  ```

- The Tailscale menu-bar app runs inside the login session. To serve with
  no session at all, `brew install tailscale` plus `sudo brew services
  start tailscale` runs it as a system daemon. otata resolves either CLI.

A project under `~/Documents`, `~/Desktop` or `~/Downloads` is unreadable
over SSH until remote users are allowed full disk access (System Settings >
General > Sharing > Remote Login).

## On Linux

A Linux builder has no console session to keep alive and no keychain to
unlock, and needs two things instead:

- **Lingering.** A user's systemd, and the otata unit under it, runs only
  while that user has a session, an SSH one included, and stops at the last
  logout. `sudo loginctl enable-linger $USER` once keeps it running with no
  session, and brings the server back after a reboot before anyone logs in.
- **The Tailscale operator.** `tailscale serve` is refused to anyone but
  root or the operator, so `sudo tailscale set --operator=$USER` once.
  tailscaled itself is a system service, up whenever the machine is, so
  there is no login-item equivalent to tick.

An Android build is signed by a keystore file, the debug one generated on
first use, so nothing prompts during a remote publish. The SDK and JDK the
build needs are environment, not session: `ANDROID_HOME` has to be set for
the SSH command's shell, or written as `sdk.dir` in the project's
`local.properties`, which does not depend on the shell at all.
