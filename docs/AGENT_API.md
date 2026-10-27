# Agent data plane — metered P2P access for agents and scrapers

Agents and scrapers do not send traffic to the coordinator. The coordinator is
a signaling, directory and liveness service only. Data flows over an
authenticated, metered **peer-to-peer tunnel** straight to a node, and the node
performs the egress with the SSRF guard and exit policy enforced locally.

## Components

| Piece | Where it runs | Responsibility |
| --- | --- | --- |
| `dmeshd` coordinator | hosted service | directory, session signaling, counters, heartbeat — no user traffic |
| `dmesh tunnel-listen` | node | P2P server, session verification, `netguard` egress policy, byte metering |
| `dmesh tunnel` | agent host | local HTTP `CONNECT` proxy that tunnels every request to the node |
| Soroban contract | Stellar testnet | spend hold, two-party settlement, 20% protocol fee |

## Request lifecycle

1. The agent discovers a node in the public directory, including its `endpoint`,
   `nat_type` and `connectivity` (`direct`, `hole-punch`, `relay-required`).
2. The two parties coordinate a session and the client attaches the on-chain
   `open_session` (optionally `open_session_with_hold` for a capped escrow).
   Only an `opened`/`proposed` session can carry traffic.
3. The agent runs a local CONNECT proxy:

   ```bash
   dmesh tunnel \
     --coordinator https://dmesh-coordinator.onrender.com \
     --secret S... \
     --nonce <session-nonce-hex> \
     --node-tcp 203.0.113.7:7777 \
     --local 127.0.0.1:1080
   ```

4. Any standard HTTP client or scraper uses it as an ordinary proxy:

   ```bash
   curl -x http://127.0.0.1:1080 https://example.com
   ```

   Go scraper:

   ```go
   transport := &http.Transport{
       Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "127.0.0.1:1080"}),
   }
   client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
   resp, err := client.Get("https://example.com/data.json")
   ```

## Tunnel protocol

The client opens a TCP connection to the node and immediately sends one
newline-terminated JSON handshake (`internal/tunnel`):

```json
{
  "v": 1,
  "nonce": "<session nonce hex>",
  "address": "G... client mesh address",
  "timestamp": 1761560000,
  "target": "example.com:443",
  "signature": "<base64 ed25519 over dmesh-v1 canonical payload>"
}
```

The node verifies the Stellar signature, confirms the session state with the
coordinator, checks the target against the exit policy, then dials and relays.
Byte counters in both directions are submitted to the coordinator on teardown
and become the settlement evidence.

## Safety and limits

- No loopback, RFC1918, link-local, metadata, multicast or unspecified
  destinations; the dial-time control hook also closes DNS rebinding.
- Blocked abuse ports include 25/465/587 (SMTP), 135-139/445 (SMB/RPC),
  111, 2049 and others — see [`ACCEPTABLE_USE.md`](ACCEPTABLE_USE.md).
- Per-node tunnel cap (128 concurrent) and a five-minute idle deadline.
- The coordinator exposes no fetch or CONNECT endpoint; removing the hosted
  proxy eliminated its SSRF/open-proxy attack surface entirely.

## NAT and reachability

Nodes self-probe with RFC 5389 STUN (`dmesh nat-probe`) and sign the result
into their claim:

| `nat_type` | Meaning | `connectivity` |
| --- | --- | --- |
| `public` | public interface, no NAT | `direct` |
| `cone` | endpoint-independent mapping (full/restricted/port-restricted) | `hole-punch` |
| `symmetric` | mapping varies per destination, typical of CGNAT | `relay-required` |
| `udp-blocked` | STUN unreachable | `relay-required` |

`relay-required` nodes publish no endpoint and should enable port
forwarding/UPnP or participate as clients until a community relay exists.
