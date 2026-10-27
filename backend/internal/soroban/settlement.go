package soroban

import (
	"context"
	"errors"
	"math/big"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Tier symbols mirror the contract's tier table keys. Soroban symbols are
// limited to 10 characters, hence the abbreviated forms.
const (
	TierResidential = "res"
	TierDatacenter  = "dc"
)

// Contract function names.
const (
	fnInitialize    = "initialize"
	fnSetTier       = "set_tier"
	fnSetAssetPrice = "set_asset_price"
	fnOpenSession   = "open_session"
	fnPropose       = "propose_settlement"
	fnConfirm       = "confirm_settlement"
	fnCancel        = "cancel_session"
	fnFeeBPS        = "fee_bps"
	fnSessionCount  = "session_count"
	fnGetTier       = "get_tier"
	fnGetAssetPrice = "get_asset_price"
)

// Settlement is the typed binding to the deployed dmesh settlement contract.
type Settlement struct {
	client     *Client
	contractID string
}

// NewSettlement binds a Client to a deployed contract instance.
func NewSettlement(client *Client, contractID string) *Settlement {
	return &Settlement{client: client, contractID: contractID}
}

// ContractID returns the C... identifier of the bound contract.
func (s *Settlement) ContractID() string { return s.contractID }

func u128Words(n *big.Int) (hi, lo uint64, err error) {
	if n == nil || n.Sign() < 0 {
		return 0, 0, errors.New("soroban: u128 requires a non-negative big.Int")
	}
	if n.BitLen() > 128 {
		return 0, 0, errors.New("soroban: value overflows u128")
	}
	lo = n.Uint64()
	hi = new(big.Int).Rsh(n, 64).Uint64()
	return hi, lo, nil
}

func i128Words(v int64) (hi int64, lo uint64) {
	return v >> 63, uint64(v)
}

// Initialize runs the one-time contract initialization (admin only).
func (s *Settlement) Initialize(ctx context.Context, admin *keypair.Full, feeBPS uint32) (string, error) {
	adminAddr, err := Address(admin.Address())
	if err != nil {
		return "", err
	}
	return s.client.Invoke(ctx, admin, s.contractID, fnInitialize,
		[]xdr.ScVal{adminAddr, U32(feeBPS)})
}

// SetTier upserts a per-GB tier price in micro-USD (admin only).
func (s *Settlement) SetTier(ctx context.Context, admin *keypair.Full, tier string, microUSDPerGB int64) (string, error) {
	sym, err := Symbol(tier)
	if err != nil {
		return "", err
	}
	hi, lo := i128Words(microUSDPerGB)
	return s.client.Invoke(ctx, admin, s.contractID, fnSetTier,
		[]xdr.ScVal{sym, I128(hi, lo)})
}

// SetAssetPrice upserts the oracle price of one whole token in micro-USD (admin only).
func (s *Settlement) SetAssetPrice(ctx context.Context, admin *keypair.Full, tokenAddr string, microUSDPerToken int64) (string, error) {
	token, err := Address(tokenAddr)
	if err != nil {
		return "", err
	}
	hi, lo := i128Words(microUSDPerToken)
	return s.client.Invoke(ctx, admin, s.contractID, fnSetAssetPrice,
		[]xdr.ScVal{token, I128(hi, lo)})
}

// OpenSession is signed by the client. It returns the on-chain tx hash and the
// newly assigned session id, read back through the session_count view.
func (s *Settlement) OpenSession(
	ctx context.Context,
	clientKP *keypair.Full,
	nodeAddr, tokenAddr, tier string,
	nonce [32]byte,
	ttlSeconds uint64,
) (sessionID *big.Int, txHash string, err error) {
	clientAddr, err := Address(clientKP.Address())
	if err != nil {
		return nil, "", err
	}
	node, err := Address(nodeAddr)
	if err != nil {
		return nil, "", err
	}
	token, err := Address(tokenAddr)
	if err != nil {
		return nil, "", err
	}
	sym, err := Symbol(tier)
	if err != nil {
		return nil, "", err
	}
	txHash, err = s.client.Invoke(ctx, clientKP, s.contractID, fnOpenSession,
		[]xdr.ScVal{clientAddr, node, token, sym, BytesN32(nonce), U64(ttlSeconds)})
	if err != nil {
		return nil, "", err
	}
	sessionID, err = s.SessionCount(ctx, clientKP)
	if err != nil {
		return nil, txHash, err
	}
	return sessionID, txHash, nil
}

// ProposeSettlement is signed by the client and commits its byte counters.
func (s *Settlement) ProposeSettlement(ctx context.Context, clientKP *keypair.Full, sessionID *big.Int, upBytes, downBytes uint64) (string, error) {
	hi, lo, err := u128Words(sessionID)
	if err != nil {
		return "", err
	}
	return s.client.Invoke(ctx, clientKP, s.contractID, fnPropose,
		[]xdr.ScVal{U128(hi, lo), U64(upBytes), U64(downBytes)})
}

// ConfirmSettlement is signed by the node; on matching counters the contract
// pulls the allowance via transfer_from and splits it 80/20 atomically.
func (s *Settlement) ConfirmSettlement(ctx context.Context, nodeKP *keypair.Full, sessionID *big.Int, upBytes, downBytes uint64) (string, error) {
	hi, lo, err := u128Words(sessionID)
	if err != nil {
		return "", err
	}
	return s.client.Invoke(ctx, nodeKP, s.contractID, fnConfirm,
		[]xdr.ScVal{U128(hi, lo), U64(upBytes), U64(downBytes)})
}

// CancelSession is signed by either party. asNode selects which role the
// signer acts as for the authorization check.
func (s *Settlement) CancelSession(ctx context.Context, signer *keypair.Full, sessionID *big.Int, asNode bool) (string, error) {
	hi, lo, err := u128Words(sessionID)
	if err != nil {
		return "", err
	}
	return s.client.Invoke(ctx, signer, s.contractID, fnCancel,
		[]xdr.ScVal{U128(hi, lo), Bool(asNode)})
}

// FeeBPS reads the protocol fee in basis points.
func (s *Settlement) FeeBPS(ctx context.Context, source *keypair.Full) (uint32, error) {
	out, err := s.client.View(ctx, source, s.contractID, fnFeeBPS, nil)
	if err != nil {
		return 0, err
	}
	if out.Type != xdr.ScValTypeScvU32 || out.U32 == nil {
		return 0, errors.New("soroban: fee_bps returned unexpected type")
	}
	return uint32(*out.U32), nil
}

// SessionCount reads the highest assigned session id.
func (s *Settlement) SessionCount(ctx context.Context, source *keypair.Full) (*big.Int, error) {
	out, err := s.client.View(ctx, source, s.contractID, fnSessionCount, nil)
	if err != nil {
		return nil, err
	}
	hi, lo, err := AsU128(out)
	if err != nil {
		return nil, err
	}
	return new(big.Int).Add(new(big.Int).Lsh(big.NewInt(int64(hi)), 64), new(big.Int).SetUint64(lo)), nil
}

// GetTier reads a tier price in micro-USD per GB; ok=false means the tier is not configured.
func (s *Settlement) GetTier(ctx context.Context, source *keypair.Full, tier string) (int64, bool, error) {
	sym, err := Symbol(tier)
	if err != nil {
		return 0, false, err
	}
	out, err := s.client.View(ctx, source, s.contractID, fnGetTier, []xdr.ScVal{sym})
	if err != nil {
		return 0, false, err
	}
	if out.Type == xdr.ScValTypeScvVoid {
		return 0, false, nil
	}
	_, lo, err := AsI128(out)
	if err != nil {
		return 0, false, err
	}
	return int64(lo), true, nil // prices are always positive and fit int64
}

// GetAssetPrice reads the oracle price of a token in micro-USD; ok=false means unconfigured.
func (s *Settlement) GetAssetPrice(ctx context.Context, source *keypair.Full, tokenAddr string) (int64, bool, error) {
	token, err := Address(tokenAddr)
	if err != nil {
		return 0, false, err
	}
	out, err := s.client.View(ctx, source, s.contractID, fnGetAssetPrice, []xdr.ScVal{token})
	if err != nil {
		return 0, false, err
	}
	if out.Type == xdr.ScValTypeScvVoid {
		return 0, false, nil
	}
	_, lo, err := AsI128(out)
	if err != nil {
		return 0, false, err
	}
	return int64(lo), true, nil
}
