# Tailscale

otata's `tailscale` transport is an embedded Tailscale node ([tsnet](https://tailscale.com/docs/features/tsnet)) that joins your tailnet as its own device, separate from any Tailscale installed on the machine. It serves at `https://<hostname>.<tailnet>.ts.net/`. Your phone needs to be on the tailnet, and the tailnet needs MagicDNS and HTTPS certificates turned on. [Getting started](getting-started.md) walks through the setup.

## The otata device

- Tailnet ACLs must let your phone reach the device on TCP 443. The host machine's tags and approval don't carry over to it.
- otata never turns on Funnel.
- Its credentials live in `OTATA_ROOT/state/tsnet/`, outside the served tree, and survive restarts and transport switches. Deleting that directory means enrolling again. Don't copy it to another install that runs at the same time.
- For unattended enrollment, give the serving process `TS_AUTHKEY` in its environment.
- Changing the transport or the hostname restarts a managed server, or stops a foreground `otata serve` so you can start it again.

To serve through your own HTTPS proxy instead, see [manual transports](manual-transports.md).

## Upgrading

0.4.0 replaced the host Tailscale integration with the embedded node, and 0.5.0 moved it to the node's root URL. Both change your install URLs. Published builds are kept and their pages regenerate, so nothing needs rebuilding or reinstalling.

1. Let running publishes and downloads finish, then stop the server with `otata stop`.
2. **Coming from before 0.4.0**, remove otata's old route from the host's Tailscale [Serve](https://tailscale.com/docs/reference/tailscale-cli/serve) config. Find the handler pointing at otata's loopback port, normally `http://127.0.0.1:8787`, in `tailscale serve status --json` and turn off only that one. For the default route:

   ```sh
   tailscale serve --bg --https=443 --set-path=/otata off
   ```

   Use your own port and path if they differ. Don't run `tailscale serve reset`, which removes unrelated routes too. A route left in place keeps exposing otata through the host's URL. otata never touches the host's Tailscale, so keep it if you use it for SSH or anything else.
3. Install the new release, then start it and check the new URL:

   ```sh
   otata autostart on
   otata transport login   # coming from before 0.4.0: enroll (and approve) the new device
   otata status
   otata doctor
   ```

   Keep any `OTATA_ROOT` and `OTATA_PORT` overrides you had. With a foreground server, restart `otata serve` instead of running `autostart on`.
4. Replace bookmarks and shared links with the URLs `otata status` reports.

`serve_path` in an old config and `OTATA_PATH` in your shell or an old service definition are ignored. Manual proxy URLs don't change.

## Troubleshooting

`otata status --json` reports `transport.state` and `transport.detail`, plus `transport.auth_url` when enrollment is needed.

| State | Action |
| --- | --- |
| `stopped` | Start the server, or restart it if its configuration differs |
| `starting`, `connecting` | Check network access and server logs if this persists |
| `needs_login` | Run `otata transport login` and open its URL |
| `needs_approval` | Approve the device in the admin console |
| `dns_disabled`, `https_disabled` | Enable MagicDNS and HTTPS certificates in tailnet DNS settings |
| `certificate_pending` | Wait for certificate issuance |
| `error` | Read the detail. Preparation retries every minute, or immediately with `transport login` |
| `ready` | Run `doctor` and open the install page on your phone |

`ready` means the HTTPS listener is up. It stays true through a network outage, with `detail` showing the health warning. `otata doctor` probes through the embedded node, so it can't check your phone's side. If doctor passes but the phone can't load the page, check that the phone is on the tailnet and that ACLs let it reach otata's device.

The server logs to `OTATA_ROOT/server.log` under autostart, or to the terminal running `otata serve`.
