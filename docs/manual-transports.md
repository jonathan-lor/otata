# Serving over your own proxy

Three setups have been verified with the `manual` transport:

- **Cloudflare quick tunnel**: works from anywhere, but the URL changes on every start.
- **Caddy on a domain you own**: a stable URL on your own name, but only where the phone can reach the Mac's network.
- **ngrok**: works from anywhere, but free-tier URLs change on every start.

Each ends with an HTTPS URL with a publicly trusted certificate, forwarding to otata's server on `127.0.0.1:8787`. iOS won't install over anything less.

## Before you start

otata needs to know one thing about your proxy: what it does with the base URL's path.

- **It strips the path before forwarding**, like nginx with a trailing slash on `proxy_pass` or Caddy's `handle_path`. This is the default, so pass nothing.
- **It forwards the path unchanged**, like a tunnel, Caddy's `handle`, or nginx without the slash. Pass `--keep-prefix` and otata's server strips it instead.

A base URL with no path, like a bare tunnel hostname, works either way.

otata doesn't check who can reach your proxy and has no access guard, so only use routes you consider private. The Caddy setup below is private because its name resolves to a LAN address. A tunnel URL is reachable from the whole internet, so the URL is the only secret, and the index lists every published app under it. A random tunnel hostname is hard to guess. Your own domain isn't, because DNS and certificate logs publish it, so put a random path segment in the base URL (`--base-url https://host/<random>/`).

## Cloudflare quick tunnel

```sh
brew install cloudflared
cloudflared tunnel --url http://127.0.0.1:8787
```

It prints a `https://<random>.trycloudflare.com` URL. In another shell:

```sh
otata transport use manual \
  --base-url https://<random>.trycloudflare.com/otata \
  --keep-prefix
otata doctor
```

- The tunnel forwards paths unchanged, hence `--keep-prefix`.
- The URL changes on every start. Run `transport use` again with the new one, which regenerates every page and manifest.
- It works from any network, LTE included.

## Your own domain and Caddy (same network)

The name points at the Mac's LAN address, so the URL only works where the phone can reach that network, usually the same Wi-Fi.

1. **DNS.** Add an A record for a subdomain, like `otata.example.com`, pointing at the Mac's LAN address (`ipconfig getifaddr en0`).

2. **Certificate.** Get one for that name. Let's Encrypt can't reach a LAN address, so it has to come through a DNS-01 challenge.

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

   - **Port 8443** avoids needing root for 443 and clashing with another HTTPS server on the Mac. iOS is fine with a non-standard HTTPS port.
   - **The `redir` is required.** `handle_path /otata/*` doesn't match a bare `/otata`, which hand-typed URLs produce, and Caddy answers an unmatched request with an empty 200: a blank page and no error.

   ```sh
   caddy run --config Caddyfile
   otata transport use manual \
     --base-url https://otata.example.com:8443/otata
   otata doctor
   ```

   No `--keep-prefix`, since `handle_path` strips the path.

4. **Troubleshooting.** If the index won't load on the phone while `doctor` is healthy:

   - Router "rebind protection" drops private addresses from public DNS answers, so the name resolves on the Mac but not on the phone. Change the router setting or use another resolver.
   - iCloud Private Relay sends Safari through Apple's network, which can't reach your LAN. Turn it off to test.
   - Guest and IoT networks often isolate devices from each other, which looks like a hang rather than an error.

## ngrok

```sh
brew install ngrok
ngrok config add-authtoken <token>     # dashboard.ngrok.com
ngrok http 8787
```

```sh
otata transport use manual \
  --base-url https://<assigned>.ngrok-free.dev/otata \
  --keep-prefix
otata doctor
```

- The free tier shows an interstitial page ("You are about to visit…") once per browser.
- Free-tier URLs change on every start unless the dashboard has given you a static domain, and bandwidth is capped.

## Switching back

```sh
otata transport use tailscale
```

If otata was never enrolled, start the server and run `otata transport login`. Once it's ready, pages and manifests regenerate with the tailnet URL. Your proxy or tunnel is left as it is.
