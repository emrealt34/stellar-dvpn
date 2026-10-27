package soroban_test

import (
	"context"
	"crypto/rand"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"

	"dmesh/internal/soroban"
	"dmesh/internal/stellarx"
)

// Live, on-chain testnet proof. Runs only with DMESH_E2E=1 and a deployed,
// initialized contract. Required environment:
//
//	DMESH_CONTRACT_ID        settlement contract C... address
//	DMESH_ADMIN_SECRET       S... secret of the contract admin (initialized)
//	DMESH_XLM_CONTRACT       native XLM SAC C... address
func liveConfig(t *testing.T) (contractID, xlmToken string, admin *keypair.Full) {
	t.Helper()
	if os.Getenv("DMESH_E2E") != "1" {
		t.Skip("set DMESH_E2E=1 to run live testnet settlement tests")
	}
	contractID = os.Getenv("DMESH_CONTRACT_ID")
	xlmToken = os.Getenv("DMESH_XLM_CONTRACT")
	adminSecret := os.Getenv("DMESH_ADMIN_SECRET")
	if contractID == "" || xlmToken == "" || adminSecret == "" {
		t.Fatal("DMESH_CONTRACT_ID, DMESH_XLM_CONTRACT and DMESH_ADMIN_SECRET are required")
	}
	var err error
	admin, err = soroban.ParseSecret(adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	return contractID, xlmToken, admin
}

func fundedKeypair(t *testing.T, chain *stellarx.Client) *keypair.Full {
	t.Helper()
	kp, err := keypair.Random()
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.FundTestnetAccount(kp.Address()); err != nil {
		t.Fatalf("friendbot funding %s: %v", kp.Address(), err)
	}
	return kp
}

func randomNonce(t *testing.T) [32]byte {
	t.Helper()
	var n [32]byte
	if _, err := rand.Read(n[:]); err != nil {
		t.Fatal(err)
	}
	return n
}

// tokenBalance reads the SAC balance (stroops) of who via a read-only view,
// using sourceKP only to shape the simulation envelope.
func tokenBalance(t *testing.T, ctx context.Context, c *soroban.Client,
	token, who string, sourceKP *keypair.Full) *big.Int {
	t.Helper()
	scAddr, err := soroban.Address(who)
	if err != nil {
		t.Fatal(err)
	}
	val, err := c.View(ctx, sourceKP, token, "balance", []xdr.ScVal{scAddr})
	if err != nil {
		t.Fatalf("balance view for %s: %v", who, err)
	}
	hi, lo, err := soroban.AsI128(val)
	if err != nil {
		t.Fatal(err)
	}
	return new(big.Int).Add(new(big.Int).Lsh(big.NewInt(hi), 64), new(big.Int).SetUint64(lo))
}

const stroopsPerXLM = 10_000_000

func xlm(n uint64) uint64 { return n * stroopsPerXLM }

// TestTestnetSettlementFullLifecycle exercises open -> allowance -> propose
// -> confirm with real XLM transfers and the 80/20 split, then mismatch
// rejection and a cancelled session that never moves funds.
func TestTestnetSettlementFullLifecycle(t *testing.T) {
	contractID, xlmToken, adminKP := liveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	chain := stellarx.New("https://horizon-testnet.stellar.org", soroban.TestnetPassphrase)
	client := soroban.New(soroban.DefaultTestnetRPC, soroban.TestnetPassphrase)
	defer client.Close()
	settle := soroban.NewSettlement(client, contractID)

	clientKP := fundedKeypair(t, chain)
	nodeKP := fundedKeypair(t, chain)
	t.Logf("client=%s node=%s admin=%s", clientKP.Address(), nodeKP.Address(), adminKP.Address())

	// Contract configuration is readable through the Go bindings.
	fee, err := settle.FeeBPS(ctx, adminKP)
	if err != nil {
		t.Fatal(err)
	}
	if fee != 2000 {
		t.Fatalf("fee_bps = %d, want 2000", fee)
	}
	resPrice, ok, err := settle.GetTier(ctx, adminKP, soroban.TierResidential)
	if err != nil || !ok || resPrice != 150000 {
		t.Fatalf("residential tier = %d ok=%v err=%v", resPrice, ok, err)
	}
	dcPrice, ok, err := settle.GetTier(ctx, adminKP, soroban.TierDatacenter)
	if err != nil || !ok || dcPrice != 30000 {
		t.Fatalf("datacenter tier = %d ok=%v err=%v", dcPrice, ok, err)
	}

	// Client approves a 100 XLM allowance for the settlement contract.
	latest, err := client.RPC().GetLatestLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fromAddr, err := soroban.Address(clientKP.Address())
	if err != nil {
		t.Fatal(err)
	}
	spenderAddr, err := soroban.Address(contractID)
	if err != nil {
		t.Fatal(err)
	}
	// SAC approve(from, spender, amount, live_until_ledger).
	approveHash, err := client.Invoke(ctx, clientKP, xlmToken, "approve", []xdr.ScVal{
		fromAddr,
		spenderAddr,
		soroban.I128(0, xlm(100)),
		soroban.U32(latest.Sequence + 100_000),
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	t.Logf("allowance approved tx=%s", approveHash)

	// 1 GB total (500 MB each way) at residential $0.15/GB, XLM @ $0.10 => 1.5 XLM.
	const halfGB = uint64(500_000_000)

	// --- Session 1: happy path, full 80/20 split ---------------------------
	sessionID, openHash, err := settle.OpenSession(ctx, clientKP,
		nodeKP.Address(), xlmToken, soroban.TierResidential, randomNonce(t), 3600)
	if err != nil {
		t.Fatalf("open_session: %v", err)
	}
	t.Logf("session=%s open_tx=%s", sessionID.String(), openHash)

	nodeBefore := tokenBalance(t, ctx, client, xlmToken, nodeKP.Address(), adminKP)
	adminBefore := tokenBalance(t, ctx, client, xlmToken, adminKP.Address(), adminKP)
	clientBefore := tokenBalance(t, ctx, client, xlmToken, clientKP.Address(), adminKP)

	proposeHash, err := settle.ProposeSettlement(ctx, clientKP, sessionID, halfGB, halfGB)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	t.Logf("propose_tx=%s", proposeHash)
	confirmHash, err := settle.ConfirmSettlement(ctx, nodeKP, sessionID, halfGB, halfGB)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	t.Logf("confirm_tx=%s", confirmHash)

	nodeAfter := tokenBalance(t, ctx, client, xlmToken, nodeKP.Address(), adminKP)
	adminAfter := tokenBalance(t, ctx, client, xlmToken, adminKP.Address(), adminKP)
	clientAfter := tokenBalance(t, ctx, client, xlmToken, clientKP.Address(), adminKP)

	// Node nets 1.2 XLM minus its own transaction resource fees, so assert a
	// tight band around the 80% share (12,000,000 stroops +/- 100k).
	nodeGain := new(big.Int).Sub(nodeAfter, nodeBefore)
	if nodeGain.Cmp(big.NewInt(11_900_000)) < 0 || nodeGain.Cmp(big.NewInt(12_100_000)) > 0 {
		t.Fatalf("node gain = %s stroops, want ~1.2 XLM", nodeGain.String())
	}
	// Admin receives the exact 20% fee (it signs no tx here): 0.3 XLM.
	adminGain := new(big.Int).Sub(adminAfter, adminBefore)
	if adminGain.Cmp(big.NewInt(3_000_000)) != 0 {
		t.Fatalf("admin fee gain = %s stroops, want exactly 0.3 XLM", adminGain.String())
	}
	// Client paid at least the 1.5 XLM settlement (its own resource fees add more).
	clientSpend := new(big.Int).Sub(clientBefore, clientAfter)
	if clientSpend.Cmp(big.NewInt(15_000_000)) < 0 {
		t.Fatalf("client spend = %s stroops, want at least 1.5 XLM", clientSpend.String())
	}
	t.Logf("split verified: node +%s, admin +%s, client -%s (stroops)",
		nodeGain.String(), adminGain.String(), clientSpend.String())

	// Replay: confirming the same settled session again must fail.
	if _, err := settle.ConfirmSettlement(ctx, nodeKP, sessionID, halfGB, halfGB); err == nil {
		t.Fatal("replay of a settled session must be rejected")
	}

	// --- Session 2: node cannot confirm counters the client did not propose -
	sid2, _, err := settle.OpenSession(ctx, clientKP,
		nodeKP.Address(), xlmToken, soroban.TierResidential, randomNonce(t), 3600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settle.ProposeSettlement(ctx, clientKP, sid2, halfGB, halfGB); err != nil {
		t.Fatal(err)
	}
	if _, err := settle.ConfirmSettlement(ctx, nodeKP, sid2, halfGB+1, halfGB); err == nil {
		t.Fatal("mismatched confirm must be rejected on chain")
	}

	// --- Session 3: cancellation moves no money ----------------------------
	clientBeforeCancel := tokenBalance(t, ctx, client, xlmToken, clientKP.Address(), adminKP)
	sid3, _, err := settle.OpenSession(ctx, clientKP,
		nodeKP.Address(), xlmToken, soroban.TierDatacenter, randomNonce(t), 3600)
	if err != nil {
		t.Fatal(err)
	}
	cancelHash, err := settle.CancelSession(ctx, clientKP, sid3, false)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	t.Logf("cancel_tx=%s", cancelHash)
	clientAfterCancel := tokenBalance(t, ctx, client, xlmToken, clientKP.Address(), adminKP)
	transferSpend := new(big.Int).Sub(clientBeforeCancel, clientAfterCancel)
	// Only the resource fee may be spent; it is far below even 0.01 XLM.
	if transferSpend.Cmp(big.NewInt(int64(xlm(1)/100))) > 0 {
		t.Fatalf("cancelled session cost %s stroops beyond fees; no transfer should occur", transferSpend.String())
	}
}
