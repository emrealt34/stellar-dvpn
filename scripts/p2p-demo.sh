#!/usr/bin/env bash
# End-to-end P2P data-plane demo, entirely local except the final public egress.
#
#   coordinator (signaling only)            node (P2P tunnel)         client
#   dmeshd on 127.0.0.1:18080  <== auth/counters ==  tunnel-listen  <== CONNECT == tunnel proxy
#                                                                              |
#                                            https://example.com  <-- egress --+
#
# The coordinator never carries user traffic. The node enforces netguard
# (public destinations only) and the exit policy (port 25 and other abuse
# ports are refused). Set DMESH=/path/to/dmesh and DMESHD=/path/to/dmeshd, or
# let the script use the binaries on PATH.
set -euo pipefail

DMESH="${DMESH:-dmesh}"
DMESHD="${DMESHD:-dmeshd}"
BASE="http://127.0.0.1:18080"
NODE_TUNNEL="127.0.0.1:17777"
LOCAL_PROXY="127.0.0.1:11080"
WORK="$(mktemp -d)"
trap 'kill "${COORD_PID:-0}" "${NODE_PID:-0}" "${PROXY_PID:-0}" 2>/dev/null || true; rm -rf "$WORK"' EXIT

say() { printf '\n\033[1;36m== %s ==\033[0m\n' "$1"; }
need() { command -v "$1" >/dev/null || { echo "missing $1" >&2; exit 1; }; }
need "$DMESH"; need "$DMESHD"; need curl; need python3

say "1. Start the signaling coordinator (no data plane)"
PORT=18080 DB_PATH="$WORK/demo.db" DEV_OTP_CODES=true \
  JWT_SECRET="local-demo-secret-0123456789abcdef-32bytes" \
  "$DMESHD" >"$WORK/coord.log" 2>&1 &
COORD_PID=$!
for _ in $(seq 1 50); do
  curl -sf "$BASE/api/v1/health" >/dev/null && break
  sleep 0.2
done
curl -s "$BASE/api/v1/health" | python3 -m json.tool

say "2. Generate node and client mesh identities"
NODE_KEY="$("$DMESH" keygen)"
CLIENT_KEY="$("$DMESH" keygen)"
NODE_SEED="$(printf '%s' "$NODE_KEY" | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret_seed"])')"
CLIENT_SEED="$(printf '%s' "$CLIENT_KEY" | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret_seed"])')"
NODE_ADDR="$(printf '%s' "$NODE_KEY" | python3 -c 'import sys,json;print(json.load(sys.stdin)["mesh_address"])')"
echo "node   $NODE_ADDR"
echo "client $(printf '%s' "$CLIENT_KEY" | python3 -c 'import sys,json;print(json.load(sys.stdin)["mesh_address"])')"

say "3. Local STUN NAT probe (real public STUN servers)"
"$DMESH" nat-probe | tee "$WORK/nat.json"

say "4. Node registers a signed geo + reachability claim"
"$DMESH" register --coordinator "$BASE" --secret "$NODE_SEED" \
  --tier res --country-iso TR --country Turkiye --asn 9121 --asn-org "Demo Network" \
  --payout "$NODE_ADDR" --nat-probe --external "$NODE_TUNNEL" | python3 -m json.tool

say "5. Public directory (raw transport IPs are never exposed)"
curl -s "$BASE/api/v1/nodes" | python3 -m json.tool

say "6. Client coordinates a session and attaches the open_session record"
COORD="$("$DMESH" session --coordinator "$BASE" --secret "$CLIENT_SEED" \
  --node "$NODE_ADDR" --token CTESTTOKEN)"
NONCE="$(printf '%s' "$COORD" | python3 -c 'import sys,json;print(json.load(sys.stdin)["open_session_args"]["nonce_hex"])')"
echo "nonce $NONCE"
"$DMESH" attach --coordinator "$BASE" --secret "$CLIENT_SEED" \
  --nonce "$NONCE" --contract-id 1 --tx-hash local-demo-open-tx | python3 -m json.tool

say "7. Bring up the node P2P tunnel and the client's local CONNECT proxy"
"$DMESH" tunnel-listen --coordinator "$BASE" --secret "$NODE_SEED" \
  --listen "$NODE_TUNNEL" >"$WORK/node.log" 2>&1 &
NODE_PID=$!
"$DMESH" tunnel --coordinator "$BASE" --secret "$CLIENT_SEED" \
  --nonce "$NONCE" --node-tcp "$NODE_TUNNEL" --local "$LOCAL_PROXY" >"$WORK/proxy.log" 2>&1 &
PROXY_PID=$!
sleep 1

say "8. Real public egress through the authenticated P2P tunnel"
curl -sS -x "http://$LOCAL_PROXY" https://example.com -o "$WORK/page.html" \
  -w 'HTTP %{http_code}, %{size_download} bytes via %{remote_ip}\n'
grep -o '<title>[^<]*</title>' "$WORK/page.html"

say "9. Exit policy: CONNECT to blocked SMTP port 25 must fail"
if curl -sS --max-time 8 -x "http://$LOCAL_PROXY" "https://example.com:25/" -o /dev/null; then
  echo "FAIL: port 25 was reachable" >&2
  exit 1
else
  echo "OK: connection to port 25 refused by the node exit policy"
fi

say "All checks passed. Logs: $WORK"
