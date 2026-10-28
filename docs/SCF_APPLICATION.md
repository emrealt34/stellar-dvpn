# Stellar Community Fund application — dmesh

**Track:** Open Track (Build)
**Prepared:** 2026-10-29 · **Submitter:** Hasan Altinok
**Repository:** https://github.com/emrealt34/stellar-dvpn

> Section A is written as paste-ready answers to the SCF form fields.
> Sections B–E are supporting narrative, roadmap, budget and on-chain evidence.
> Items marked `[Hasan: …]` are personal details only you can fill in. Every
> technical claim maps to a shipped artifact, CI run or testnet transaction.

---

## A. Form answers (paste-ready)

### Project Information

**Project Title**
dmesh — pay-per-byte web access for AI agents, settled on Stellar

**Project Description**
dmesh is a decentralized data-access network that lets AI agents, scraping
pipelines and data engineers pay for web access per request, instead of buying
flat subscriptions to centralized proxy APIs. Agents open an authenticated
peer-to-peer tunnel to a community-run node, retrieve exactly the pages they
need, and settle the metered byte counters through a Soroban smart contract on
Stellar — no subscription, no shared API key, no custodian holding deposits.

Objectives:
1. Make single-request economics possible: sub-cent micropayments with
   second-level finality, so an agent can pay for one fetch without a monthly
   plan or a trusted middleman.
2. Let anyone with residential or datacenter bandwidth earn directly for the
   bytes they carry, with an immutable, publicly verifiable 80/20
   node/protocol revenue split.
3. Remove custodial and platform risk: capped time-boxed escrow holds protect
   both sides, the coordinator cannot move funds or read traffic, and every
   settlement is an auditable contract event.
4. Keep the network safe by construction: signed node claims, enforced exit
   policies (abuse ports such as SMTP blocked at the node), SSRF protection,
   and a published acceptable-use policy with an abuse-delisting process.

**Project Category**
Developer Tools / Infrastructure (AI-adjacent data-access layer). If the
dropdown only offers broad buckets, choose **Infrastructure**; if an **AI**
category exists, select it as secondary. dmesh is infrastructure that AI
agents consume, not an AI model itself.

**Current Traction**
Stage: working testnet MVP, shipped and continuously deployed.

- A live, publicly reachable network: web app at
  https://emrealt34.github.io/stellar-dvpn/ and coordinator API at
  https://dmesh-coordinator.onrender.com (health, node directory and public
  pricing endpoints).
- 1 live registered node at draft time (Türkiye, residential tier, cone NAT,
  hole-punch reachability, signed claim, heartbeat-monitored and rendered on
  the live world map). Node onboarding is self-service via the open-source CLI.
- 15+ settlement-related testnet transactions across two full on-chain
  lifecycle runs, plus 5 deployment/configuration transactions — all linked
  in the evidence table below and independently verifiable on Horizon/any
  explorer. No mainnet TVL yet; testnet XLM has no market value, so no TVL is
  claimed.
- Proven on-chain escrow flow: capped spend hold → metered settlement with the
  exact 80/20 split → automatic remainder refund → TTL reclaim, with early and
  double-reclaim rejected by the contract (captured transcript in
  `docs/test-runs/2026-10-28-spend-hold.txt`).
- Proven data plane: a real end-to-end P2P run fetched example.com through a
  community node (577-byte response, counters reported, port-25 exit refused),
  transcript in `docs/test-runs/2026-10-27-p2p-demo.txt`.
- Engineering discipline visible to reviewers: seven CI workflows including
  Go/Rust build and test, gitleaks secret scanning, `npm audit`, `cargo audit`,
  advisory-based `govulncheck`, daily dependency reporting and a documented
  two-round security audit (`docs/SECURITY_AUDIT.md`).
- Windows and Linux GUI (PWA) plus CLI clients shipped; macOS and mobile are
  PWA-shell pending. Documentation, terms of service and acceptable-use policy
  are public in the repository.

