// Command dmesh is the reference mesh client and node CLI. Keys are
// generated and held locally; every on-chain action is signed on this
// machine and the coordinator never sees a secret seed.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"dmesh/internal/cliclient"
	"dmesh/internal/geo"
	"dmesh/internal/nat"
	"dmesh/internal/soroban"
	"dmesh/internal/tunnel"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "keygen":
		err = keygen()
	case "register":
		err = register(args)
	case "verify-node":
		err = verifyNode(args)
	case "reclaim":
		err = reclaim(args)
	case "tunnel-listen":
		err = tunnelListen(args)
	case "tunnel":
		err = tunnelClient(args)
	case "heartbeat":
		err = heartbeat(args)
	case "nodes":
		err = nodes(args)
	case "session":
		err = session(args)
	case "counters":
		err = counters(args)
	case "settle":
		err = settle(args)
	case "cancel":
		err = cancel(args)
	case "nat-probe":
		err = natProbe(args)
	case "attach":
		err = attach(args)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `dmesh - trustless mesh client

Commands:
  keygen                       Generate a local Stellar mesh identity
  register   --coordinator URL --secret S... [--geo-country FILE --geo-asn FILE] [--tier res|dc --country-iso TR --country Turkiye --asn 9121 --asn-org "Turk Telekom"] [--payout ADDR] [--ip 1.2.3.4]
  heartbeat  --coordinator URL --secret S... --up N --down N
  nodes      --coordinator URL             Public node directory
  verify-node --coordinator URL --secret S... --mesh G... --ip 1.2.3.4 [--geo-country F --geo-asn F]
  session    --coordinator URL --secret S... --node G... --token C/G... [--node-ip 1.2.3.4] [--ttl 3600] [--open]
  attach     --coordinator URL --secret S... --nonce HEX --contract-id ID --tx-hash HASH
  counters   --coordinator URL --secret S... --nonce HEX --up N --down N
  settle     --rpc URL --secret S... --contract C... --id 123 --up N --down N --role client|node [--coordinator URL --nonce HEX]
  cancel     --rpc URL --secret S... --contract C... --id 123 --as-node [--coordinator URL --nonce HEX]
  reclaim    --rpc URL --secret S... --contract C... --id 123 [--coordinator URL --nonce HEX]   (release expired hold)
  nat-probe  [--stun host:port ...]            Classify local NAT (public/cone/symmetric/CGNAT) via STUN
  tunnel-listen --coordinator URL --secret S... [--listen 0.0.0.0:7777] [--external host:port] [--register]
  tunnel     --coordinator URL --secret S... --nonce HEX --node-tcp host:port [--local 127.0.0.1:1080]

There is no coordinator-side proxy: agents and scrapers send metered traffic
directly to nodes over tunnel-listen/tunnel (the P2P data plane).
`)
}

func keygen() error {
	kp, err := keypair.Random()
	if err != nil {
		return err
	}
	out := map[string]string{
		"mesh_address": kp.Address(),
		"secret_seed":  kp.Seed(),
		"warning":      "save the secret seed offline now; it is shown once and cannot be recovered",
	}
	return printJSON(out)
}

func envOr(flagValue, envKey string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(envKey)
}

func newClient(coordinator, secret string) (*cliclient.Client, error) {
	secret = envOr(secret, "DMESH_SECRET")
	if coordinator == "" || secret == "" {
		return nil, fmt.Errorf("--coordinator and --secret (or DMESH_SECRET) are required")
	}
	return cliclient.New(coordinator, secret)
}

