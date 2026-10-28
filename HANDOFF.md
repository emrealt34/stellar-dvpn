# Handoff — dmesh session state (2026-10-28)

Read this first when continuing in a new chat. Repo: `emrealt34/stellar-dvpn`.
Live site (GitHub Pages): https://emrealt34.github.io/stellar-dvpn/ ·
coordinator (Render): https://dmesh-coordinator.onrender.com ·
branch `main`, remote is the source of truth.

## State of play

**Spend-hold is now proven end-to-end on live testnet.** The previously
deployed contract lacked `open_session_with_hold`; a fresh v2 wasm (built by
CI `contract.yml` from the same source, artifact of run 40526426400) was
deployed and fully configured:

- v2 settlement contract:
  `CD3CYW3VGVCQBQUPLQTTWJPLYB6DNARFQBOSFLTK2B55XWLJDDVDBMMK`
  (deploy tx `f314e288…0d27f3`, initialize tx `6aee6f90…33a18a`,
  fee 2000 bps; res 150000 / dc 30000 micro-USD per GB; XLM SAC price
  100000 micro-USD/token).
- XLM SAC: `CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC`.
- Admin identity `dmesh-admin` =
  `GATPSIQXSTWHXCWAKPRYZ2SGCXBNNVZQQPWUQP77MEJ3KN3UFLM5OTRO`. Its identity
  TOML (seed phrase) is backed up at `data/dmesh-v2-admin.toml`, which is
  gitignored — verify it survives; the secret never enters the repo.
- Old contract `CCBNQXDZ…YHOT` is superseded; README/SCF/ContractInfo.astro/
  .env.example now reference the v2 address (old ID kept once in README as a
  superseded note).

Two live testnet runs are green against v2 and saved under
`docs/test-runs/`:

- `2026-10-28-spend-hold.txt` — `TestTestnetSpendHoldLifecycle`: 5 XLM
  escrow lock (exact delta), 200 MB settlement from escrow (3 XLM charge;
  node ~2.384m / fee 600k stroops; full remainder auto-refunded; escrow back
  to baseline), 1 XLM 30 s TTL hold with early-reclaim refusal, full TTL
  reclaim, double-reclaim refusal.
- `2026-10-28-lifecycle-v2.txt` — `TestTestnetSettlementFullLifecycle`:
  approve → open → propose → dual-sig confirm 80/20 → cancel, all pass.

Live node re-registered after the sandbox rebuild:
`GDPPX5FALV7VWWBU5HSERFJDIP62UJ5ILIFPEQWFBPZVASJPRQG7Q7JX` (TR, res, cone,
hole-punch, client-signed), heartbeat loop running in-process; fresh
screenshots in `docs/test-runs/screenshots/2026-10-28-*.png` (map shows the
green residential marker over Türkiye; no console errors).

## Outstanding work

1. **Render env update (needs Hasan, dashboard only — no Render API token
   exists here):** set `SETTLEMENT_CONTRACT_ID` to
   `CD3CYW3VGVCQBQUPLQTTWJPLYB6DNARFQBOSFLTK2B55XWLJDDVDBMMK` and redeploy.
   Until then the coordinator still points at the old contract for any
   on-chain read paths. Verify via `/api/v1/health` afterwards.
2. Commit/push the hold test + doc/screenshot updates from this session,
   then confirm Pages redeploy shows the v2 address in the Contract section.
3. SCF (`docs/SCF_APPLICATION.md`): tx table and node numbers are filled
   with real 2026-10-28 values; remaining brackets are the demo video link,
   Hasan's background/prior-projects link, and a final count refresh right
   before submission.
4. MaxMind: `geoip-latest` release needs `MAXMIND_LICENSE_KEY` repo secret +
   one manual `geo-db-refresh` dispatch (ask Hasan).
5. Record the 3-minute demo video (nat-probe → register → tunnel-listen →
   agent request → on-chain settlement).
6. Roadmap: community relay for symmetric/CGNAT nodes; UPnP/PCP + TCP
   simultaneous open; allowance precheck before `opened`.
7. **Rotate the GitHub PAT used earlier — it appeared in chat; gitleaks
   confirms it is not stored in the repo.**

## Environment gotchas (sandbox was rebuilt again mid-work)

- Reinstalled: Go 1.27.3 at `~/.local/go` (`GOMODCACHE=~/.local/gomodcache`),
  Node v22.23.4 at `~/.local/node22`, Playwright chromium-headless-shell
  (system libs + `fonts-noto-color-emoji` via passwordless sudo),
  stellar-cli v28.1.0 at `/tmp/stellar` (ephemeral). None of `/tmp` or
  `~/.local` survives a rebuild — reinstall first if absent.
- 2 GB RAM: `GOMAXPROCS=1 GOGC=20 GOMEMLIMIT=1300MiB`, build with `-p 1`.
  `govulncheck` OOMs locally; the CI advisory job is the gate (chi v4
  GO-2026-4316 is transitive, outside the call graph).
- Live e2e test pattern:
  `DMESH_E2E=1 DMESH_CONTRACT_ID=CD3CYW3… DMESH_XLM_CONTRACT=CDLZFC3…
  DMESH_ADMIN_SECRET=<S-key from `stellar keys show dmesh-admin`>
  go test -v -run TestTestnet… ./internal/soroban/`
  (hold test does not need the admin secret; plain lifecycle does).
- SAC gotcha: a Stellar-address `balance` view of the XLM SAC includes the
  native XLM reserve, so absolute client SAC balances are meaningless for
  refund assertions — measure the contract's own escrow deltas (the test
  already does this).
- No Rust toolchain; contract wasm comes from CI artifacts, never hand-built
  or hand-assembled XDR. Stellar Expert testnet DNS does not resolve in the
  sandbox; use Horizon + CLI links for evidence.

## Key invariants — do not regress

- No money custody on the coordinator; 20% (2000 bps) fee immutable in
  contract; no KYC, email OTP only for the dashboard; English copy; no
  project token (XLM SAC only); generic `payout_address` (XMR removed).
- Geo/tier/reachability are client-derived and Stellar-signed; the
  coordinator only validates shape and stores claims; raw observed IPs never
  leave the server.
- Data plane stays P2P; `netguard` (public-only + exit-port denylist incl.
  port 25) runs on the node; no fetch/CONNECT routes may be re-added to the
  coordinator.
- Fresh-schema rule: add columns to the CREATE TABLE, never write migrations.
- Node 22.23.4 pinned for Astro 7 (CI and local). Secrets never enter the
  repo; gitleaks + npm audit stay green in CI.
