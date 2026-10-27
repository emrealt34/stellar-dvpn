# Security model

dmesh is designed so that neither the client nor the node has to trust each
other, and the coordinator cannot touch user funds. This document records what
is enforced today and the remaining hardening roadmap.

## Trust boundaries

- **Soroban settlement contract — source of truth for money.** It holds no
  balances. Funds move only through `transfer_from` against an allowance the
  client approved on the token's Stellar Asset Contract.
- **Coordinator (`dmeshd`) — dumb relay and directory.** It stores
  coordination metadata in SQLite, signs no payments, and holds no wallet.
  Its database can be rebuilt from contract events.
- **Client and node — each signs its own Stellar transactions locally.**
  Private keys never leave the process that owns them.

## Guarantees enforced today

| Threat | Enforcement |
| --- | --- |
| Coordinator steals funds | Impossible by construction: no wallet key that controls user balances, no custody; the contract only exposes `transfer_from` on user allowances. |
| Client uses 3 GB but pays for 1 | `confirm_settlement` only accepts byte counters identical to the client's `propose_settlement`; mismatched confirmations fail and nothing moves. |
| Node invents traffic | The node cannot pull funds without the client's matching on-chain proposal; every session has a unique nonce, so replays are rejected. |
| Replay of a settled session | Re-confirming a settled session fails on chain (covered by the live testnet lifecycle test). |
| Free-riding the data plane without an on-chain session | Sessions are created `pending`. The agent API (`/agent/fetch`, `/agent/connect`) and counter ingestion only accept `opened`/`proposed` sessions, and a session becomes `opened` only after the client attaches a real on-chain `open_session` transaction. A pending or expired session is refused with `401`. |
| Disappearing node / client | Sessions carry a TTL. Unconfirmed sessions expire and never move funds; either party can cancel, which moves no money beyond network resource fees. |
| Hidden fees / price edits | The 20% (2000 bps) protocol fee is hard-coded; tier and asset prices live in contract storage and every change emits an event. |
| Fake node geography | Nodes never self-report country or tier. The coordinator resolves both from the peer IP using CI-refreshed GeoLite2 databases; hosting/cloud ASNs are classified as datacenter. |
| Forged counter reports | Requests are authenticated by Stellar key signatures; counters must be monotonic per party and within an anti-fraud byte/second ceiling, and only the two session parties may report. |

## Authentication

- Node identity is a locally generated Stellar `G...` address. There is no
  password, email, name, or country at node registration.
- API requests are authenticated with signed headers (`X-DMesh-Address`,
  `X-DMesh-Timestamp`, `X-DMesh-Signature`) over a canonical payload; replay is
  bounded by the timestamp window.
- Email OTP exists only for the optional dashboard link and is the sole place
  email is used. There is no SMS path.

## Known limits and roadmap

1. **Absolute payment guarantee (escrow/hold).** An allowance can in principle
   be reduced after a session opens, so "serve then settle" still assumes the
   allowance remains until settlement. The hard guarantee is a contract-level
   spend hold placed at `open_session`: the node can claim against confirmed
   counters within the TTL, and the client can reclaim the unspent hold only
   after expiry. This is the next contract iteration.
2. **On-chain allowance precheck.** The coordinator will additionally read the
   live SAC allowance before promoting a session to `opened` and cap the
   session byte budget to it, stopping service when the budget is reached.
3. **Node-side data plane.** The metered agent API is enforced centrally; the
   peer-to-peer VPN data plane on each node must apply the same
   pending/opened gate and counter logic using the shared contract.
4. **Geo database provisioning.** Node registration returns `503` until the
   CI-refreshed GeoLite2 databases are present; the geo-refresh workflow
   publishes them to the rolling `geoip-latest` release.
5. **Dashboard email.** Without SMTP configured, OTP codes are logged to the
   console (development only). Configure Cloudflare Email SMTP in production.

## Payout addresses and cross-chain transfers (e.g. Monero/XMR)

A node may register an optional `payout_address` (such as a Monero address).
This is **metadata only**:

- Stellar and Monero are separate ledgers. A Soroban contract cannot move XMR,
  and there is no atomic, trustless Stellar→Monero instruction.
- dmesh settlement therefore always occurs in Stellar assets through the
  non-custodial allowance model. Automatically converting and paying out XMR
  would require an independent payout service that holds funds — a deliberate
  custodial trust addition that this project does not make by default.
- A Monero address does not need any XMR balance to **receive** funds; receiving
  has no minimum-balance requirement.
- The trust-minimizing path is for nodes to receive Stellar assets and perform
  any conversion themselves. A future optional, separately-audited payout relay
  can consume the registered addresses, but it is outside the non-custodial
  core and must be opted into explicitly.

## Reporting

Report security issues privately to the repository maintainer rather than
opening a public issue. Do not paste live secrets into tickets or logs.
