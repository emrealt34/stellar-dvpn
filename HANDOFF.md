# Handoff — dmesh session state (2026-10-27)

Read this first when continuing in a new chat. Repo: `emrealt34/stellar-dvpn`.
Live site (GitHub Pages): https://emrealt34.github.io/stellar-dvpn/ ·
coordinator (Render): https://dmesh-coordinator.onrender.com ·
branch `main`, remote is the source of truth.

## What this session changed (push after committing)

1. **Hosted coordinator proxy removed (H-3).** `internal/agent`,
   `/api/v1/agent/fetch`, `/api/v1/agent/connect`, the CLI `agent` command and
   `api.New`'s agent parameter are gone. The coordinator is signaling +
   directory + liveness only; it carries zero user bytes.
2. **NAT/STUN package `internal/nat`.** RFC 5389 client (no third-party deps),
   classifies `public` / `cone` / `symmetric` / `udp-blocked`, detects RFC6598
   CGNAT, maps to `direct` / `hole-punch` / `relay-required`. Verified live
   against Google + Cloudflare STUN (sandbox: cone, 136.66.142.110).
3. **Signed reachability claims.** Node registration now signs
   `endpoint`, `nat_type`, `connectivity`, `cgnat` (new nodes table columns;
   fresh schema, no migration). Enum + host:port validation; tampering → 401;
   test `TestNodeReachabilityClaimRoundTrip`.
4. **CLI:** new `nat-probe`, `attach`, `--nat-probe/--external` on register,
   `tunnel-listen --register/--external` with auto STUN probe and endpoint
   derivation. `scripts/p2p-demo.sh` runs the full local E2E; transcript in
   `docs/test-runs/2026-10-27-p2p-demo.txt` (real example.com fetch through
   P2P, port 25 blocked, counters recorded).
5. **Web:** vendored 84 KB world GeoJSON (`web/src/data/land.json`) +
   centroids → new `WorldMap.jsx` with pulsing tier-colored markers, relay
   ring for symmetric/CGNAT; Network table shows NAT/endpoint; Hero/Model
   reframed from VPN to agent/scraper data access; Stellar Expert links;
   footer Terms/AUP/abuse links.
6. **Docs/policy:** `TERMS.md`, AUP already existed, `AGENT_API.md` rewritten
   for P2P, round-2 entries in `SECURITY_AUDIT.md`, SCF draft in
   `docs/SCF_APPLICATION.md` (fill bracketed numbers + demo video).
7. `.gitleaks.toml` allowlists test files only; full-history gitleaks clean;
   `npm audit` 0 vulns.

## Environment gotchas (the sandbox was rebuilt mid-work)

- Go toolchain and Node 22 were missing; reinstalled to
  `~/.local/go` (go1.27.3) and `~/.local/node22` (v22.23.4). They are outside
  the persisted snapshot — reinstall first thing if absent.
- The old Go module cache was corrupt; `go env -w GOMODCACHE=$HOME/.local/gomodcache`
  plus `go clean -modcache && go mod download` fixed it.
- 2 GB RAM: build/test with `GOMAXPROCS=1 GOGC=20 GOMEMLIMIT=1300MiB -p 1`.
  Full `govulncheck` OOM-kills locally — rely on the advisory CI job
  (only known advisory: transitive chi v4 GO-2026-4316, not in call graph).
- No browser is installed. For screenshots: try `npx playwright install
  chromium-headless-shell` (sudo works passwordless if system libs are
  needed) or add Playwright to `web/` devDeps and run in CI as an artifact job.
- Rust/cargo is not installed; contract changes from the previous session
  (spend-hold escrow) are validated by `contract.yml` in CI, not locally.

## Explicit next steps for the new chat

1. **Commit and push this work**, then wait for Render redeploy (~2-4 min).
   The live register call currently 401s until the new signed-parts schema is
   deployed. Then run a REAL live registration (key already created at
   `/tmp/live/node.json`, ephemeral — regenerate if gone):
   `dmesh register --coordinator https://dmesh-coordinator.onrender.com ...
   --nat-probe --external <ip>:7777`, keep a heartbeat loop alive for
   screenshots, confirm the node appears on the live map.
2. **Screenshots/demo video** of: live map with node + NAT badge,
   `scripts/p2p-demo.sh` run, Stellar Expert contract page, spend-hold tx.
3. **MaxMind:** `geoip-latest` release does not exist yet because
   `MAXMIND_LICENSE_KEY` is not a repo secret. Ask Hasan to add it, then
   manually dispatch the `geo-db-refresh` workflow once.
4. **Contract spend hold end-to-end on testnet with funded keys** (previous
   sessions did plain lifecycle; the hold path needs an XLM SAC allowance +
   `open_session_with_hold`). Document the tx hashes in SCF numbers.
5. SCF: replace every `[bracket]`, record real node/session/tx counts,
   record the 3-minute demo video, add Hasan's GitHub/background.
6. Roadmap: community (non-coordinator) relay for symmetric/CGNAT nodes;
   UPnP/PCP + TCP simultaneous open; allowance precheck before `opened`.
7. Rotate the GitHub PAT used during this work — it appeared in chat; it is
   not stored anywhere in the repo (gitleaks-verified).

## Key invariants — do not regress

- No money custody on the coordinator; 20% (2000 bps) fee is immutable in
  contract; no KYC, email OTP only for the dashboard; English copy.
- Geo/tier/reachability are client-derived and Stellar-signed; the
  coordinator only validates shape and stores claims; raw observed IPs never
  leave the server.
- Data plane stays P2P; `netguard` (public-only + exit-port denylist) runs on
  the node; no fetch/CONNECT routes may be re-added to the coordinator.
- Fresh-schema rule: add columns to the CREATE TABLE, never write migrations.
- Node 22.23.4 is pinned for Astro 7 (both CI and local).
