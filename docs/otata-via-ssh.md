# otata via SSH

otata works over SSH, so an agent on one machine can publish from another. The likely setup is a Mac that only builds iOS apps, driven by an agent somewhere else. otata reads whatever working tree it's pointed at, so getting the source there is up to you. The examples use `mac` and `box` as host names from `~/.ssh/config`.

```
agent's machine      edits source; runs no otata
  └── ssh
        └── mac      otata publish; the server; the store
              └── transport (built-in Tailscale / your proxy)
                    └── phone taps Install
```

The phone talks to the build machine directly, so the agent's machine needs no otata install.

## Publishing

```sh
ssh mac 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --platform ios --json'
ssh box 'cd ~/path/to/MyApp && ~/.local/bin/otata publish --platform android --json'
```

- **Spell out the binary's full path.** A non-interactive SSH shell skips the files where installers add to PATH (zsh on a Mac reads only `~/.zshenv`, and a Linux `~/.bashrc` usually returns early). That's why `otata` can work in a terminal and be `command not found` over SSH.
- **A dropped connection kills the publish.** On a shaky link, run it under `tmux` or `nohup`.
- **Keep the project at one path on the build machine.** Each publish records the commit and branch of the tree there, and the same project at a new path is refused with `slug_conflict`.
- **An Android build needs the SDK in the command's environment.** Set `ANDROID_HOME` in the shell SSH gives you, or write `sdk.dir` into the project's `local.properties`, which doesn't depend on the shell at all. Signing uses a keystore file, so nothing prompts.

## Setting up the Mac

- Run `otata autostart on` and `otata transport login`, as in [getting started](getting-started.md).
- After any change of signing identity, publish once at the Mac and answer the keychain prompt with **Always Allow**. Otherwise `codesign` waits on that dialog and the remote publish hangs mid-build.
- Before leaving, run `caffeinate -s` or turn off sleep, and consider turning off automatic macOS updates. The reboot after one stops at the FileVault screen, where nothing runs until someone types the password.
- A project under `~/Documents`, `~/Desktop` or `~/Downloads` can't be read over SSH until Remote Login has full disk access (System Settings > General > Sharing > Remote Login).

### Headless

A Mac that's only ever used over SSH needs three changes:

- **Turn on automatic login**, which requires FileVault off. The launch agent can only load while a user is logged into the console, and auto-login also unlocks the keychain that signing reads. Without a console session, run `otata serve` under `tmux` instead of `autostart on`.
- **Use `sudo pmset -a sleep 0`** instead of `caffeinate`. It survives reboots with no terminal held open.
- **Deal with the keychain prompt**, which appears on a console nobody is watching. Answer it once over Screen Sharing, or pre-authorize codesign so it never appears:

  ```sh
  security unlock-keychain ~/Library/Keychains/login.keychain-db
  security set-key-partition-list -S apple-tool:,apple: -s \
    -k <login password> ~/Library/Keychains/login.keychain-db
  ```

otata only serves builds and doesn't give you SSH access. To reach the Mac over Tailscale, keep a regular Tailscale install on it for SSH.

## On Linux

A Linux build machine has no console session or keychain to worry about. It only needs [lingering](getting-started.md#3-enroll-otata), so the server keeps running with nobody logged in.
