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
| Free-riding the data plane without an on-chain session | Sessions are created `pending`. The P2P tunnel handshake and counter ingestion only accept `opened`/`proposed` sessions, and a session becomes `opened` only after the client attaches a real on-chain `open_session` transaction. A pending or expired session is refused at the node. The coordinator exposes no data-plane endpoint. |
| Disappearing node / client | Sessions carry a TTL. Unconfirmed sessions expire and never move funds; either party can cancel, which moves no money beyond network resource fees. |
| Hidden fees / price edits | The 20% (2000 bps) protocol fee is hard-coded; tier and asset prices live in contract storage and every change emits an event. |
| False node geography | The coordinator performs no IP lookup. The node client resolves its own country and tier locally against the CI-published geo database and signs the entire claim; every field is covered by the Stellar signature, so it cannot be altered in transit. The coordinator only validates shape and stores it. |
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

1. **Spend hold (shipped).** `open_session_with_hold` pulls a capped escrow
   from the client allowance into contract-held balance at open. Settlement
   pays the node and the 20% fee from the hold and refunds the remainder;
   cancellation returns the full hold, and `reclaim_hold` releases an expired
   one. The legacy zero-hold allowance path remains available.
2. **On-chain allowance precheck.** The coordinator will additionally read the
   live SAC allowance before promoting a session to `opened` and cap the
   session byte budget to it, stopping service when the budget is reached.
3. **Relay for symmetric NAT/CGNAT nodes.** Cone and public nodes connect
   directly or via hole punching; symmetric/CGNAT nodes are currently marked
   `relay-required` and publish no endpoint. A community-run, metered relay
   (never coordinator-hosted) is the next data-plane iteration.
4. **Geo database distribution.** The node client owns classification and
   reads the CI-published GeoLite2 files from the rolling `geoip-latest`
   release. The coordinator has no geo dependency and no geo database to
   operate; a node without the files may pass an explicit manual claim.
5. **Dashboard email.** Without SMTP configured, OTP codes are logged to the
   console (development only). Configure Cloudflare Email SMTP in production.

## Optional payout address

A node may register an optional `payout_address`. It is informational only:
the coordinator never sends funds to it and holds no balance. All settlement
happens in Stellar assets through the non-custodial allowance model, so no
custodial payout rail or treasury wallet is introduced into the core. Any
off-ledger payout using that address is an explicit, separately operated
integration outside the trustless core.

## Tracked non-reachable advisories

- **GO-2026-4316** (`github.com/go-chi/chi` v4, open redirect in
  `RedirectSlashes`). It is pulled in transitively by
  `go-stellar-sdk` v0.8.0 (the latest release). The vulnerable symbol is not
  in dmesh's call graph, and chi v4 has no patched release, so it cannot be
  bumped without an upstream change or a fork. The Go vulnerability scan is
  therefore advisory (`continue-on-error`) and this entry is removed once the
  dependency ships a fix.

## Reporting

Report security issues privately to the repository maintainer rather than
opening a public issue. Do not paste live secrets into tickets or logs.
