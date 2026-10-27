// Command dmesh is the reference mesh client and node CLI. Keys are
// generated and held locally; every on-chain action is signed on this
// machine and the coordinator never sees a secret seed.
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"dmesh/internal/cliclient"
	"dmesh/internal/geo"
	"dmesh/internal/meshauth"
	"dmesh/internal/soroban"
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
	case "agent":
		err = agentFetch(args)
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
  session    --coordinator URL --secret S... --node G... --token C/G... [--ttl 3600] [--open]
  counters   --coordinator URL --secret S... --nonce HEX --up N --down N
  settle     --rpc URL --secret S... --contract C... --id 123 --up N --down N --role client|node [--coordinator URL --nonce HEX]
  cancel     --rpc URL --secret S... --contract C... --id 123 --as-node [--coordinator URL --nonce HEX]
  agent      --coordinator URL --secret S... --nonce HEX --url https://example.com
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
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
	id, txHash, err := settle.OpenSession(ctx, c.KP, *node, *token, tier, nonce, uint64(ttlSec))
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

func agentFetch(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	coordinator := fs.String("coordinator", os.Getenv("DMESH_COORDINATOR"), "coordinator base URL")
	secret := fs.String("secret", "", "S... secret seed")
	nonce := fs.String("nonce", "", "active session nonce hex")
	target := fs.String("url", "", "http(s) URL to fetch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient(*coordinator, *secret)
	if err != nil {
		return err
	}
	ts := time.Now().Unix()
	payload := meshauth.CanonicalPayload(c.Address(), ts, http.MethodGet, *target)
	kp, err := soroban.ParseSecret(envOr(*secret, "DMESH_SECRET"))
	if err != nil {
		return err
	}
	sig, err := kp.Sign(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet,
		strings.TrimRight(*coordinator, "/")+"/api/v1/agent/fetch?url="+*target, nil)
	if err != nil {
		return err
	}
	req.Header.Set(meshauth.HeaderAddress, c.Address())
	req.Header.Set(meshauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(meshauth.HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	req.Header.Set(meshauth.HeaderSession, *nonce)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("agent fetch -> %s: %s", resp.Status, string(body))
	}
	fmt.Printf("X-DMesh-Bytes: %s\nX-DMesh-Session: %s\n\n",
		resp.Header.Get("X-DMesh-Bytes"), resp.Header.Get("X-DMesh-Session"))
	_, err = os.Stdout.Write(body)
	return err
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