func register(args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "S... secret seed")
	tier := fs.String("tier", "", "manual pricing tier: res|dc (otherwise derived from the geo DB)")
	countryISO := fs.String("country-iso", "", "manual country ISO code")
	countryName := fs.String("country", "", "manual country name")
	asnOrg := fs.String("asn-org", "", "manual ASN organization")
	asn := fs.Uint("asn", 0, "manual ASN number")
	payout := fs.String("payout", "", "optional payout address (informational only)")
	externalIP := fs.String("ip", "", "override the externally observed IP")
	geoCountry := fs.String("geo-country", os.Getenv("GEO_COUNTRY_DB"), "path to GeoLite2-Country.mmdb")
	geoASN := fs.String("geo-asn", os.Getenv("GEO_ASN_DB"), "path to GeoLite2-ASN.mmdb")
	externalEndpoint := fs.String("external", "", "public P2P dial host:port to publish (port forward/UPnP)")
	runProbe := fs.Bool("nat-probe", false, "STUN-probe local NAT and sign nat_type/connectivity into the claim")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}

	claim := cliclient.NodeClaim{
		Tier:        strings.TrimSpace(*tier),
		CountryISO:  strings.TrimSpace(*countryISO),
		CountryName: strings.TrimSpace(*countryName),
		ASNOrg:      strings.TrimSpace(*asnOrg),
		ASN:         *asn,
		Payout:      strings.TrimSpace(*payout),
	}
	if *runProbe || *externalEndpoint != "" {
		// No TCP listener here: the endpoint is the explicit override; the
		// probe still contributes the signed NAT classification.
		claim.Endpoint, claim.NATType, claim.Connectivity, claim.CGNAT, err =
			probeReachability(*externalEndpoint, "")
		if err != nil {
			return err
		}
	}

	// Unless a full manual claim is supplied, classify locally against the
	// CI-published geo databases. The coordinator never performs this lookup.
	if claim.Tier == "" {
		ipStr := strings.TrimSpace(*externalIP)
		if ipStr == "" {
			ipStr, err = c.EchoIP()
			if err != nil {
				return fmt.Errorf("resolving external IP: %w (pass --ip to override)", err)
			}
		}
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return fmt.Errorf("invalid IP %q", ipStr)
		}
		lookup, gerr := geo.Open(*geoCountry, *geoASN)
		if gerr != nil {
			return fmt.Errorf("geo databases unavailable (%w); download them from the geoip-latest release or pass --tier/--country manually", gerr)
		}
		defer lookup.Close()
		info, gerr := lookup.Lookup(ip)
		if gerr != nil {
			return fmt.Errorf("classifying %s: %w", ipStr, gerr)
		}
		claim.Tier = string(info.Tier)
		claim.CountryISO = info.CountryISO
		claim.CountryName = info.CountryName
		claim.ASN = info.ASN
		claim.ASNOrg = info.ASNOrg
	}

	out, err := c.Register(claim)
	if err != nil {
		return err
	}
	return printJSON(out)
}

// tierAgreement is the result of independently re-classifying a node endpoint.
type tierAgreement struct {
	Mesh            string `json:"mesh_address"`
	ClaimedTier     string `json:"claimed_tier"`
	ObservedTier    string `json:"observed_tier"`
	ClaimedCountry  string `json:"claimed_country_iso"`
	ObservedCountry string `json:"observed_country_iso"`
	Agree           bool   `json:"agree"`
}

// classifyIP runs the same deterministic geo classification the node itself
// uses, so the two peers independently derive the same tier from one IP.
func classifyIP(ipStr, countryDB, asnDB string) (geo.Info, error) {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return geo.Info{}, fmt.Errorf("invalid IP %q", ipStr)
	}
	lookup, err := geo.Open(countryDB, asnDB)
	if err != nil {
		return geo.Info{}, err
	}
	defer lookup.Close()
	return lookup.Lookup(ip)
}

