# Streaming through Cloudflare and Nginx

For a request passing through `client -> Cloudflare -> Nginx -> CLIProxyAPI -> provider`,
CLIProxyAPI must send response bytes while it waits for the provider. Increasing a
local timeout or enabling TCP keepalive does not change Cloudflare's HTTP read timeout.

## CLIProxyAPI configuration

Merge the following into the existing v8 configuration:

```yaml
requests:
  passthrough-headers: false
  streaming:
    keepalive-seconds: 15
    send-keepalive-before-ft: true
```

`send-keepalive-before-ft` covers waiting for the upstream connection, upstream
response headers, and the first payload. The first heartbeat is sent after one
`keepalive-seconds` interval. A successful response or error arriving earlier is
delivered immediately, without waiting for a heartbeat.

- Before the first heartbeat or payload, an upstream failure retains its HTTP error
  status and response headers, such as `429` and `Retry-After`.
- Once a heartbeat is flushed, the response is committed as HTTP `200`. Later failures
  are sent in the endpoint's SSE error format. Clients must inspect those events.
- During normal streaming, heartbeats continue when the stream is idle. This setting
  does not impose a deadline on model inference.
- These settings apply to streaming requests; they do not convert a non-streaming JSON
  request into SSE. Clients must request streaming.
- With `passthrough-headers: true`, upstream headers that arrive after a heartbeat
  cannot be added to the already committed response.

Both keepalive options remain disabled by default. Existing deployments must enable
them explicitly. The experimental `prelude-keepalive` setting has been removed;
replace it with `send-keepalive-before-ft: true` and a positive `keepalive-seconds`.
There is no longer an immediate padded prelude or a second bootstrap heartbeat loop.

SSE responses include `Content-Type: text/event-stream`,
`Cache-Control: no-cache, no-transform`, and `X-Accel-Buffering: no`.
These headers accompany either the first real payload or the first heartbeat.

## Nginx configuration

Add this map once inside Nginx's `http` context, outside any `server` block. If an
equivalent WebSocket upgrade map already exists, reuse it:

```nginx
map $http_upgrade $cpa_connection_upgrade {
    default upgrade;
    ''      '';
}
```

Merge these directives into the existing location that proxies to CLIProxyAPI.
Keep the existing domain, TLS certificates, access controls, and other server settings.
Do not create a second competing `location /` block.

```nginx
location / {
    proxy_pass http://127.0.0.1:8317;
    proxy_http_version 1.1;

    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    # Preserve WebSocket support while leaving ordinary SSE requests unupgraded.
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $cpa_connection_upgrade;

    # Deliver each heartbeat immediately, without caching or compression buffering.
    proxy_buffering off;
    proxy_cache off;
    gzip off;
    proxy_set_header Accept-Encoding "";

    # Preserve API error responses instead of substituting Nginx error pages.
    proxy_intercept_errors off;

    # Maximum gap between upstream reads, not the total request duration.
    proxy_read_timeout 300s;
}
```

`127.0.0.1:8317` assumes Nginx runs on the same host as the published CPA port.
When Nginx and CPA run in separate containers on the same Docker network, use
`http://cli-proxy-api:8317` (or the actual CPA service name) instead. Loopback inside
the Nginx container refers to that container, not the CPA container.

`proxy_buffering off` is the essential response-streaming setting. The
`X-Accel-Buffering` header also instructs Nginx to disable buffering unless an
inherited `proxy_ignore_headers` setting ignores it. Configuring buffering explicitly
avoids relying on that header alone. Disable other response compression filters for
this location if they are configured separately.

An interval of 15 seconds is comfortably below the 300-second Nginx read timeout.
Each received heartbeat prevents an idle read timeout; a response can therefore take
longer than 300 seconds overall. This does not increase Cloudflare's own timeout.

Validate the effective configuration before reloading:

```bash
sudo nginx -t && sudo nginx -s reload
```

For containerized Nginx, run the equivalent commands inside its container. Nginx
Proxy Manager and other generated configurations must be updated through their
supported custom configuration mechanism so generated files do not overwrite the change.

## Verify the complete path

Set `CPA_BASE_URL` and `CPA_API_KEY` locally, and replace `YOUR_MODEL` with a model
available in the deployment:

```bash
curl --no-buffer --include \
  --request POST "$CPA_BASE_URL/v1/chat/completions" \
  --header "Authorization: Bearer $CPA_API_KEY" \
  --header 'Content-Type: application/json' \
  --data '{"model":"YOUR_MODEL","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

For a reliable long-wait check, use a controllable test upstream that withholds its
response headers for at least three minutes. Compare direct CPA access with access
through the public Cloudflare hostname. The client should receive a complete
`: keep-alive` SSE comment approximately every 15 seconds while the upstream is silent,
then receive the final response or an SSE error. Also check an immediate upstream
failure: it should retain its HTTP error status and contain no heartbeat.

A fast successful request does not verify the long-wait behavior. If direct access
receives periodic comments but the proxied request receives them in a burst, inspect
every intermediary for response buffering and compression. Exclude API routes from
any cache rules that override the origin's cache headers. Nginx normally consumes
`X-Accel-Buffering`, so its absence in the final client response alone is not a failure.

Cloudflare Tunnel's published HTTP applications still have CDN timeout limits; switching
to Tunnel does not remove them. Preserve `Content-Type: text/event-stream` through the
whole path so streaming responses are recognized.

## References

- [Cloudflare error 524](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-524/)
- [Cloudflare connection limits](https://developers.cloudflare.com/fundamentals/reference/connection-limits/)
- [Cloudflare Tunnel streaming troubleshooting](https://developers.cloudflare.com/tunnel/troubleshooting/)
- [Nginx response buffering](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffering)
- [Nginx read timeout](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_read_timeout)
- [Nginx WebSocket proxying](https://nginx.org/en/docs/http/websocket.html)
