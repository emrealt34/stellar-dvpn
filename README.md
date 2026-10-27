# dmesh

<p align="left">
  <img src="site/logo.svg" width="72" height="72" alt="dmesh logo">
</p>

**Trustless, non-custodial decentralized VPN and pay-per-use agent API, settled on Stellar Soroban.**

## Live deployments

| Component | URL |
| --- | --- |
| Website / download page (GitHub Pages) | <https://emrealt34.github.io/stellar-dvpn/> |
| Coordinator API (Render, testnet) | <https://dmesh-coordinator.onrender.com> |
| Source repository | <https://github.com/emrealt34/stellar-dvpn> |
| Releases / prebuilt binaries | <https://github.com/emrealt34/stellar-dvpn/releases> |

Live API endpoints: [`/api/v1/health`](https://dmesh-coordinator.onrender.com/api/v1/health),
[`/api/v1/public/pricing`](https://dmesh-coordinator.onrender.com/api/v1/public/pricing),
[`/api/v1/public/stats`](https://dmesh-coordinator.onrender.com/api/v1/public/stats).
Pricing is served live from the Soroban contract (`"source": "contract"`).

dmesh lets anyone run a mesh exit node and anyone consume residential or
datacenter bandwidth / HTTP egress, paying only for the bytes they actually
move. There is no platform token, no custodial wallet, and no internal credit
ledger: every payment is executed by a Soroban smart contract in real Stellar
assets (native XLM and any Stellar token with a configured price) through
allowances that users approve themselves.

## Why it is trustless

| Concern | How dmesh solves it |
| --- | --- |
| Platform holding user money | It never does. The contract holds no balances and the service controls no wallets. Funds move only via `transfer_from` on an allowance the user approved. |
| Client pays for traffic it did not use | Both parties must authorize the same byte counters on chain (`propose_settlement` by the client, `confirm_settlement` by the node). A mismatch rejects the settlement. |
| Node invents fake traffic | The node cannot pull funds without the client's matching on-chain proposal; every session has a unique nonce, so replays are impossible. |
| Disappearing node / client crash | Sessions carry a TTL. A session that is never confirmed simply expires; no money is ever charged. Either party can cancel. |
| Hidden fees or price changes | The 20% protocol fee is hard-coded in the contract; tier prices and asset prices live in contract storage and every change emits an auditable event. |
| Fake node location / tier | Nodes never self-report country or network type. The coordinator derives both from the peer IP using CI-refreshed GeoLite2 databases (country + ASN), with hosting/cloud ASNs classified as datacenter. |

## Money flow

```
 Client                Soroban contract                 Node
   |  approve(contract, allowance)                         |
   | -------------->  (allowance stored on token)          |
   |  open_session(client,node,token,tier,nonce,ttl)       |
   | -------------->  session stored, event emitted        |
   |  ... traffic metered off chain, counters reported ... |
   |  propose_settlement(id, up, down)   (client auth)     |
   | -------------->  proposal stored                       |
   |                    confirm_settlement(id, up, down)    |
   |                    <-------------- (node auth)          |
   |                    transfer_from(client -> node, 80%)  |
   |                    transfer_from(client -> admin, 20%) |
```

If the two counter sets differ, the call fails and nothing moves. If either
party disappears, the session expires after its TTL and is cancelled without
any token movement.

## Pricing (stored in the contract)

| Tier | Price | Symbol on chain |
| --- | --- | --- |
| Residential | USD 0.15 / GB | `res` |
| Datacenter | USD 0.03 / GB | `dc` |

Protocol fee: **20%** of every settlement (`2000` bps), paid directly to the
protocol admin address by the contract. Asset prices are micro-USD per whole
token (e.g. XLM at `100000` = USD 0.10), set by the admin with a
`price_set` event on every change.

## Testnet deployment

- Network: Stellar Testnet (`Test SDF Network ; September 2015`)
- Settlement contract: `CCBNQXDZIOYBSFRSXBSNMCLANJG6H6EK4HFTO2A7EJR4BJD2HGM3YHOT`
- Native XLM SAC used in the end-to-end test:
  `CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC`
- RPC: `https://soroban-testnet.stellar.org`
- Horizon: `https://horizon-testnet.stellar.org`

The full on-chain lifecycle (allowance, open, propose, confirm, 80/20 split,
mismatch rejection, replay rejection, cancellation with no transfer) is
exercised against the live testnet by:

```bash
DMESH_E2E=1 \
DMESH_CONTRACT_ID=CCBN... \
DMESH_XLM_CONTRACT=CDLZ... \
DMESH_ADMIN_SECRET=S... \
go test ./internal/soroban -run TestTestnetSettlementFullLifecycle -v
```

## Repository layout

```
contract/          Rust/Soroban settlement contract (soroban-sdk 29)
  src/lib.rs         contract logic + pure pricing math
  src/test.rs        9 unit tests (split, tiers, replay, expiry, cancel...)
backend/           Go coordinator ("dumb" non-custodial coordinator)
  cmd/dmesh          reference mesh client / node CLI
  cmd/dmeshd         coordinator HTTP service
  internal/soroban   typed Go bindings for the contract + RPC lifecycle
  internal/geo       GeoLite2 country/ASN lookup + residential/dc classifier
  internal/meshauth  Stellar ed25519 signed-request authentication
  internal/metering  counter evidence + anti-fraud rate ceiling
  internal/agent     metered HTTP fetch and CONNECT proxy
  internal/api       REST API, dashboard OTP, node directory
```

## Local development

### Contract

```bash
cd contract
cargo test
SOROBAN_SDK_BUILD_SYSTEM_SUPPORTS_SPEC_SHAKING_V2=1 \
  cargo build --target wasm32v1-none --release
stellar contract optimize --wasm target/wasm32v1-none/release/dmesh_settlement.wasm
```

### Backend

```bash
cd backend
go build ./...
go vet ./...
go test ./...
```

Run the coordinator:

```bash
export SETTLEMENT_CONTRACT_ID=CCBN...
export GEO_COUNTRY_DB=data/GeoLite2-Country.mmdb
export GEO_ASN_DB=data/GeoLite2-ASN.mmdb
# optional: SMTP for dashboard OTP (Cloudflare Email Routing SMTP works)
go run ./cmd/dmeshd
```

Node identity is a locally generated Stellar key (`dmesh keygen`). The secret
seed never leaves the machine; requests are authenticated with ed25519
signatures over a canonical payload.

## Security model notes

- No passwords exist anywhere. Dashboard access uses email OTP only; node and
  settlement actions use Stellar key signatures. SMS is not used.
- The coordinator database is rebuildable from contract events; it stores
  coordination metadata only, never funds.
- Never paste secret seeds into the repository or issue trackers. Rotate any
  seed that was shared outside a local, trusted environment.

## License

MIT