// verifyNode independently classifies the node's endpoint and compares it with
// the node's signed directory claim. The coordinator is not involved in the
// decision — it only served the claim — so the two clients must agree.
func verifyNode(args []string) error {
	fs := flag.NewFlagSet("verify-node", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "S... secret seed (signs the read request)")
	mesh := fs.String("mesh", "", "node G address to verify")
	nodeIP := fs.String("ip", "", "the node endpoint's public IP as observed by this client")
	geoCountry := fs.String("geo-country", os.Getenv("GEO_COUNTRY_DB"), "path to GeoLite2-Country.mmdb")
	geoASN := fs.String("geo-asn", os.Getenv("GEO_ASN_DB"), "path to GeoLite2-ASN.mmdb")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mesh == "" || *nodeIP == "" {
		return errors.New("--mesh and --ip are required")
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}
	claim, err := c.NodeByMesh(*mesh)
	if err != nil {
		return err
	}
	observed, err := classifyIP(*nodeIP, *geoCountry, *geoASN)
	if err != nil {
		return err
	}
	result := tierAgreement{
		Mesh:            claim.MeshAddress,
		ClaimedTier:     claim.Tier,
		ObservedTier:    string(observed.Tier),
		ClaimedCountry:  claim.CountryISO,
		ObservedCountry: observed.CountryISO,
		Agree:           claim.Tier == string(observed.Tier),
	}
	if !result.Agree {
		if err := printJSON(result); err != nil {
			return err
		}
		return fmt.Errorf("tier disagreement: node claims %q but independent classification is %q — refusing to trust the node",
			result.ClaimedTier, result.ObservedTier)
	}
	return printJSON(result)
}

