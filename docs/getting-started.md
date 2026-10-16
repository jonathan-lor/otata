# Getting Started

This guide sets up otata with Tailscale on a Mac or Linux machine, for iOS and Android. To serve through your own HTTPS proxy instead, see [manual transports](manual-transports.md).

## 1. Tailscale on your phone

Install Tailscale [on your iPhone](https://tailscale.com/docs/install/ios) or [on your Android phone](https://tailscale.com/docs/install/android) and sign in to the tailnet you will enroll otata in.

Then open the [Tailscale admin console](https://login.tailscale.com/admin/dns), go to **DNS**, and turn on **MagicDNS** and **HTTPS Certificates**. A new tailnet has HTTPS certificates off, and otata needs them to serve trusted HTTPS.

## 2. Install otata

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

## 3. Enroll otata

otata joins your tailnet as its own device, named `otata`. To pick another name, run `otata transport use tailscale --hostname <name>` first.

```sh
otata autostart on
otata transport login
```

Open the printed login URL, enroll otata in the same tailnet as your phone, and approve the device if your tailnet requires it. Then wait for `otata status` to report ready, since the first certificate can take a minute.

`autostart on` runs the server under launchd on a Mac or your user's systemd on Linux, so it comes back at login and after a crash.

On Linux, turn on lingering once as well. Otherwise the server stops when your last session ends, SSH included, and doesn't start after a reboot until you log in:

```sh
sudo loginctl enable-linger $USER
```

## 4. iOS: signing and Developer Mode

Open the project in Xcode once and select your team under **Signing & Capabilities**. It has to be a paid Apple Developer account ($99/year). otata refuses to publish a build signed by a free personal team, because iOS won't install it over the air.

Connect the phone to the Mac once so Xcode registers it with the team. After that, `otata publish` keeps the provisioning profile up to date on its own.

Do your first publish at the Mac and answer the keychain prompt with **Always Allow**. `codesign` waits on that dialog, and nobody will be there to click it during a remote publish.

On the phone, turn on Settings -> Privacy & Security -> **Developer Mode**. The option only appears once the phone has been connected to a Mac running Xcode.

## 5. Android: a JDK and the SDK

The project's own Gradle wrapper builds the app. It needs:

- **A JDK 17 or newer.** `java -version` says which you have.
- **The Android SDK.** Set `ANDROID_HOME` to its directory, or let `sdk.dir` in the project's `local.properties` name it (Android Studio writes that for you). Accept its licenses once with `sdkmanager --licenses`, from `cmdline-tools/latest/bin` under the SDK, or with Android Studio's SDK Manager. Gradle then downloads whatever else the project asks for.
- **A signing config for release builds.** Debug builds are signed with the machine's debug keystore, so `--config Debug` works on a fresh project. The default `Release` needs a `signingConfig` in the module's build script, because Android won't install an unsigned APK.

## 6. Publish

```sh
cd ~/path/to/MyApp
otata publish --platform ios       # on a Mac
otata publish --platform android   # on a Mac or Linux; --config Debug on a fresh project
```

otata prints two URLs: one for the app you just published and one listing every app. Open either on the phone and tap **Install**. An iPhone installs from there. On Android the browser downloads the APK, so tap the download if nothing opens on its own.

And that's it! From here, every build is just another `otata publish`. Your agent can run it for you with the [agent skill](../skills/otata/SKILL.md).

## When something goes wrong

```sh
otata doctor --fix
```

It repairs what it can, then verifies every URL and names anything still broken. See [what doctor checks](FAQ.md#what-exactly-does-doctor-check) for the details.
