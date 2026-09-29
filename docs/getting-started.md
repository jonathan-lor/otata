# Getting Started

This guide sets up otata with Tailscale, on a Mac or a Linux machine, for iOS
and Android. Serving through your own HTTPS proxy instead of Tailscale is covered in [manual transports](manual-transports.md).

**As of otata v0.4.0, Tailscale now runs inside otata. Existing host-backed installations must [migrate once](tailscale.md#upgrading-from-the-host-integration).**

## 1. Tailscale on your phone

Install Tailscale [on your iPhone](https://tailscale.com/docs/install/ios) or
[on your Android phone](https://tailscale.com/docs/install/android) and sign in
to the tailnet you will enroll otata in.

## 2. Turn on HTTPS certificates

Open the [Tailscale admin console](https://login.tailscale.com/admin/dns), go to **DNS**,
and enable **HTTPS Certificates**. A new tailnet has them off, and
otata needs them to serve trusted HTTPS. The same page has **MagicDNS**, which
HTTPS certificates require, so turn that on too if it isn't already.

## 3. Install otata

On Mac:

```sh
brew install --cask jonathan-lor/tap/otata
```

On Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/jonathan-lor/otata/main/install.sh | sh
```

Or, on either:

```sh
go install github.com/jonathan-lor/otata@latest
```

## 4. Enroll otata

```sh
otata autostart on
otata transport login
```

Open the printed login URL and enroll otata in the same tailnet as your phone.
Approve the device if required. Run `otata status` and wait since
first certificate issuance can take a minute. `otata doctor` will verify the
served URLs. To customize the device name before starting, use
`otata transport use tailscale --hostname otata-builds`.

`autostart on` runs the file server under launchd on a Mac and under your
own systemd on Linux, so it starts at login and comes back if it exits.
Without it the server only lives as long as a foreground `otata serve`.

On Linux, a user's systemd runs only while that user is logged in, an SSH
session included, so a machine that should serve after a reboot with nobody
logged in, or that you only ever reach over SSH, needs lingering turned on
once:

```sh
sudo loginctl enable-linger $USER
```

## 5. iOS: sign the app with a paid team

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

## 6. iOS: Developer Mode on your iPhone

Settings -> Privacy & Security -> **Developer Mode**. 
If you don't see the option to enable Developer Mode, connect your phone to the Mac once.
iOS only shows it after connecting to a Mac running Xcode.

## 7. Android: a JDK and the SDK

The project's own Gradle wrapper does the building, and it needs a JDK 17 or
newer (`java -version` says which you have) and the Android SDK. Set
`ANDROID_HOME` to the SDK's directory, or let the project's `local.properties`
name it in `sdk.dir`, which Android Studio writes for you. The SDK's
build-tools are also what read the finished APK, so a machine with neither
cannot publish an Android build at all.

Gradle downloads whatever platform and build-tools the project asks for, once
the SDK's licenses are accepted: `sdkmanager --licenses`, from
`cmdline-tools/latest/bin` under the SDK, or Android Studio's SDK Manager.

A debug build is signed by the debug keystore every machine has, so
`--config Debug` publishes from a fresh project with no setup. The default
`Release` needs a `signingConfig` in the module's build script, because
Android will not install an unsigned APK; until one exists, otata refuses a
release build with `needs_setup` before running it.

## 8. Publish

```sh
cd ~/path/to/MyApp
otata publish --platform ios       # on a Mac
otata publish --platform android   # on a Mac or Linux; --config Debug on a fresh project
```

Two URLs will be printed: one for the app you just ran `otata publish` for, and a root URL for every app you've published.
Open up either on the phone and tap **Install**.

On an iPhone, iOS installs from there. On an Android phone, the browser
downloads the APK; tap the download if nothing opens on its own.

And that's it! From here, it's just `otata publish --platform ios` or
`otata publish --platform android` per build.

As a reminder, otata includes an [agent skill](../skills/otata/SKILL.md).

## When something goes wrong

```sh
otata doctor --fix
```

It'll repair what it can, then verify every URL and name anything still broken.
[What exactly does doctor check?](FAQ.md#what-exactly-does-doctor-check) goes into more depth on `otata doctor`.