**Website**
https://emrealt34.github.io/stellar-dvpn/
(Coordinator API: https://dmesh-coordinator.onrender.com ·
Code: https://github.com/emrealt34/stellar-dvpn)

**Planned Stellar Integration**
Stellar is the settlement and trust layer of dmesh, not an afterthought:

- **Soroban settlement contract (live on testnet):** session opening with a
  capped, time-boxed escrow hold (`open_session_with_hold`), dual-signature
  settlement proposals, deterministic byte-based pricing math, the immutable
  2000 bps (20%) protocol fee, and event emission for every state change.
- **Standard asset contracts:** settlement currently uses the XLM SAC
  (`CDLZFC3…GCYSC`). The price-oracle storage is already multi-asset, so
  USDC and other Stellar stablecoin SACs can be enabled by an admin price
  feed without contract redeployment — the planned mainnet rail for
  stable-denominated pricing.
- **Non-custodial design:** the coordinator holds no wallet key and cannot
  move value; only the contract can release escrowed funds, and only against
  counters both peers signed.
- **Stellar-native properties used deliberately:** ~second finality and
  fraction-of-a-cent fees make per-request micropayments economically sane;
  SAC allowances let agents pre-authorize a cap without depositing into a
  custodian; on-chain events give every participant an immutable receipt.
- Planned next integrations: stablecoin price feeds, on-chain node
  reputation/staking claims, and relayer-sponsored fee abstraction so agents
  can pay entirely in stable assets.

**Build Track**
Open Track.

### Team Information

**Submitter type**
Individual / solo builder.

**Email**
[Hasan: paste the email you monitor — this is the SCF contact address.]

**Team Description**
One person today: Hasan Altinok, full-stack and systems engineer who designed
and built every layer of dmesh — the Go coordinator and P2P data plane
(authenticated tunnels, RFC 5389 STUN/NAT classification, SSRF guard and exit
policy), the Soroban/Rust settlement contract including the capped-escrow
hold logic, the Astro/React web client with a live node map, and the full
CI/CD pipeline (seven GitHub Actions workflows, signed release artifacts).
The project is intentionally small and fully executable by one engineer; the
SCF grant funds audits, legal review and community onboarding rather than
headcount. [Hasan: add 1–2 sentences on prior companies/projects and paste
your LinkedIn URL; GitHub is https://github.com/emrealt34.]

### Target Markets & Jurisdictions

**Countries/regions targeted**
Select **Global / Not region-specific**. dmesh is developer infrastructure
usable from any jurisdiction with internet access; it has no region-locked
features and no local-currency rails. If the form requires at least one
concrete market, additionally select **Türkiye** (builder's home market and
first live node) and **United States** (largest AI-agent/scraping buyer
market), but the product itself is launched globally in English.

**Where the team is based**
Türkiye (Istanbul).

**Local-currency / local payment involvement**
No. All settlement is in Stellar digital assets (XLM SAC today, stablecoin
SACs planned); there is no fiat on/off-ramp, local currency instrument or
money-transmission component in the product. Nodes provide generic
`payout_address` entries and choose their own off-ramp.

---

## B. Problem and solution narrative

### Problem
AI agents and data scrapers need reliable, distributed access to the public
web, but the available options are centralized proxy and scraping APIs sold as
subscriptions with opaque pricing, static API keys and monthly commitments.
Agents cannot economically pay for a single request on their own, while the
community operators who actually provide residential and datacenter capacity
have no simple way to be paid fairly for exactly the bytes they carry.

### Solution
dmesh connects agents and scrapers directly to community-run nodes over an
authenticated peer-to-peer tunnel. Every session is metered in bytes and
settled by a Soroban smart contract, so an agent pays for exactly the data it
retrieves — no subscription, no centralized API key, no trusted middleman
holding funds.

Why Stellar makes this practical:

- **Sub-cent micropayments at ~second finality.** Frequent per-request
  settlements only work when fees are negligible and confirmation is fast;
  Stellar testnet transactions confirm in seconds for fractions of a cent.
- **Soroban escrow (`open_session_with_hold`).** A session locks a capped,
  time-boxed spend hold from the client's own SAC allowance. The node gets a
  guaranteed claim up to the cap, the client can never be overcharged, and the
  remainder is refunded automatically.
- **Stable-asset settlement.** The contract's price storage is multi-asset by
  design, starting with the XLM SAC and ready for stablecoin SACs, so pricing
  can stay stable while rails stay native.
- **Non-custodial by construction.** The coordinator has no wallet key. It
  only signals sessions, serves the node directory and collects signed
  counters; the contract is the sole authority that can move value, with an
  immutable 20% protocol fee and full event history.

### What makes dmesh different
Unlike centralized scraping APIs, dmesh has no subscription, no central proxy
fleet and no custodial balance: agents settle micropayments peer to peer under
contract-enforced rules, and node operators earn directly for the bytes they
carry. Unlike "decentralized VPN" projects that keep a hosted data plane and a
backend trust anchor, dmesh's coordinator cannot carry traffic, classify
geography, or move funds — those capabilities are split between the peers and
the Soroban contract by design.

---

## C. Detailed roadmap

### Phase 0 — Testnet MVP (done, October 2026)
- Signaling-only coordinator (directory, liveness heartbeats, signed claims);
  the hosted proxy was deliberately removed and the data plane is fully P2P.
- Soroban v2 settlement contract with capped escrow holds, 80/20 split,
  TTL reclaim and replay protection; two complete live testnet lifecycle runs
  captured as evidence.
- Node CLI with STUN NAT discovery (public/cone/symmetric + CGNAT), signed
  reachability claims, SSRF guard and exit policy (port 25 and other abuse
  ports blocked).
- Web app with live world map, tier-colored NAT markers and node table;
  Windows/Linux CLI plus installable PWA; public ToS/AUP and abuse process.
- Seven CI workflows and a two-round documented security audit.

### Phase 1 — Testnet hardening and first external users (Q4 2026 – Q1 2027)
- **Agent SDK:** shrink integration to a few lines of Python/TypeScript
  (`client.get(url)` that handles signaling, tunnel, allowances and
  settlement), plus two reference scrapers published as examples.
- **Allowance precheck** before a session is marked open, with readable
  insufficient-balance errors and automatic top-up guidance.
- **Onboarding drive:** 10+ live nodes across 5+ countries via operator
  bounties; guided docs and a node-operator checklist (exit policy, AUP
  acceptance, payout setup).
- **Observability:** public Grafana-style status page from coordinator
  heartbeats and contract events; per-node reliability and success-rate
  scoring surfaced to agents.
- **Success metrics:** 50+ settled testnet sessions, 5+ countries live,
  median session setup < 5 seconds, zero custodial-key incidents (by design).

### Phase 2 — Reachability and stable-money rails (Q1 – Q2 2027)
- **NAT traversal hardening:** UPnP/PCP port mapping, TCP simultaneous open,
  and a community-operated relay tier for symmetric/CGNAT nodes, with
  on-chain metering of relay bandwidth and a relay share of the fee.
- **Stablecoin settlement:** enable USDC (and compatible) SACs through the
  existing multi-asset price storage, with an admin/oracle price feed and
  stable-denominated tier prices.
- **Reputation layer:** on-chain node availability and dispute history,
  slashing-free quality scoring, and optional operator stake claims.
- **Success metrics:** 80% of nodes reachable without a relay; relay fallback
  covering the rest; first stablecoin-denominated end-to-end session.

### Phase 3 — Mainnet readiness (Q2 – Q3 2027)
- Independent **third-party security audit** of the Soroban contract and the
  P2P/SSRF surface; public report and remediation window.
- **Legal review** of terms, acceptable-use policy, operator agreements and
  GDPR-style data handling before any mainnet launch.
- Mainnet deployment with immutable fee parameters, runbooks, incident
  response and an abuse-delisting process operated against published SLAs.
- **Success metrics:** audited mainnet contract, legal sign-off, first 100
  mainnet sessions with real per-request payments and zero fund-loss
  incidents.

### Phase 4 — Growth and ecosystem (Q3 2027 onward)
- Native integrations for major agent frameworks (MCP-style tool and
  LangChain/LlamaIndex adapters) so agents buy data access like any API.
- Relayer-sponsored transaction fees so end users never need a wallet;
  fee abstraction settled inside the micropayment flow.
- Operator dashboards, earnings analytics and tax-ready export; regional
  community ambassadors and a public grants program for node coverage gaps.
- **Success metrics:** 1,000+ mainnet sessions/month, 20+ countries with
  live nodes, measurable reuse by at least three external agent projects.

---

## D. Budget and use of SCF funds

Indicative allocation of an Open Track award; exact amounts scale to the
award size and will be reported against milestones.

| Share | Use | Milestone tied to |
| --- | --- | --- |
| 30% | Third-party Soroban contract + P2P security audit | Phase 3 mainnet gate |
| 20% | Legal review (ToS/AUP, operator agreements, data handling) | Phase 3 mainnet gate |
| 20% | Node-onboarding bounties and testnet coverage grants | Phase 1 (10 nodes / 5 countries) |
| 15% | Relay infrastructure and NAT-traversal engineering | Phase 2 relay tier |
| 10% | Agent SDK, reference scrapers, documentation and demo material | Phase 1 developer experience |
| 5% | Contingency / incident response | Ongoing |

No funds are used for token issuance or market making: dmesh has no project
token and no token-sale plan.

### Risks and mitigation
Web scraping and distributed infrastructure carry legal and compliance risks,
including site terms of use, data protection and operator consent. dmesh
mitigates them by design and policy:

- Node operators **explicitly opt in** by signing a registration claim and
  accepting the terms; there is no KYC but there is an auditable mesh identity.
- An **enforced exit policy** blocks spam and abuse categories (email, file
  sharing, legacy remote shells) at the node before any connection is made,
  and private/metadata address ranges are unreachable (SSRF guard).
- A published **acceptable use policy and abuse process**
  (`TERMS.md`, `docs/ACCEPTABLE_USE.md`) supports delisting of abusive nodes;
  on-chain settlement history remains immutable evidence.
- Nodes store no traffic logs beyond the byte counters needed for settlement.
- Budget is allocated for **legal advice before mainnet launch**, covering
  site terms, GDPR-style data handling and operator agreements.
- Technical risk of nodes behind symmetric NAT is mitigated by the Phase 2
  community-relay tier, so the network degrades gracefully instead of
  excluding those operators.

---

## E. Verification and on-chain evidence

### Quick verification

```bash
# 1. local end-to-end P2P demo (coordinator + node + agent proxy + real egress)
DMESH=./dmesh DMESHD=./dmeshd bash scripts/p2p-demo.sh

# 2. NAT classification against real STUN servers
dmesh nat-probe

# 3. live network state
curl https://dmesh-coordinator.onrender.com/api/v1/nodes
```

### Testnet evidence (2026-10-28)

Network: Test SDF Network ; September 2015.
Settlement contract `CD3CYW3VGVCQBQUPLQTTWJPLYB6DNARFQBOSFLTK2B55XWLJDDVDBMMK`;
XLM SAC `CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC`.
Every hash is independently viewable on Horizon or any Stellar testnet
explorer (Stellar Expert: prefix the hash at
`https://expert.testnet.stellar.expert/tx/<hash>`).

| Transaction hash | Action |
| --- | --- |
| `f314e28893ebf3724dcabd9e9b728892c07f621c7dc6cbe3e9f5dd74ef0d27f3` | v2 contract deploy (spend-hold enabled) |
| `6aee6f9033cc3ed8cd04d37461b6d78bb4249478b873b00a8374cba0b633a18a` | `initialize`, protocol fee 2000 bps |
| `13cb1f64fdeeaeb34d4d51d0ee67c242da873c59f3901b4bdfc5dcc74d64f833` | `set_tier` residential 150000 micro-USD/GB |
| `0add8de9f09c9872191c619621c53518e66c6487518ad5b730176d634e38680c` | `set_tier` datacenter 30000 micro-USD/GB |
| `4e18582631200d5c6b9e7030022e78d5e7bce78bf9709b3c937363abed9822c0` | XLM `set_asset_price` 100000 micro-USD/token |
| `6a53e9583cec188277d61989dc02b2b87faff75676a278e3aba71eff66692b23` | `open_session_with_hold`, 5 XLM escrow locked |
| `b0db99b6f0fd6be02dc81512ff2c336b8d57715a6b784e49932bfc315e06daa0` | Escrow settlement, 200 MB, 80/20 split, 4.7 XLM auto-refund |
| `29f455c4856aff9162938dff213c7fc2b5be0fe30e0aa6ee8c3b033ff37cffa6` | Short TTL hold, 1 XLM; early reclaim refused on chain |
| `ddecc30a034d6680bb6f8e00256fd878d1181f9f3e531d41f483606369f404da` | TTL reclaim in full; subsequent double-reclaim refused |

Full transcripts with simulated rejection events and exact escrow balance
deltas: [`docs/test-runs/2026-10-28-spend-hold.txt`](test-runs/2026-10-28-spend-hold.txt)
and the non-hold lifecycle run
[`docs/test-runs/2026-10-28-lifecycle-v2.txt`](test-runs/2026-10-28-lifecycle-v2.txt).
P2P data-plane evidence:
[`docs/test-runs/2026-10-27-p2p-demo.txt`](test-runs/2026-10-27-p2p-demo.txt);
live map and homepage screenshots:
[`docs/test-runs/screenshots/`](test-runs/screenshots/).

### Submission checklist
- [ ] Replace every `[Hasan: …]` item (email, LinkedIn, background lines).
- [ ] Record and link the 3-minute demo video (nat-probe → register →
  tunnel-listen → agent request → on-chain settlement).
- [ ] Refresh node/session/transaction counts on the day of submission.
- [ ] Confirm the Render coordinator points at the v2 contract
      (`CD3CYW3…BMMK`) and re-check `/api/v1/health`.
- [ ] Select jurisdictions and category in the live dropdowns; attach the
      security audit and architecture docs if an attachments field exists.
