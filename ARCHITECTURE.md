# dmesh — Trustless Decentralized VPN & Paid Agent Gateway

## Core principles

1. **No dmesh token. Ever.** Payments settle in real Stellar assets — native
   **XLM** and any Stellar Classic/Soroban token the client chooses.
2. **Non-custodial.** dmesh servers never hold user funds. Every token stays in
   the user's own Stellar wallet until an on-chain settlement pulls it with a
   scoped token allowance the user approved.
3. **The Soroban smart contract is the only source of trust.** It verifies
   authorization, enforces pricing, splits the fee and blocks replay. The
   backend is a dumb coordinator: discovery, relay of signed statements,
   geo-IP classification and indexing. It can lie in its database — nobody's
   money depends on it.
4. **20% protocol fee, on-chain.** Each settlement sends 80% to the exit node
   and 20% to the protocol address. The split is contract code, not a server
   setting.
5. **Nobody can cheat.** A client that consumed 3 GB cannot settle 1 GB; a node
   cannot invent traffic. Settlement requires both parties' Stellar
   authorization on the exact same byte counters.
6. **Client-provided metadata is never trusted.** Node country/IP type
   (residential vs datacenter) is derived server-side from the node's observed
   public IP against a geo-IP database refreshed daily by CI.

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
      |        (traffic flows over WireGuard, both sides keep     |
      |         hash-chained ed25519-signed counter statements)   |
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
  signs a server challenge with its mesh key to prove control. The backend
  records the observed public IP, resolves country + ASN + residential class
  from the CI-refreshed geo database, and assigns the pricing tier.
- Email OTP exists **only** for the web/app dashboard login. No SMS.

## Components

| Component | Stack | Responsibility |
|---|---|---|
| `contract/` | Rust + Soroban | settlement, fee split, tier table, oracle entries, auth, replay protection |
| `backend/` | Go, stdlib HTTP, SQLite | OTP, mesh registry, geo-IP, session relay, agent API, Soroban RPC client |
| `cli/` (`dmesh`) | Go | wallet, node daemon, WireGuard control, signed counters, contract calls |
| `web/` | Astro + React | marketing site, live map/stats, docs, downloads, dashboard |
| `extension/` | WebExtension | browser-side agent API consumer |
| `.github/` | Actions | tests, contract build, cross-platform releases, geo-DB refresh, deploys |

## Trust boundaries

| Claim | Who/what guarantees it |
|---|---|
| Bytes billed | dual Stellar authorization in `confirm_settlement` |
| Price/tier | contract storage + change events |
| Fee goes to protocol | hard-coded 2000 bps split in contract |
| Node location/type | server geo-IP DB (CI refreshed), client input ignored |
| No double spend | settled flag + unique nonce, enforced by contract |
| Funds safety | allowances only; contract holds no balance, no keys |
| Crash recovery | TTL expiry; unconfirmed sessions never draw funds |
