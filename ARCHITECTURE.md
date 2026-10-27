# dmesh — Trustless Decentralized VPN & Paid Agent Gateway

## Core principles

1. **No dmesh token. Ever.** Payments settle in real Stellar assets — native
   **XLM** and any Stellar Classic/Soroban token the client chooses.
2. **Non-custodial.** dmesh servers never hold user funds. Every token stays in
   the user's own Stellar wallet until an on-chain settlement pulls it with a
   scoped token allowance the user approved.
3. **The Soroban smart contract is the only source of trust.** It verifies
   authorization, enforces pricing, splits the fee and blocks replay. The
   backend is a dumb coordinator: discovery and relay of signed statements and
   indexing. It holds no wallet and does no geo-IP classification — it can lie
   in its database, and nobody's money depends on it.
4. **20% protocol fee, on-chain.** Each settlement sends 80% to the exit node
   and 20% to the protocol address. The split is contract code, not a server
   setting.
5. **Nobody can cheat.** A client that consumed 3 GB cannot settle 1 GB; a node
   cannot invent traffic. Settlement requires both parties' Stellar
   authorization on the exact same byte counters.
6. **The node classifies itself, the coordinator stays dumb.** The node client
   resolves its own public IP and derives country/IP type (residential vs
   datacenter) locally against the CI-published geo database, then signs the
   full claim. The coordinator stores the signed record but performs no geo
   lookup, so no trust in the backend is required even for geography.

## Settlement flow (Soroban)

```
 Client wallet                dmesh contract                 Node wallet
      |                            |                              |
      | 1. token.approve(contract, |                              |
      |    session allowance)      |                              |
      |--------------------------->|                              |
      | 2. open_session(node,token,|                              |
      |    tier, nonce, ttl)       |                              |
      |--------------------------->|                              |
      |        (traffic flows over the P2P tunnel, both sides     |
      |         keep ed25519-signed, monotonic counter records)   |
      | 3. propose_settlement(id,  |                              |
      |    up, down)  client auth  |                              |
      |--------------------------->|                              |
      |                            | 4. confirm_settlement(id,    |
      |                            |    up, down) node auth       |
      |                            |<-----------------------------|
      |  transfer_from client 80%  |----------------------------->| node
      |  transfer_from client 20%  |------> protocol admin        |
      |                            |  session marked settled      |
```

### Spend hold (escrow) option

`open_session_with_hold` pulls a capped amount into programmatic escrow at
session start instead of relying solely on a later allowance draw:

- the client can never pay above the hold; the node holds a guaranteed claim;
- `confirm_settlement` pays the 80/20 split from escrow and refunds the
  remainder, all in one transaction;
- `cancel_session` refunds the full hold immediately;
- after TTL either party calls `reclaim_hold`: committed (proposed) usage is
  paid and the remainder refunded, or the full hold returns to the client when
  nothing was proposed. A `released` flag makes double payout impossible.

The zero-hold `open_session` allowance path remains supported; 14 contract
tests cover both, including lock/settle/refund and post-expiry reclaim.

- **Two-step settlement** (propose/confirm) means each party signs its own
  transaction; no off-line signature collection is needed.
- If the node crashes, disappears, or disagrees, `confirm_settlement` never
  happens: the allowance is simply never drawn. Sessions expire on-chain.
  A client is only ever charged for mutually-attested bytes.
- If the client disappears, the node keeps the client's signed proposal and
  confirms within the TTL; after expiry the session is void.
- Replay is impossible: a settled session id is rejected, and every session has
  a unique nonce.

## Pricing (contract-stored, extensible)

| Tier | Price (USD / GB) | Source |
|---|---|---|
| residential | **$0.15** | contract tier table |
| datacenter | **$0.03** | contract tier table |

USD→token conversion uses an on-chain price entry updated by a CI-fed oracle
transaction (daily, signed by the protocol admin, emitted as a contract event).
The fee is `2000 bps = 20%`. Tier prices and fee updates emit events, so every
rate change is permanently auditable on-chain.

## Identity: mesh addresses, no OTP for nodes

- A node's identity is a **mesh address**: the `G...` Stellar address derived
  from a key generated locally on the device (`dmesh --address G...` or
  `dmesh --address you@mail.example` for the app account).
