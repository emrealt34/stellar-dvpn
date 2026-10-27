# Stellar Community Fund application — working draft

**Track:** Open Track
**Project:** dmesh
**Team:** Hasan Altinok
**Date:** 2026-10-27

> Fill every `[bracketed]` item with the live links and numbers captured at
> submission time. Keep the tone factual; every claim here maps to a shipped
> artifact in the repository.

## One-line pitch

dmesh is a decentralized network that gives scraping pipelines and AI agents
pay-per-request access to web data, with micropayments settled on Stellar.

## Problem

AI agents and data scrapers need reliable, distributed access to the public
web, but the available options are centralized proxy and scraping APIs sold as
subscriptions with opaque pricing, static API keys and monthly commitments.
Agents cannot economically pay for a single request on their own, while the
community operators who actually provide residential and datacenter capacity
have no simple way to be paid fairly for exactly the bytes they carry.

## Solution and why Stellar

dmesh connects agents and scrapers directly to community-run nodes over an
authenticated peer-to-peer tunnel. Every session is metered in bytes and
settled by a Soroban smart contract, so an agent pays for exactly the data it
retrieves — no subscription, no centralized API key, no trusted middleman
holding funds.

Stellar makes the model practical:

- **Sub-cent micropayments at ~second finality.** Frequent per-request
  settlements only work when fees are negligible and confirmation is fast;
  Stellar testnet transactions confirm in seconds for fractions of a cent.
- **Soroban escrow (`open_session_with_hold`).** A session locks a capped,
  time-boxed spend hold from the client's own allowance. The node gets a
  guaranteed claim up to the cap, the client can never be overcharged, and the
  remainder is refunded automatically.
- **Stable-asset settlement.** The contract settles in Stellar assets the
  parties already hold, starting with the XLM standard asset contract, so
  pricing can stay stable while rails stay native.
- **Non-custodial by construction.** The coordinator has no wallet key. It
  only signals sessions, serves the node directory and collects signed
  counters; the contract is the sole authority that can move value, with an
  immutable 20% protocol fee and full event history.

## Current status

- **Working testnet version:** [https://emrealt34.github.io/stellar-dvpn/](https://emrealt34.github.io/stellar-dvpn/)
- **Coordinator API:** [https://dmesh-coordinator.onrender.com](https://dmesh-coordinator.onrender.com)
  ([health](https://dmesh-coordinator.onrender.com/api/v1/health),
  [live directory](https://dmesh-coordinator.onrender.com/api/v1/nodes),
  [pricing](https://dmesh-coordinator.onrender.com/api/v1/public/pricing))
- **Stellar testnet transactions:** [Settlement contract on Stellar Expert](https://expert.testnet.stellar.expert/contract/CCBNQXDZIOYBSFRSXBSNMCLANJG6H6EK4HFTO2A7EJR4BJD2HGM3YHOT)
  — settlement `CCBNQXDZIOYBSFRSXBSNMCLANJG6H6EK4HFTO2A7EJR4BJD2HGM3YHOT`,
  XLM SAC `CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC`
- **Demo video:** [link to recording — 3 minutes: nat-probe → register →
  tunnel-listen → agent request through the P2P proxy → on-chain settlement]
- **GitHub repository:** [https://github.com/emrealt34/stellar-dvpn](https://github.com/emrealt34/stellar-dvpn)
- **Numbers (update at submission):** [N] registered test nodes, [N] countries,
  [N] testnet sessions, [N] Soroban transactions, [X] KiB metered in the
  reference end-to-end run (`docs/test-runs/2026-10-27-p2p-demo.txt`).

### What is demonstrably working today

- Full session lifecycle on testnet: coordinate → on-chain open (with optional
  capped spend hold) → metered P2P traffic → dual-signature settlement with
  the 80/20 node/protocol split → cancel/reclaim paths.
- A real P2P data plane: the coordinator carries zero user bytes; the node
  enforces an SSRF guard and a Tor-style exit policy (SMTP, SMB, NFS and other
  abuse ports blocked), validated against real public egress and a blocked-port
  test.
- Client-derived geography and tier: both peers run the same deterministic
  GeoLite2 classification locally and independently agree; the backend never
  classifies an IP.
- STUN-based NAT discovery (`public` / `cone` / `symmetric`, including CGNAT
  detection) with signed reachability claims and a live world map of nodes.
- CI security gates: `govulncheck`, `npm audit`, `cargo audit`, gitleaks and
  daily dependency reports; documented severity-classified security audit.

## What makes us different

Unlike centralized scraping APIs, dmesh has no subscription, no central proxy
fleet and no custodial balance: agents settle micropayments peer to peer under
contract-enforced rules, and node operators earn directly for the bytes they
carry. Unlike "decentralized VPN" projects that keep a hosted data plane and a
backend trust anchor, dmesh's coordinator cannot carry traffic, classify
geography, or move funds — those capabilities are split between the peers and
the Soroban contract by design.

## Plan with SCF funding

- **MVP (current):** an agent sends a request through a community node and
  pays per byte, with escrowed spend holds and an enforceable abuse/exit
  policy. Shrink the agent SDK to a few lines and publish reference scrapers.
- **Testnet scale-out:** onboard [X] nodes across [Y] countries, harden NAT
  traversal (UPnP/PCP, simultaneous open) and add a community-operated relay
  for symmetric/CGNAT nodes, with on-chain metering for relay bandwidth.
- **Mainnet:** independent security review of the contract, legal review of
  the acceptable-use and operator framework, stablecoin settlement options,
  and a public launch with real per-request payments and published SLAs.

## Risks and mitigation

Web scraping and distributed infrastructure carry legal and compliance risks,
including site terms of use, data protection and operator consent. dmesh
mitigates them by design and policy:

- Node operators **explicitly opt in** by signing a registration claim and
  accepting the terms; there is no KYC but there is an auditable mesh identity.
- An **enforced exit policy** blocks spam and abuse categories (email, file
  sharing, legacy remote shells) at the node before any connection is made, and
  private/metadata address ranges are unreachable.
- A published **acceptable use policy and abuse process**
  (`TERMS.md`, `docs/ACCEPTABLE_USE.md`) supports delisting of abusive nodes;
  on-chain settlement history remains immutable evidence.
- Nodes store no traffic logs beyond the byte counters needed for settlement.
- Budget is allocated for **legal advice before mainnet launch**, covering
  site terms, GDPR-style data handling and operator agreements.

## Team

- **Hasan Altinok** — design, Go backend and P2P data plane, Soroban contract,
  Astro/React client and CI/CD. [GitHub profile link] [background / prior
  projects link]

## Appendix: quick verification

```bash
# 1. local end-to-end P2P demo (coordinator + node + agent proxy + real egress)
DMESH=./dmesh DMESHD=./dmeshd bash scripts/p2p-demo.sh

# 2. NAT classification against real STUN servers
dmesh nat-probe

# 3. live network state
curl https://dmesh-coordinator.onrender.com/api/v1/nodes
```
