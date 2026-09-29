# Tailscale

As of v0.4.0, otata's `tailscale` transport now joins your tailnet directly as a separate device via [tsnet](https://tailscale.com/docs/features/tsnet) instead of using your machine's Tailscale installation.
Your phone still needs tailnet access, and the tailnet needs MagicDNS and HTTPS certificates enabled.
If you were an existing user on the previous host-backed Tailscale implementation, you'll need to [migrate once](#upgrading-from-the-host-integration).

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

## Upgrading from the host integration

**This is a breaking change:** otata enrolls as a new device with a new URL.
Published builds are kept, and installed apps do not need reinstalling.
Old configurations report `migration_required` rather than silently switching.

After upgrading, run:

```sh
otata transport use tailscale
otata autostart on
otata transport login
otata doctor
```

The first command uses the host Tailscale CLI once to inspect Serve and remove
only the old path on port 443 if it still points to otata's configured loopback
port. Unrelated handlers are left alone. If inspection or removal fails, the
configuration stays unmigrated; fix the reported error and rerun the command.
Use the same `OTATA_ROOT`, `OTATA_PORT`, and `OTATA_PATH` as the old server if
you configured overrides; migration does not persist those overrides.

Complete browser enrollment, wait for HTTPS readiness, then replace bookmarks
and shared links with the URL in `otata status`. Tailnets with approval or
restricted ACLs may need an administrator to authorize the new device.
Pages and manifests regenerate automatically. Serving is interrupted during
migration; no rebuild is needed. The host CLI is not used afterward.

Foreground users run `otata serve` in a separate terminal instead of
`autostart on`. Keep any host Tailscale installation you use for SSH or other
applications; otata's embedded node serves builds, not host SSH.

## Troubleshooting

`otata status --json` reports `transport.state`, `transport.detail`, and
`transport.auth_url` when enrollment is needed.

| State | Action |
| --- | --- |
| `migration_required` | Follow the upgrade steps above |
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