// natProbe runs the local STUN classification and prints the JSON report.
func natProbe(args []string) error {
	fs := flag.NewFlagSet("nat-probe", flag.ContinueOnError)
	stunCSV := fs.String("stun", "", "comma-separated STUN host:port list (default built-in)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var servers []string
	if strings.TrimSpace(*stunCSV) != "" {
		for _, s := range strings.Split(*stunCSV, ",") {
			if s = strings.TrimSpace(s); s != "" {
				servers = append(servers, s)
			}
		}
	}
	return printJSON(nat.Classify(servers))
}

// probeReachability runs the STUN probe and, when a TCP listen port is known,
// derives the public dial endpoint candidates. An explicit external override
// always wins. Symmetric/CGNAT paths publish no endpoint because they cannot
// accept inbound connections without an external relay.
func probeReachability(external, listenAddr string) (endpoint, natType, connectivity string, cgnat bool, err error) {
	report := nat.Classify(nil)
	natType, connectivity, cgnat = report.NATType, report.Connectivity, report.CGNAT
	switch {
	case strings.TrimSpace(external) != "":
		if _, _, perr := net.SplitHostPort(external); perr != nil {
			return "", "", "", false, fmt.Errorf("--external must be host:port: %w", perr)
		}
		endpoint = external
	case report.Connectivity == nat.Direct || report.Connectivity == nat.HolePunch:
		if _, port, perr := net.SplitHostPort(listenAddr); perr == nil && report.PublicIP != "" {
			endpoint = net.JoinHostPort(report.PublicIP, port)
		}
	}
	return endpoint, natType, connectivity, cgnat, nil
}

// tunnelListen runs the node-side P2P tunnel server. It authenticates every
// inbound tunnel against the coordinator (signaling only), enforces the SSRF
// and exit-policy guard locally, and meters counters on teardown.
func tunnelListen(args []string) error {
	fs := flag.NewFlagSet("tunnel-listen", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "node S... secret seed")
	listen := fs.String("listen", "0.0.0.0:7777", "address to accept P2P tunnels on")
	external := fs.String("external", "", "public host:port clients dial (override behind port forwarding/UPnP)")
	register := fs.Bool("register", false, "STUN-probe reachability and (re)register this node before serving")
	claimTier := fs.String("tier", "", "with --register: res|dc (else pass a pre-registered node)")
	claimCountry := fs.String("country-iso", "", "with --register: country ISO code")
	claimName := fs.String("country", "", "with --register: country name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	endpoint, natType, connectivity, cgnat, err := probeReachability(*external, ln.Addr().String())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "NAT: type=%s connectivity=%s cgnat=%t endpoint=%s\n",
		natType, connectivity, cgnat, orDash(endpoint))
	if *register {
		claim := cliclient.NodeClaim{
			Tier: *claimTier, CountryISO: *claimCountry, CountryName: *claimName,
			Endpoint: endpoint, NATType: natType, Connectivity: connectivity, CGNAT: cgnat,
		}
		if claim.Tier == "" {
			return errors.New("--register requires --tier (and usually --country-iso/--country) or run 'dmesh register' first")
		}
		if _, err := c.Register(claim); err != nil {
			return fmt.Errorf("register: %w", err)
		}
		fmt.Fprintf(os.Stderr, "registered node %s (signed reachability claim)\n", c.Address())
	}
	fmt.Fprintf(os.Stderr, "P2P tunnel node %s listening on %s\n", c.Address(), ln.Addr())
	srv := &tunnel.NodeServer{KP: c.KP, Verifier: c, Counters: c}
	return srv.Serve(ln)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// tunnelClient runs a local HTTP CONNECT proxy that exits through a node over
// the authenticated P2P data plane; the coordinator carries no traffic.
func tunnelClient(args []string) error {
	fs := flag.NewFlagSet("tunnel", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "client S... secret seed")
	nonce := fs.String("nonce", "", "opened session nonce hex")
	nodeTCP := fs.String("node-tcp", "", "node tunnel listener host:port")
	local := fs.String("local", "127.0.0.1:1080", "local CONNECT proxy address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *nonce == "" || *nodeTCP == "" {
		return errors.New("--nonce and --node-tcp are required")
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}
	proxy := &tunnel.LocalProxy{ListenAddr: *local, NodeTCP: *nodeTCP, KP: c.KP, Nonce: *nonce}
	ln, err := proxy.Listen()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "local CONNECT proxy on %s -> node %s (session %s)\n",
		ln.Addr(), *nodeTCP, *nonce)
	return proxy.Serve(ln)
}

func heartbeat(args []string) error {
	fs := flag.NewFlagSet("heartbeat", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "S... secret seed")
	up := fs.Int64("up", 0, "cumulative upload bytes")
	down := fs.Int64("down", 0, "cumulative download bytes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}
	return c.Heartbeat(*up, *down)
}

func nodes(args []string) error {
	fs := flag.NewFlagSet("nodes", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *coordinator == "" {
		return fmt.Errorf("--coordinator is required")
	}
	resp, err := http.Get(strings.TrimRight(*coordinator, "/") + "/api/v1/nodes")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("nodes -> %s: %s", resp.Status, string(data))
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	return printJSON(out)
}

func session(args []string) error {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "S... secret seed")
	node := fs.String("node", "", "node G address")
	token := fs.String("token", "", "token contract C address (or G address for a classic asset SAC)")
	ttl := fs.Uint64("ttl", 0, "TTL seconds (default set by coordinator)")
	open := fs.Bool("open", false, "also sign and submit the on-chain open_session")
	rpcURL := fs.String("rpc", os.Getenv("DMESH_RPC"), "Soroban RPC URL for --open")
	nodeIP := fs.String("node-ip", "", "node endpoint public IP: independently verify its tier before opening a session")
	geoCountry := fs.String("geo-country", os.Getenv("GEO_COUNTRY_DB"), "path to GeoLite2-Country.mmdb")
	geoASN := fs.String("geo-asn", os.Getenv("GEO_ASN_DB"), "path to GeoLite2-ASN.mmdb")
	allowMismatch := fs.Bool("allow-tier-mismatch", false, "proceed even if the independent tier check disagrees")
	hold := fs.Int64("hold", 0, "stroops to lock into contract escrow for a capped spend hold (0 = allowance at settlement)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}
	// Client-to-client agreement: re-derive the node's tier from its endpoint
	// IP rather than trusting the directory claim or the coordinator.
	if *nodeIP != "" {
		claim, err := c.NodeByMesh(*node)
		if err != nil {
			return err
		}
		observed, err := classifyIP(*nodeIP, *geoCountry, *geoASN)
		if err != nil {
			return err
		}
		if claim.Tier != string(observed.Tier) && !*allowMismatch {
			return fmt.Errorf("tier disagreement: directory says %q, independent check of %s says %q (refusing; pass --allow-tier-mismatch to override)",
				claim.Tier, *nodeIP, observed.Tier)
		}
		fmt.Fprintf(os.Stderr, "tier check: agreement on %q (%s)\n", observed.Tier, observed.CountryName)
	}
	coordinated, err := c.CoordinateSession(*node, *token, *ttl)
	if err != nil {
		return err
	}
	if !*open {
		return printJSON(coordinated)
	}
	contractID, _ := coordinated.OpenSessionArgs["contract_id"].(string)
	if contractID == "" {
		return fmt.Errorf("coordinator did not return a contract id; deploy and configure the settlement contract first")
	}
	nonceHex, _ := coordinated.OpenSessionArgs["nonce_hex"].(string)
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil || len(nonceBytes) != 32 {
		return fmt.Errorf("bad nonce from coordinator: %w", err)
	}
	var nonce [32]byte
	copy(nonce[:], nonceBytes)
	ttlSec, _ := coordinated.OpenSessionArgs["ttl_seconds"].(float64)
	tier, _ := coordinated.OpenSessionArgs["tier"].(string)

	rpc := soroban.New(*rpcURL, "")
	defer rpc.Close()
	settle := soroban.NewSettlement(rpc, contractID)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var id *big.Int
	var txHash string
	if *hold > 0 {
		id, txHash, err = settle.OpenSessionWithHold(ctx, c.KP, *node, *token, tier, nonce, uint64(ttlSec), *hold)
	} else {
		id, txHash, err = settle.OpenSession(ctx, c.KP, *node, *token, tier, nonce, uint64(ttlSec))
	}
	if err != nil {
		return err
	}
	if err := c.AttachOnChain(nonceHex, id.String(), txHash); err != nil {
		return fmt.Errorf("on-chain open succeeded but coordinator sync failed: %w", err)
	}
	return printJSON(map[string]any{
		"contract_session_id": id.String(),
		"tx_hash":             txHash,
		"nonce_hex":           nonceHex,
	})
}

// attach records the on-chain open_session transaction against a coordinated
// session, moving it from pending to opened so the P2P data plane can serve it.
func attach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "client S... secret seed")
	nonce := fs.String("nonce", "", "session nonce hex")
	contractID := fs.String("contract-id", "", "on-chain session id returned by the contract")
	txHash := fs.String("tx-hash", "", "open_session transaction hash")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *nonce == "" || *contractID == "" || *txHash == "" {
		return errors.New("--nonce, --contract-id and --tx-hash are required")
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}
	if err := c.AttachOnChain(*nonce, *contractID, *txHash); err != nil {
		return err
	}
	return printJSON(map[string]string{"status": "opened", "nonce_hex": *nonce})
}

func counters(args []string) error {
	fs := flag.NewFlagSet("counters", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "S... secret seed")
	nonce := fs.String("nonce", "", "session nonce hex")
	up := fs.Int64("up", 0, "cumulative upload bytes")
	down := fs.Int64("down", 0, "cumulative download bytes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}
	return c.SubmitCounters(*nonce, *up, *down)
}

func settle(args []string) error {
	fs := flag.NewFlagSet("settle", flag.ContinueOnError)
	rpcURL := fs.String("rpc", os.Getenv("DMESH_RPC"), "Soroban RPC URL")
	secret := fs.String("secret", "", "S... secret seed")
	contractID := fs.String("contract", os.Getenv("DMESH_CONTRACT"), "settlement contract C address")
	id := fs.String("id", "", "contract session id (u128 decimal)")
	up := fs.Uint64("up", 0, "agreed upload bytes")
	down := fs.Uint64("down", 0, "agreed download bytes")
	role := fs.String("role", "client", "client (propose) or node (confirm)")
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "optional coordinator URL")
	nonce := fs.String("nonce", "", "session nonce for coordinator sync")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*secret = envOr(*secret, "DMESH_SECRET")
	if *secret == "" || *contractID == "" || *id == "" {
		return fmt.Errorf("--secret, --contract and --id are required")
	}
	kp, err := soroban.ParseSecret(*secret)
	if err != nil {
		return err
	}
	sessionID, ok := new(big.Int).SetString(*id, 10)
	if !ok {
		return fmt.Errorf("invalid session id %q", *id)
	}
	rpc := soroban.New(*rpcURL, "")
	defer rpc.Close()
	settle := soroban.NewSettlement(rpc, *contractID)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var txHash, status string
	switch *role {
	case "client":
		txHash, err = settle.ProposeSettlement(ctx, kp, sessionID, *up, *down)
		status = "proposed"
	case "node":
		txHash, err = settle.ConfirmSettlement(ctx, kp, sessionID, *up, *down)
		status = "settled"
	default:
		return fmt.Errorf("--role must be client or node")
	}
	if err != nil {
		return err
	}
	if *coordinator != "" && *nonce != "" {
		c, err := cliclient.New(*coordinator, *secret)
		if err != nil {
			return err
		}
		if err := c.RecordSettlement(*nonce, status, txHash, int64(*up), int64(*down)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: coordinator sync failed: %v\n", err)
		}
	}
	return printJSON(map[string]string{"tx_hash": txHash, "status": status})
}

func cancel(args []string) error {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	rpcURL := fs.String("rpc", os.Getenv("DMESH_RPC"), "Soroban RPC URL")
	secret := fs.String("secret", "", "S... secret seed")
	contractID := fs.String("contract", os.Getenv("DMESH_CONTRACT"), "settlement contract C address")
	id := fs.String("id", "", "contract session id (u128 decimal)")
	asNode := fs.Bool("as-node", false, "signer acts as the node (default: client)")
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "optional coordinator URL")
	nonce := fs.String("nonce", "", "session nonce for coordinator sync")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*secret = envOr(*secret, "DMESH_SECRET")
	kp, err := soroban.ParseSecret(*secret)
	if err != nil {
		return err
	}
	sessionID, ok := new(big.Int).SetString(*id, 10)
	if !ok {
		return fmt.Errorf("invalid session id %q", *id)
	}
	rpc := soroban.New(*rpcURL, "")
	defer rpc.Close()
	settle := soroban.NewSettlement(rpc, *contractID)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	txHash, err := settle.CancelSession(ctx, kp, sessionID, *asNode)
	if err != nil {
		return err
	}
	if *coordinator != "" && *nonce != "" {
		c, err := cliclient.New(*coordinator, *secret)
		if err != nil {
			return err
		}
		if err := c.RecordSettlement(*nonce, "cancelled", txHash, 0, 0); err != nil {
			fmt.Fprintf(os.Stderr, "warning: coordinator sync failed: %v\n", err)
		}
	}
	return printJSON(map[string]string{"tx_hash": txHash, "status": "cancelled"})
}

