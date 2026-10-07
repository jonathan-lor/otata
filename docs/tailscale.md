# Tailscale

otata's `tailscale` transport joins your tailnet directly as a separate device via [tsnet](https://tailscale.com/docs/features/tsnet), without using your machine's Tailscale installation.
It serves at `https://<node>.<tailnet>.ts.net/`, with no configurable path prefix.
Your phone still needs tailnet access, and the tailnet needs MagicDNS and HTTPS certificates enabled.
For an existing installation, read the upgrade steps below before installing.

## Setup

```sh
otata autostart on
otata transport login
```

Open the login URL and enroll the device in your phone's tailnet. Approve it
in the Tailscale admin console if required. For foreground use, run
`otata serve` in another terminal instead of `autostart on`.

```sh
otata status
otata doctor
otata publish --artifact /path/to/App.ipa
```

The hostname defaults to `otata`. To choose another name, run
`otata transport use tailscale --hostname otata-builds` before starting it.
Status reports the assigned URL. First certificate issuance can take a minute;
wait for `transport.ready` before downloading. A successful selection or login
command alone doesn't necessarily mean HTTPS is ready.

## Identity and switching

Tailnet ACLs must allow your phone to reach the embedded node on TCP 443.
The host device's tags and approval do not transfer to this separate device.
otata never enables Funnel.

Credentials persist in `OTATA_ROOT/state/tsnet/`, outside the served tree.
Restarts and transport switches retain them. Do not copy this directory to
concurrently running installations. Deleting it requires enrollment again.
For unattended enrollment, provide `TS_AUTHKEY` to the serving process through
its environment.

To use an existing private HTTPS proxy, select `manual` with `--base-url`;
see [manual transports](manual-transports.md). A managed server restarts when
the transport or hostname changes; a foreground server asks you to restart it.

## Upgrading from a prefixed tsnet release

**This is a breaking change:** Tailscale always serves at `/`. The old
`/otata` prefix and custom Tailscale prefixes are no longer supported or
redirected. `serve_path` in saved configuration and `OTATA_PATH` in your shell
or an old service definition are ignored. Manual proxy paths are unchanged.

Let running publishes and downloads finish, then stop otata before replacing
the binary. A publish running the old binary still holds its old URLs.

```sh
otata stop
```

Install the new release, then refresh the service and check the new URL:

```sh
otata autostart on
otata status
otata doctor
```

Use the same `OTATA_ROOT` and `OTATA_PORT` if you configured overrides.
The embedded node keeps its identity and enrollment in `state/tsnet/`.
Published payloads are retained, and pages and manifests regenerate at startup
before HTTPS is marked ready. No rebuild or app reinstall is needed.
Replace bookmarks and shared links with the URLs reported by otata.

For foreground serving, stop the old `otata serve` with Ctrl-C and start the
new binary in that terminal instead of using `autostart on`.

## Upgrading from the host integration

Otata no longer detects or cleans up the host Tailscale integration. Old
configurations selecting `tailscale` now select the embedded node directly.
It requires its own enrollment and uses a different hostname from the host.

Before installing the new release, finish running publishes and stop the old
server as above. Use the **host's** Tailscale CLI to inspect its Serve routes:

```sh
tailscale serve status --json
```

Find the handler that points to otata's loopback address, normally
`http://127.0.0.1:8787`, and confirm its HTTPS port and path. Remove only that
handler. For the old background route on port 443 at `/otata`:

```sh
tailscale serve --bg --https=443 --set-path=/otata off
```

Use your original port and path if they differ. If no otata handler exists,
there is nothing to remove. If the handler points elsewhere, leave it alone.
Do not use `tailscale serve reset`, which removes unrelated routes too.
See [Tailscale's Serve reference](https://tailscale.com/docs/reference/tailscale-cli/serve).
An old route left in place may still expose otata through the host's URL and
access policy because it forwards to the loopback server.

Install the release, then run:

```sh
otata autostart on
otata transport login
otata status
otata doctor
```

Complete browser enrollment and any required device approval, then wait for
HTTPS readiness and replace saved links with the new URLs. Keep the same
`OTATA_ROOT` to retain published builds; pages and manifests regenerate without
rebuilding payloads. Keep any host Tailscale installation used for SSH or other
services. Otata never invokes the host CLI.

## Troubleshooting

`otata status --json` reports `transport.state`, `transport.detail`, and
`transport.auth_url` when enrollment is needed.

| State | Action |
| --- | --- |
| `stopped` | Start the server, or restart it if its configuration differs |
| `starting`, `connecting` | Check network access and server logs if this persists |
| `needs_login` | Run `otata transport login` and open its URL |
| `needs_approval` | Approve the device in the admin console |
| `dns_disabled`, `https_disabled` | Enable MagicDNS and HTTPS certificates in tailnet DNS settings |
| `certificate_pending` | Wait for certificate issuance |
| `error` | Read the detail; preparation retries every minute, or immediately with `transport login` |
| `ready` | Run `doctor` and open the install page on your phone |

`ready` means the HTTPS listener is prepared. It can remain true during a
network outage while `detail` reports a health warning. Doctor probes through
the embedded node; it cannot verify your phone's ACL access or VPN connection.
Logs appear in `OTATA_ROOT/server.log` for managed serving, or in the foreground
terminal.