- Node registration requires no email, no OTP, no name, no country. The node
  client classifies its own public IP against the CI-published geo database,
  then signs the country + ASN + tier claim with its mesh key. The coordinator
  stores the signed claim (and the observed transport IP, kept private) but
  performs no geo lookup and assigns nothing.
- Email OTP exists **only** for the web/app dashboard login. No SMS.

## Components

| Component | Stack | Responsibility |
|---|---|---|
| `contract/` | Rust + Soroban | settlement, fee split, tier table, oracle entries, auth, replay protection |
| `backend/` | Go, stdlib HTTP, SQLite | OTP, mesh registry (stores signed claims), session signaling, counter collection, Soroban RPC client — no geo-IP dependency, no user traffic |
| `cli/` (`dmesh`) | Go | wallet, P2P node daemon (`tunnel-listen`) and local CONNECT proxy (`tunnel`), STUN NAT probe, signed counters, contract calls |
| `web/` | Astro + React | marketing site, live world map/stats, docs, downloads, dashboard |
| `extension/` | WebExtension | browser-side consumer of the local P2P proxy |
| `.github/` | Actions | tests, contract build, cross-platform releases, geo-DB refresh, deploys |

## Trust boundaries

| Claim | Who/what guarantees it |
|---|---|
| Bytes billed | dual Stellar authorization in `confirm_settlement` |
| Price/tier | contract storage + change events |
| Fee goes to protocol | hard-coded 2000 bps split in contract |
| Node location/type | node client classifies locally against the CI-published geo DB and signs it; the consuming client independently re-classifies the node's endpoint IP and must agree before opening a session; coordinator stores the claim |
| No double spend | settled flag + unique nonce, enforced by contract |
| Egress destination safety | `internal/netguard` on every node restricts P2P egress to public IPs and allowed ports, enforced by a dialer hook at connect time; the coordinator has no egress path at all |
| Node reachability | the node self-probes NAT via STUN (`internal/nat`) and signs `endpoint`/`nat_type`/`connectivity`; symmetric/CGNAT nodes publish no endpoint and are marked relay-required |
| Spend cap | `open_session_with_hold` locks capped escrow; the node cannot claim beyond it and the client gets the remainder refunded |

### Client-to-client tier agreement

The coordinator never decides residential vs datacenter. Both peers run the
same deterministic classifier (`internal/geo`) against the same CI-published
GeoLite2 databases. A client verifies a node with `dmesh verify-node --mesh
G... --ip <node-endpoint-ip>` or passes `--node-ip` to `dmesh session`; a
tier mismatch refuses the session unless the client explicitly overrides it.
The backend is limited to signaling (relaying session messages) and node
liveness/heartbeat storage.

### Peer-to-peer data plane and NAT discovery

There is no coordinator-side proxy. The node runs `dmesh tunnel-listen` and
the agent runs `dmesh tunnel`, a local HTTP CONNECT proxy; the two peers
establish an authenticated TCP tunnel with a Stellar-signed handshake bound to
an on-chain session. The node applies the SSRF guard and the Tor-style exit
policy before dialing any target, meters both directions, and submits
cumulative counters on teardown. Replicable end-to-end:
[`scripts/p2p-demo.sh`](scripts/p2p-demo.sh), evidence in
[`docs/test-runs/`](docs/test-runs/). See also
[`docs/AGENT_API.md`](docs/AGENT_API.md) and
[`docs/SECURITY_AUDIT.md`](docs/SECURITY_AUDIT.md).

Nodes classify their own network path with RFC 5389 STUN against independent
operators and sign the result into the directory: `public`/`direct`,
`cone`/`hole-punch`, or `symmetric` (including RFC6598 CGNAT) /
`relay-required` with no published endpoint. The connecting peer still
observes the real remote socket address at connect time rather than trusting
the claim alone.

### Peer-to-peer data plane

`internal/tunnel` keeps user traffic off the coordinator. The node runs
`dmesh tunnel-listen`; the client runs `dmesh tunnel`, which exposes a local
HTTP CONNECT proxy. Every tunnel starts with a Stellar-signed handshake bound
to an on-chain session nonce and target; the node verifies it through the
coordinator's `/sessions/lookup` (signaling only), applies the same
`netguard` SSRF and exit-policy checks, relays the bytes, and submits
cumulative counters on teardown. The coordinator never sits in the byte path.
| Funds safety | allowances only; contract holds no balance, no keys |
| Crash recovery | TTL expiry; unconfirmed sessions never draw funds |
