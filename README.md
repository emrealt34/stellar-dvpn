<p align="left">
  <img src="web/public/logo.svg" width="64" height="64" alt="dmesh logo">
</p>

# dmesh

Pay-per-request web access for AI agents and scrapers, settled per byte by a
Soroban smart contract on Stellar. Traffic runs directly between agents and
community nodes over an authenticated P2P tunnel; the coordinator is signaling
and liveness only and never holds funds or relays user traffic.

## Live

| Component | URL |
| --- | --- |
| Web app (Astro + React, GitHub Pages) | <https://emrealt34.github.io/stellar-dvpn/> |
| Coordinator API (Render, testnet) | <https://dmesh-coordinator.onrender.com> |
| Releases | <https://github.com/emrealt34/stellar-dvpn/releases> |
| Source | <https://github.com/emrealt34/stellar-dvpn> |

The coordinator serves pricing directly from the contract:
[`/api/v1/public/pricing`](https://dmesh-coordinator.onrender.com/api/v1/public/pricing),
with live network state at
[`/api/v1/public/stats`](https://dmesh-coordinator.onrender.com/api/v1/public/stats)
and [`/api/v1/nodes`](https://dmesh-coordinator.onrender.com/api/v1/nodes).

## Model

- **Non-custodial.** The service holds no wallet and no balance. A session
  moves funds only through `transfer_from` against an allowance the client
  approved on the token contract.
- **Dual-party settlement.** The client proposes byte counters on chain and
  the node confirms the same values. A mismatch fails and nothing moves, so
  traffic can be neither fabricated nor underpaid. Replay is rejected via a
  per-session nonce.
- **Crash safe.** Sessions carry a TTL. An unconfirmed session simply expires
  and never charges beyond network fees. The data plane refuses a session
  until its on-chain `open_session` is attached, so traffic without an on-chain
  session is impossible.
- **Verifiable pricing.** Residential $0.15/GB and datacenter $0.03/GB, with a
  fixed 20% (2000 bps) protocol fee, live in contract storage; changes emit
  events.
- **A deliberately dumb coordinator.** It relays signed statements and keeps
  an index in SQLite. Node geography and tier are classified by the node
  client itself against the CI-published geo database, then signed; the
  coordinator stores the claim and performs no geo lookup.

## Money flow

```
 Client                Soroban contract                 Node
   | approve(allowance)                                   |
   | ----------------->  token allowance recorded         |
   | open_session(client,node,token,tier,nonce,ttl)       |
   | ----------------->  session stored, event emitted    |
   |   ... traffic metered, counters reported ...         |
   | propose_settlement(up,down)        (client auth)     |
   | ----------------->  proposal stored                  |
   |                     confirm_settlement(up,down)       |
   |                     <--------------- (node auth)     |
   |                     transfer_from -> node 80%         |
   |                     transfer_from -> protocol 20%     |
```

## Testnet

| Item | Value |
| --- | --- |
| Network | Test SDF Network ; September 2015 |
| RPC | `https://soroban-testnet.stellar.org` |
| Horizon | `https://horizon-testnet.stellar.org` |
| Settlement contract | `CD3CYW3VGVCQBQUPLQTTWJPLYB6DNARFQBOSFLTK2B55XWLJDDVDBMMK` |
| XLM asset contract | `CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC` |

The end-to-end lifecycle (allowance, capped spend hold, session, atomic split,
replay rejection, cancellation with no transfer) is exercised against live
testnet in `backend/internal/soroban/live_test.go`. The capped escrow path
(`open_session_with_hold` → escrow split → automatic remainder refund → TTL
reclaim, including early/double-reclaim rejection) is proven on this exact
contract in `hold_live_test.go`; the latest captured run is in
[`docs/test-runs/2026-10-28-spend-hold.txt`](docs/test-runs/2026-10-28-spend-hold.txt).
The previous deployment without spend-hold support
(`CCBNQXDZIOYBSFRSXBSNMCLANJG6H6EK4HFTO2A7EJR4BJD2HGM3YHOT`) is superseded.

### P2P data plane in three commands

```bash
# Node: discover your NAT type, sign the reachability claim, accept tunnels
dmesh nat-probe
dmesh register --coordinator "$URL" --secret "$NODE_S" --tier res \
  --country-iso TR --country Turkiye --asn 9121 --asn-org "Example AS" \
  --nat-probe --external 203.0.113.7:7777
dmesh tunnel-listen --coordinator "$URL" --secret "$NODE_S" --listen 0.0.0.0:7777

# Agent: coordinate + attach the on-chain open_session, then expose a local proxy
dmesh session  --coordinator "$URL" --secret "$CLIENT_S" --node "$NODE_G" --token C...
dmesh attach   --coordinator "$URL" --secret "$CLIENT_S" --nonce "$NONCE" --contract-id 1 --tx-hash "$HASH"
dmesh tunnel   --coordinator "$URL" --secret "$CLIENT_S" --nonce "$NONCE" \
  --node-tcp 203.0.113.7:7777 --local 127.0.0.1:1080
curl -x http://127.0.0.1:1080 https://example.com
```

A fully scripted local run — coordinator, node, agent proxy, real public
egress and the blocked-port exit-policy check — lives in
[`scripts/p2p-demo.sh`](scripts/p2p-demo.sh), with a captured transcript in
[`docs/test-runs/`](docs/test-runs/). The coordinator exposes no fetch or
CONNECT endpoint: all traffic stays peer to peer.

## Layout

| Path | Purpose |
| --- | --- |
| `contract/` | Rust/Soroban settlement contract (tier table, fee split, auth, replay guard) |
| `backend/` | Go coordinator (`dmeshd`) and reference client (`dmesh`); stdlib HTTP, SQLite, pure-Go build |
| `web/` | Astro + React static app: marketing, live network island, pricing, downloads |
| `scripts/` | Dependency/vulnerability reports, reproducible P2P demo (`p2p-demo.sh`) |
| `docs/test-runs/` | Captured end-to-end evidence from real P2P/egress runs |
| `.github/workflows/` | Build, test, contract/wasm, releases, Pages deploy, geo-DB refresh, security scans |

## Develop

```bash
# Contract
cd contract && cargo test && cargo build --target wasm32v1-none

# Backend (Go 1.27, CGO-free)
cd backend && go build ./... && go test ./...

# Node registration classifies geography in the client, not the daemon
dmesh register --coordinator "$URL" --secret "$S" \
  --geo-country GeoLite2-Country.mmdb --geo-asn GeoLite2-ASN.mmdb

# Web app (Node 22)
cd web && npm ci && npm run dev
```

## Documentation

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — trust model, components, client-to-client tier agreement
- [`docs/SECURITY_AUDIT.md`](docs/SECURITY_AUDIT.md) — severity-classified findings and fixes
- [`docs/AGENT_API.md`](docs/AGENT_API.md) — signed, metered proxy for agents/scrapers
- [`SECURITY.md`](SECURITY.md) — reporting, secret handling, advisory list
- [`docs/ACCEPTABLE_USE.md`](docs/ACCEPTABLE_USE.md) — enforced exit policy and abuse process
- [`docs/DEPENDENCIES.md`](docs/DEPENDENCIES.md) — dependency and vulnerability report

A client never trusts the backend for geography: it independently classifies a
node's endpoint IP and must agree on the tier before opening a session
(`dmesh verify-node`, or `dmesh session --node-ip <ip>`).

> **Deployment note:** the coordinator stores registry/session state in SQLite.
> On an ephemeral host (e.g. a free tier without a persistent disk) the
> database resets on redeploy; attach a persistent volume or point
> `DMESH_DB_PATH` at durable storage for a standing testnet.

<details>
<summary><strong>Dependency & vulnerability report</strong></summary>

Direct dependencies and available updates are regenerated by CI into
[`docs/DEPENDENCIES.md`](docs/DEPENDENCIES.md) (weekly and on demand via the
`dependencies` workflow). Scans: `govulncheck` (Go), `npm audit` (web),
`cargo audit` (contract), and gitleaks for committed secrets.

Current direct dependencies:

| Area | Package | Version |
| --- | --- | --- |
| Backend | github.com/stellar/go-stellar-sdk | v0.8.0 |
| Backend | modernc.org/sqlite | v1.61.0 |
| Backend | github.com/golang-jwt/jwt/v5 | v5.3.1 |
| Backend | github.com/oschwald/geoip2-golang | v1.13.0 |
| Contract | soroban-sdk | 29.0.1 |
| Web | astro | 7.4.2 |
| Web | @astrojs/react | 7.1.1 |
| Web | react / react-dom | 19.3.1 |

</details>

## License

MIT — see [LICENSE](LICENSE). Testnet software: use test funds only.
