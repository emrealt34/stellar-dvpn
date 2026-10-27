# Agent API — metered proxy for agents and scrapers

The coordinator exposes an authenticated, metered egress proxy for HTTP
fetching and tunnelling. Every request is signed with a Stellar ed25519 key
and bound to an **on-chain session**, so traffic is always attributable and
settled through the Soroban contract. The proxy only dials public internet
addresses (loopback, private and metadata ranges are blocked).

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/agent/fetch?url=<https-url>` | Server-side GET, metered by response bytes (25 MB cap per call). |
| CONNECT | `/api/v1/agent/connect` | HTTP CONNECT tunnel for larger streams / arbitrary TCP over TLS. |

Both require an `opened` (or `proposed`) session. A session stays `pending`
until its on-chain `open_session` is attached, so there is no un-metered path.

## Signed headers

All three headers are required, plus the session nonce for agent calls:

| Header | Content |
| --- | --- |
| `X-DMesh-Address` | the client's `G...` address |
| `X-DMesh-Timestamp` | current unix seconds (±5 min window) |
| `X-DMesh-Signature` | base64 ed25519 signature |
| `X-DMesh-Session` | the session nonce hex |

The signed canonical payload is newline-joined and identical on every client:

```
dmesh-v1
<address>
<unix-timestamp>
<part 1>
<part 2>
...
```

For `fetch`, the parts are the HTTP method (`GET`) and the exact target URL.
For `connect`, they are `CONNECT` and the `host:port`.

## Go example

```go
import (
    "encoding/base64"
    "net/http"
    "strconv"
    "strings"
    "time"

    "github.com/stellar/go-stellar-sdk/keypair"
)

func sign(kp *keypair.Full, parts ...string) http.Header {
    ts := time.Now().Unix()
    payload := strings.Join(append([]string{
        "dmesh-v1", kp.Address(), strconv.FormatInt(ts, 10),
    }, parts...), "\n")
    sig, _ := kp.Sign([]byte(payload))
    h := http.Header{}
    h.Set("X-DMesh-Address", kp.Address())
    h.Set("X-DMesh-Timestamp", strconv.FormatInt(ts, 10))
    h.Set("X-DMesh-Signature", base64.StdEncoding.EncodeToString(sig))
    return h
}

// metered server-side GET
func fetch(base string, kp *keypair.Full, nonce, target string) (*http.Response, error) {
    req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/agent/fetch?url="+target, nil)
    for k, vs := range sign(kp, http.MethodGet, target) {
        req.Header[k] = vs
    }
    req.Header.Set("X-DMesh-Session", nonce)
    return http.DefaultClient.Do(req)
}
```

For a scraper, point an HTTP client's proxy at the coordinator and issue
`CONNECT` with the same headers; the tunnel bytes are counted on both
directions and recorded as counter evidence for the next settlement.

## Constraints

- Destinations must be public IPs; internal/metadata addresses return `403`.
- `fetch` buffers up to 25 MB; use `connect` for larger responses.
- Idle tunnels close after five minutes and at most 64 run concurrently.
- Counters must stay monotonic and under the anti-fraud byte/second ceiling,
  and only the two session parties may submit them.
