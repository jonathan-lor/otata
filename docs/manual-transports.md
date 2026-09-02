# Serving over your own proxy

This is a practical setup guide for three verified setups for the `manual` transport:

- **Cloudflare quick tunnel** — works from anywhere; the URL changes on every start.
- **Caddy on a domain you own** — a stable URL on your own name; works only
  where the phone can reach the Mac's network.
- **ngrok** — works from anywhere; free-tier URLs change per start.

Every method ends with a URL served over HTTPS with a publicly trusted
certificate, forwarding to otata's server on `127.0.0.1:8787`. iOS refuses
anything less for an `itms-services://` install.

## The one thing otata needs to know

`otata transport use manual` needs one fact about your proxy: what it does
with the base URL's path.

- **Strips it before forwarding** — nginx with a trailing slash on
  `proxy_pass`, Caddy's `handle_path`: the default; say nothing.
- **Forwards it unchanged** — a tunnel, Caddy's `handle`, nginx without the
  slash: pass `--keep-prefix`, and otata's server strips it instead.

The prefix *is* the base URL's path. A base URL with no path (a tunnel
hostname used bare) sidesteps the question.

## Visibility

`--visibility public` is refused: no access guard ships. What
`--visibility private` means depends on the route. On the Caddy route it is a
fact (the name resolves to a private address), while a tunnel URL is 
reachable from the whole internet. On any publicly reachable route, the
unguessable URL is all that stands between your builds and the internet, and
the index enumerates every published app under it. A random tunnel hostname
is unguessable on its own, and a stable domain of yours is not. DNS and
certificate logs publish it so there the secret must be a random path
segment in the base URL (`--base-url https://host/<random>/`).

## Cloudflare quick tunnel

```sh
brew install cloudflared
cloudflared tunnel --url http://127.0.0.1:8787
```

It prints a `https://<random>.trycloudflare.com` URL. In another shell:

```sh
otata transport use manual \
  --base-url https://<random>.trycloudflare.com/otata \
  --keep-prefix --visibility private
otata doctor
```

- The tunnel forwards paths unchanged, hence `--keep-prefix`.
- The URL changes on every start. Re-run `transport use` with the new one:
  manifests and pages embed the base URL, and the command regenerates them all.
- Works from any network; loading the index over LTE proves it.

## Your own domain and Caddy (same network)

The A record points at a private address, so the URL works only where the
phone can reach the Mac's network: the same Wi-Fi. Pointing the record at the
Mac's Tailscale IP (`tailscale ip -4`) instead, with Tailscale running on the
phone, should extend that to anywhere (unverified).

1. **DNS.** Add an A record for a subdomain — `otata.example.com` — pointing
   at the Mac's LAN address (`ipconfig getifaddr en0`). Issuing a certificate
   records the hostname in Certificate Transparency logs, so anyone can learn
   the name exists; they still cannot connect to it.

2. **Certificate.** Let's Encrypt cannot reach a LAN address, so issuance goes
   through a DNS-01 challenge:

   ```sh
   brew install certbot caddy
   certbot certonly --manual --preferred-challenges dns \
     --config-dir ~/.otata-certs --work-dir ~/.otata-certs --logs-dir ~/.otata-certs \
     -d otata.example.com
   ```

   certbot prints a TXT record. Add it where your DNS lives, confirm the
   authoritative servers answer
   (`dig TXT _acme-challenge.otata.example.com`), then let certbot continue.
   The `--config-dir` keeps everything out of `/etc` so nothing needs sudo.
   The certificate lasts 90 days and renews by the same dance. Automating
   renewal needs a DNS provider API (a certbot plugin, or a Caddy build with
   your provider's module).

3. **Caddy.**

   ```
   otata.example.com:8443 {
       tls /path/to/fullchain.pem /path/to/privkey.pem
       redir /otata /otata/ 308
       handle_path /otata/* {
           reverse_proxy 127.0.0.1:8787
       }
   }
   ```

   - **`:8443`, not `:443`.** The Tailscale app holds a wildcard listener on
     443, and macOS allows an unprivileged bind of a low port only on the
     wildcard address. iOS installs from a non-standard HTTPS port without
     complaint.
   - **The `redir` is required.** `handle_path /otata/*` does not match a
     bare `/otata` — which hand-typed URLs produce — and Caddy answers an
     unmatched request with an empty 200: a white screen with no error.

   ```sh
   caddy run --config Caddyfile
   otata transport use manual \
     --base-url https://otata.example.com:8443/otata --visibility private
   otata doctor
   ```

   No `--keep-prefix`: `handle_path` strips.

4. **Troubleshooting.** If the index will not load on the phone while
   `doctor` is healthy:

   - Router "rebind protection" filters private addresses out of public DNS
     answers: the name resolves on the Mac and not on the phone. The fix is
     a router setting or a different resolver.
   - iCloud Private Relay routes Safari through Apple's network, which has no
     path to your LAN; turn it off to test.
   - Guest and IoT SSIDs commonly isolate clients from each other, which
     looks like a hang rather than an error.

## ngrok

```sh
brew install ngrok
ngrok config add-authtoken <token>     # dashboard.ngrok.com
ngrok http 8787
```

```sh
otata transport use manual \
  --base-url https://<assigned>.ngrok-free.dev/otata \
  --keep-prefix --visibility private
otata doctor
```

- The free tier will show an initial page ("You are about to visit…") once per browser.
- Free-tier URLs change per start unless the dashboard has assigned you a
  static domain, and bandwidth is capped.

## Switching back

```sh
otata transport use tailscale
```

Everything regenerates against the tailnet URL. Switching is validated before
the previous transport is torn down, and teardown removes only what otata added.
Any other route your proxy or tunnel created never gets touched.