// reclaim releases an expired escrow: committed usage is paid to the node and
// the remainder refunded, or the full hold returns to the client.
func reclaim(args []string) error {
	fs := flag.NewFlagSet("reclaim", flag.ContinueOnError)
	rpcURL := fs.String("rpc", os.Getenv("DMESH_RPC"), "Soroban RPC URL")
	secret := fs.String("secret", "", "S... secret seed (client or node)")
	contractID := fs.String("contract", os.Getenv("DMESH_CONTRACT"), "settlement contract C address")
	id := fs.String("id", "", "contract session id (u128 decimal)")
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "optional coordinator URL")
	nonce := fs.String("nonce", "", "session nonce for coordinator sync")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*secret = envOr(*secret, "DMESH_SECRET")
	kp, err := soroban.ParseSecret(*secret)
	if err != nil {
		return err
	}
	sessionID, ok := new(big.Int).SetString(*id, 10)
	if !ok {
		return fmt.Errorf("invalid session id %q", *id)
	}
	rpc := soroban.New(*rpcURL, "")
	defer rpc.Close()
	settle := soroban.NewSettlement(rpc, *contractID)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	txHash, err := settle.ReclaimHold(ctx, kp, sessionID)
	if err != nil {
		return err
	}
	if *coordinator != "" && *nonce != "" {
		c, err := cliclient.New(*coordinator, *secret)
		if err != nil {
			return err
		}
		if err := c.RecordSettlement(*nonce, "settled", txHash, 0, 0); err != nil {
			fmt.Fprintf(os.Stderr, "warning: coordinator sync failed: %v\n", err)
		}
	}
	return printJSON(map[string]string{"tx_hash": txHash, "status": "reclaimed"})
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
