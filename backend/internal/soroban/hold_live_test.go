package soroban_test

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"

	"dmesh/internal/soroban"
	"dmesh/internal/stellarx"
)

// TestTestnetSpendHoldLifecycle proves the capped escrow path on live
// testnet without any coordinator or admin key:
//
//  1. the client locks a 5 XLM hold via open_session_with_hold,
//  2. a small metered settlement is paid out of the escrow (80/20 split),
//  3. the unspent remainder is refunded automatically,
//  4. an early reclaim is refused and, after TTL, a second hold is reclaimed
//     in full and cannot be reclaimed twice.
//
// Run with DMESH_E2E=1, DMESH_CONTRACT_ID and DMESH_XLM_CONTRACT.
func TestTestnetSpendHoldLifecycle(t *testing.T) {
	if os.Getenv("DMESH_E2E") != "1" {
		t.Skip("set DMESH_E2E=1 to run live testnet spend-hold tests")
	}
	contractID := os.Getenv("DMESH_CONTRACT_ID")
	xlmToken := os.Getenv("DMESH_XLM_CONTRACT")
	if contractID == "" || xlmToken == "" {
		t.Fatal("DMESH_CONTRACT_ID and DMESH_XLM_CONTRACT are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	chain := stellarx.New("https://horizon-testnet.stellar.org", soroban.TestnetPassphrase)
	rpc := soroban.New(soroban.DefaultTestnetRPC, soroban.TestnetPassphrase)
	defer rpc.Close()
	settle := soroban.NewSettlement(rpc, contractID)

	clientKP := fundedKeypair(t, chain)
	nodeKP := fundedKeypair(t, chain)
	t.Logf("client=%s node=%s", clientKP.Address(), nodeKP.Address())

	// Approve a generous allowance; transfer_from pulls native XLM through
	// the canonical SAC wrapper.
	latest, err := rpc.RPC().GetLatestLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fromAddr, _ := soroban.Address(clientKP.Address())
	spenderAddr, _ := soroban.Address(contractID)
	approveHash, err := rpc.Invoke(ctx, clientKP, xlmToken, "approve", []xdr.ScVal{
		fromAddr, spenderAddr, soroban.I128(0, xlm(100)),
		soroban.U32(latest.Sequence + 100_000),
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	t.Logf("allowance approved tx=%s", approveHash)

	// Measure the contract's own SAC balance: it is the escrow account, so its
	// deltas show the hold locks, payouts and refunds exactly. (The client's
	// SAC balance view includes its full native XLM reserve and is unsuitable
	// for absolute refund assertions.)
	escrowBaseline := tokenBalance(t, ctx, rpc, xlmToken, contractID, clientKP)
	t.Logf("contract escrow baseline %s stroops", escrowBaseline.String())

	const hold5 = int64(50_000_000) // 5 XLM escrow
	nonce1 := randomNonce(t)
	sessionID, openHash, err := settle.OpenSessionWithHold(ctx, clientKP,
		nodeKP.Address(), xlmToken, soroban.TierResidential, nonce1, 3600, hold5)
	if err != nil {
		t.Fatalf("open_session_with_hold: %v", err)
	}
	t.Logf("hold session=%s open_tx=%s hold=%d stroops", sessionID.String(), openHash, hold5)

	// The contract's own SAC balance must grow by exactly the locked hold.
	escrowAfter := tokenBalance(t, ctx, rpc, xlmToken, contractID, clientKP)
	locked := new(big.Int).Sub(escrowAfter, escrowBaseline)
	if locked.Cmp(big.NewInt(hold5)) != 0 {
		t.Fatalf("escrow lock delta = %s stroops, want exactly %d", locked.String(), hold5)
	}
	t.Logf("escrow lock verified: +%s stroops", locked.String())

	// Settle 200 MB total (100 MB each way) at residential pricing.
	// compute_cost: 200e6 * 150000 / 1e9 = 30000 micro-USD ($0.03);
	// at 100000 micro-USD/XLM -> 3,000,000 stroops total. 80% node =
	// 2,400,000; 20% protocol fee = 600,000; hold remainder refunded = 47m.
	const half100MB = uint64(100_000_000)
	nodeBefore := tokenBalance(t, ctx, rpc, xlmToken, nodeKP.Address(), clientKP)
	if _, err := settle.ProposeSettlement(ctx, clientKP, sessionID, half100MB, half100MB); err != nil {
		t.Fatalf("propose: %v", err)
	}
	confirmHash, err := settle.ConfirmSettlement(ctx, nodeKP, sessionID, half100MB, half100MB)
	if err != nil {
		t.Fatalf("confirm against hold: %v", err)
	}
	t.Logf("settlement from escrow confirm_tx=%s", confirmHash)

	// Tolerant band: SAC balance views can carry minor wrapper dust, but the
	// on-chain split itself is exact (2,400,000 / 600,000).
	nodeGain := new(big.Int).Sub(
		tokenBalance(t, ctx, rpc, xlmToken, nodeKP.Address(), clientKP), nodeBefore)
	if nodeGain.Cmp(big.NewInt(2_280_000)) < 0 || nodeGain.Cmp(big.NewInt(2_400_000)) > 0 {
		t.Fatalf("node 80%% share = %s stroops, want ~2,400,000", nodeGain.String())
	}
	// After settlement the escrow must have released the whole 5 XLM hold:
	// 3,000,000 paid out (2.4m node + 0.6m protocol fee) and 47,000,000
	// refunded to the client, so the contract balance returns to baseline.
	escrowSettled := tokenBalance(t, ctx, rpc, xlmToken, contractID, clientKP)
	settledDelta := new(big.Int).Sub(escrowSettled, escrowBaseline)
	if settledDelta.Sign() != 0 {
		t.Fatalf("escrow not fully released after settlement, residual = %s stroops", settledDelta.String())
	}
	t.Logf("escrow split verified from contract balances: node share %s stroops; full hold released",
		nodeGain.String())

	// A second, short-lived hold for the reclaim path.
	const hold1 = int64(10_000_000) // 1 XLM, 30 s TTL
	beforeSecond := tokenBalance(t, ctx, rpc, xlmToken, contractID, clientKP)
	sid2, open2, err := settle.OpenSessionWithHold(ctx, clientKP,
		nodeKP.Address(), xlmToken, soroban.TierResidential, randomNonce(t), 30, hold1)
	if err != nil {
		t.Fatalf("second open_session_with_hold: %v", err)
	}
	locked2 := new(big.Int).Sub(
		tokenBalance(t, ctx, rpc, xlmToken, contractID, clientKP), beforeSecond)
	if locked2.Cmp(big.NewInt(hold1)) != 0 {
		t.Fatalf("second hold lock = %s stroops, want %d", locked2.String(), hold1)
	}
	t.Logf("short hold session=%s open_tx=%s ttl=30s escrow +%s", sid2.String(), open2, locked2.String())

	// Reclaim before expiry must be rejected by the contract.
	if _, err := settle.ReclaimHold(ctx, clientKP, sid2); err == nil {
		t.Fatal("early reclaim must fail with HoldNotReleasable")
	}
	t.Log("early reclaim correctly refused")

	// Wait past the 30 s TTL plus ledger close margin.
	deadline := time.Now().Add(75 * time.Second)
	var reclaimHash string
	for time.Now().Before(deadline) {
		time.Sleep(8 * time.Second)
		reclaimHash, err = settle.ReclaimHold(ctx, clientKP, sid2)
		if err == nil {
			break
		}
		t.Logf("reclaim not yet releasable: %v", err)
	}
	if err != nil {
		t.Fatalf("reclaim after TTL: %v", err)
	}
	afterReclaim := tokenBalance(t, ctx, rpc, xlmToken, contractID, clientKP)
	if afterReclaim.Cmp(beforeSecond) != 0 {
		t.Fatalf("reclaim did not return the full hold; escrow delta %s stroops",
			new(big.Int).Sub(afterReclaim, beforeSecond).String())
	}
	t.Logf("expired hold reclaimed in full tx=%s; escrow returned to baseline", reclaimHash)

	// Double reclaim must fail (the hold is marked released).
	if _, err := settle.ReclaimHold(ctx, clientKP, sid2); err == nil {
		t.Fatal("reclaiming a released hold must fail")
	}
	t.Log("double reclaim correctly refused")
}
