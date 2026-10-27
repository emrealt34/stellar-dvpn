# Security audit

A structured review of the coordinator and agent proxy, classified by severity
and with the resolution for each finding. The settlement contract is the
authority for money; the coordinator cannot move funds, so no finding grants
custody or fund theft. Scans (`govulncheck`, `npm audit`, `gitleaks`,
`cargo audit`) run in the `quality` workflow.

## Resolved

| ID | Severity | Finding | Resolution |
| --- | --- | --- | --- |
| H-1 | High | The agent endpoints were an open proxy/SSRF: `GET /agent/fetch` and `CONNECT /agent/connect` could reach loopback, RFC1918 ranges and the cloud metadata endpoint (`169.254.169.254`) from the coordinator's network. | New `internal/netguard` blocks every non-public IP. A dialer `Control` hook validates the exact IP at connect time (DNS-rebinding safe), with an early `CheckHost`. Covered by `netguard_test.go` and an SSRF handler test. |
| H-2 | High | A hard-coded default `JWT_SECRET` meant dashboard tokens could be forged if the environment variable was omitted. | `Config.ValidateProduction` refuses to start when `DEV_OTP_CODES=false` and the secret is missing, default, or shorter than 32 bytes. |
| M-1 | Medium | CONNECT tunnels had no deadlines or concurrency limit, so idle sockets could exhaust file descriptors. | Per-connection idle deadline (5 min, refreshed on activity), absolute initial deadline, and a 64-tunnel semaphore that returns 503 at capacity. |
| M-2 | Medium | Most JSON endpoints accepted unbounded request bodies. | A 1 MiB `MaxBytesReader` is applied to every request in middleware (CONNECT hijack streams are unaffected). |
| M-3 | Medium | Proxy errors built JSON by string concatenation, allowing malformed/injected JSON from upstream error text. | All proxy errors now go through a JSON encoder (`jsonError`). |
| L-1 | Low | API responses lacked hardening headers. | `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` and a restrictive CSP are set on every response. |
| L-2 | Low | OTP requests were rate-limited per email only, allowing cross-inbox spam from one IP. | A second per-IP sliding-window limiter (10 / 10 min) is enforced alongside the per-email limit. |
| L-3 | Low | `clientIP` only trusted `X-Forwarded-For` on loopback peers, so behind Render's proxy it returned the proxy address and broke the node's self-classification. | The platform-provided first `X-Forwarded-For` hop is preferred. The value is not a trust boundary: geo/tier claims are self-derived and Stellar-signed, and the IP is never in the public directory. |
| H-3 | High | Dependabot: integer overflow / out-of-bounds write in transitive `github.com/klauspost/compress` (≤ 1.18.6, via the Stellar SDK's zstd XDR codec). | Pinned to v1.20.3 in `go.mod`; build and full test suite pass, Dependabot alert cleared. |
| M-4 | Medium | No egress exit policy: the proxy could relay to abuse-prone service ports (SMTP spam, SMB/NetBIOS, NFS, legacy shells). | `netguard.CheckPort` enforces an exit-policy denylist (25/465/587, 135–139/445, 2049, 512–514, …) in both handlers and in the dialer `Control` hook; covered by table tests. |

## Accepted / by design

| ID | Severity | Item | Rationale |
| --- | --- | --- | --- |
| A-1 | Low | Signed mesh requests have a 5-minute validity window and no server-side nonce store, so a captured request can be replayed within the window. | All signed operations are idempotent or monotonic: registration is an upsert, counters only increase, and settlement is enforced by the on-chain nonce/replay guard. A nonce cache can be added if stricter transport semantics are needed. |
| A-2 | Informational | CORS is `Access-Control-Allow-Origin: *`. | Authentication uses Stellar-signed headers, never cookies or ambient credentials, so a wildcard origin cannot authorize anything without the caller's secret key. |
| A-3 | Informational | `GO-2026-4316` in transitive `go-chi/chi v4` (via the Stellar SDK). | The vulnerable `RedirectSlashes` symbol is outside the call graph and chi v4 has no patched release; tracked in `SECURITY.md` and the govulncheck job is advisory. |
| A-4 | Informational | `RUSTSEC-2026-0387`: timing variability in transitive `crypto-bigint 0.5.5` (via `soroban-sdk`), fixed in 0.7.6. | The contract performs no private-key or constant-time operations — signing happens off-chain — so the side channel is not reachable in our code; a fix requires a future `soroban-sdk` upgrade. The `cargo audit` job is non-blocking and tracks it. |

## Implemented controls (roadmap items)

- **Contract-level spend hold.** `open_session_with_hold` locks a capped escrow
  from the client's allowance for the session's lifetime. Settlement pays the
  node and the 20% fee out of the hold and refunds any remainder; cancellation
  refunds immediately, and after TTL either party can call `reclaim_hold`, which
  pays the client's committed counters (if any) and refunds the rest. A
  `released` flag prevents any double payout. Covered by five dedicated contract
  tests; the legacy zero-hold allowance path remains fully supported.
- **Peer-to-peer data plane.** `internal/tunnel` moves traffic directly between
  client and node — the coordinator carries zero bytes and is used only for
  signaling (`/sessions/lookup`) and counter collection. Each tunnel requires a
  Stellar-signed handshake bound to an on-chain session, and the node reuses the
  identical `netguard` SSRF/exit-policy rules, tunnel cap and idle deadlines as
  the hosted proxy. The client exposes a local HTTP CONNECT proxy
  (`dmesh tunnel`); the node runs `dmesh tunnel-listen`.

## Still on the roadmap

- **WireGuard kernel transport.** The P2P data plane currently uses an
  application-level authenticated TCP relay; the bundled WireGuard control plane
  will carry the same signed-session gate, counters and `netguard` policy once
  the kernel module path is exercised across the Windows/Linux builds.
- **Signed nonce cache.** The 5-minute signed-request replay window (A-1) can be
  narrowed with a server-side nonce cache if stricter transport semantics are
  required.

## Tier integrity (client-to-client, no backend trust)

Residential vs datacenter is not decided by the backend. Both the node and the
consuming client run the identical, deterministic `internal/geo` classifier
against the CI-published GeoLite2 databases. The node signs its claim; the
client independently classifies the node's observed endpoint IP with
`dmesh verify-node` (or `session --node-ip`) and refuses to open a session on
disagreement. The coordinator only stores the signed claim and tracks liveness.
