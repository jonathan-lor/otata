# Getting Started

This setup currently covers the otata setup for iOS and macOS using Tailscale. Android and Linux/Windows/WSL steps will be added alongside support for them. 

## 1. Tailscale on the Mac

iOS only installs from a URL served over HTTPS with a publicly trusted
certificate, so your iPhone can't just fetch from your Mac's IP address.
Tailscale solves that by putting your devices on your own private network and
issuing a real certificate for a name only your devices can reach.
It is completely free for personal use.

Install Tailscale [on your Mac](https://tailscale.com/docs/install/mac), then sign in.

## 2. Tailscale on the iPhone

Install Tailscale [on your iPhone](https://tailscale.com/docs/install/ios) and sign in **with the same account**.

It's very important that you don't skip this step. otata will work on the Mac, and then the provided link
won't load on your phone, because your phone won't be on the same network as the Mac.

## 3. Turn on HTTPS certificates

Open the [Tailscale admin console](https://login.tailscale.com/admin/dns), go to **DNS**,
and enable **HTTPS Certificates**. A new tailnet has them off, and
`tailscale serve` can't run without them. The same page has **MagicDNS**, which
HTTPS certificates require, so turn that on too if it isn't already.

## 4. Install otata on the Mac

```sh
brew install --cask jonathan-lor/tap/otata
```

Or 

```sh
go install github.com/jonathan-lor/otata@latest
```

## 5. Point otata at Tailscale

```sh
otata transport use tailscale
otata autostart on
```

`otata transport use` verifies the whole path before saving anything, so if a step
above was missed it'll name that step exactly.

`autostart on` runs the file server under launchd, so it starts at login and
comes back if it exits. Without it the server only lives as long as a foreground `otata serve`.

## 6. Sign the app with a paid team

Open the project in Xcode once, and under **Signing & Capabilities** select your
team. This has to be a paid Apple Developer account ($99/year). iOS refuses to
install a build signed by a free personal team over the air, and otata refuses
to publish one as well instead of giving you a link that won't work.

Connect the phone to the Mac once and let Xcode register it to the team.
`otata publish` passes `-allowProvisioningUpdates`, so xcodebuild can create and
update the provisioning profile on its own from then on, but it can only do that
for a device the team already knows about.

While you're here, answer the keychain prompt on the first local build with **Always Allow**.
`codesign` blocks on that dialog, and you probably won't be there to click it during a remote publish.

## 7. Developer Mode on your iPhone

Settings -> Privacy & Security -> **Developer Mode**. 
If you don't see the option to enable Developer Mode, connect your phone to the Mac once.
iOS only shows it after connecting to a Mac running Xcode.

## 8. Publish

```sh
cd ~/path/to/MyApp
otata publish --platform ios
```

Two URLs will be printed: one for the app you just ran `otata publish` for, and a root URL for every app you've published.
Open up either on the phone and tap **Install**.

And that's it! From here, it's just `otata publish --platform ios` per build.

As a reminder, otata includes an [agent skill](skills/otata/SKILL.md).

## When something goes wrong

```sh
otata doctor --fix
```

It'll repair what it can, then verify every URL and name anything still broken.
[What exactly does doctor check?](FAQ.md#what-exactly-does-doctor-check) goes into more depth on `otata doctor`.
