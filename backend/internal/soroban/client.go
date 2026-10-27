// Package soroban is the thin client for the dmesh settlement smart contract.
// It never custody-creates or controls user funds: every transaction is signed
// locally by the party that invokes it (client or node), and the backend only
// coordinates which calls are made.
package soroban

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Default endpoints for the Stellar testnet.
const (
	DefaultTestnetRPC        = "https://soroban-testnet.stellar.org"
	TestnetPassphrase        = "Test SDF Network ; September 2015"
	defaultBaseFee     int64 = 100 // 100 stroops inclusion fee, matching stellar-cli
	// Generous validity window: the simulate -> rebuild -> sign -> send flow,
	// plus upstream friendbot/ledger latency on testnet, must not outlive the
	// timebound (an expired envelope is rejected by core with no funds moved).
	txTimeout = 10 * time.Minute
)

// Client talks to a Soroban RPC endpoint on a fixed Stellar network.
type Client struct {
	rpc        *rpcclient.Client
	passphrase string
}

// New creates a Client. An empty rpcURL selects the public testnet endpoint.
func New(rpcURL, passphrase string) *Client {
	if rpcURL == "" {
		rpcURL = DefaultTestnetRPC
	}
	if passphrase == "" {
		passphrase = TestnetPassphrase
	}
	return &Client{
		rpc:        rpcclient.NewClient(rpcURL, nil),
		passphrase: passphrase,
	}
}

// RPC exposes the underlying client for callers that need raw access.
func (c *Client) RPC() *rpcclient.Client { return c.rpc }

// Close releases the underlying HTTP resources.
func (c *Client) Close() error { return c.rpc.Close() }

// ParseSecret parses an S... Stellar secret seed into a signing keypair.
func ParseSecret(secret string) (*keypair.Full, error) {
	return keypair.ParseFull(secret)
}

// ---------------------------------------------------------------------------
// ScVal construction helpers (mirrors the contract's #[contracttype] layout)
// ---------------------------------------------------------------------------

// Symbol builds an ScVal symbol. The string must be <= 10 characters.
func Symbol(s string) (xdr.ScVal, error) {
	if len(s) > 10 {
		return xdr.ScVal{}, fmt.Errorf("soroban: symbol %q exceeds 10 characters", s)
	}
	sym := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}, nil
}

// MustSymbol panics if the symbol is invalid; use only with constants.
func MustSymbol(s string) xdr.ScVal {
	v, err := Symbol(s)
	if err != nil {
		panic(err)
	}
	return v
}

// Address builds an ScVal address from a G... account id or C... contract id.
func Address(addr string) (xdr.ScVal, error) {
	sc, err := ScAddress(addr)
	if err != nil {
		return xdr.ScVal{}, err
	}
	return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &sc}, nil
}

// ScAddress parses a strkey into an xdr.ScAddress (account or contract).
func ScAddress(addr string) (xdr.ScAddress, error) {
	version, payload, err := strkey.DecodeAny(addr)
	if err != nil {
		return xdr.ScAddress{}, fmt.Errorf("soroban: invalid strkey %q: %w", addr, err)
	}
	switch version {
	case strkey.VersionByteAccountID:
		var account xdr.AccountId
		if err := account.SetAddress(addr); err != nil {
			return xdr.ScAddress{}, err
		}
		return xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &account,
		}, nil
	case strkey.VersionByteContract:
		if len(payload) != 32 {
			return xdr.ScAddress{}, fmt.Errorf("soroban: contract id must be 32 bytes")
		}
		var hash xdr.Hash
		copy(hash[:], payload)
		contractID := xdr.ContractId(hash)
		return xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &contractID,
		}, nil
	default:
		return xdr.ScAddress{}, fmt.Errorf("soroban: unsupported strkey version %v", version)
	}
}

// U64 builds an ScVal u64.
func U64(v uint64) xdr.ScVal {
	u := xdr.Uint64(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &u}
}

// U32 builds an ScVal u32.
func U32(v uint32) xdr.ScVal {
	u := xdr.Uint32(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u}
}

// U128 builds an ScVal u128 from a pair of 64-bit words.
func U128(hi, lo uint64) xdr.ScVal {
	parts := xdr.UInt128Parts{Hi: xdr.Uint64(hi), Lo: xdr.Uint64(lo)}
	return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &parts}
}

// I128 builds an ScVal i128 from a pair of 64-bit words.
func I128(hi int64, lo uint64) xdr.ScVal {
	parts := xdr.Int128Parts{Hi: xdr.Int64(hi), Lo: xdr.Uint64(lo)}
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &parts}
}

// Bool builds an ScVal boolean.
func Bool(b bool) xdr.ScVal {
	return xdr.ScVal{Type: xdr.ScValTypeScvBool, B: &b}
}

// BytesN32 builds an ScVal fixed-size 32-byte value (e.g. session nonces).
func BytesN32(b [32]byte) xdr.ScVal {
	sc := xdr.ScBytes(b[:])
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &sc}
}

// AsU64 unwraps an ScVal u64.
func AsU64(v xdr.ScVal) (uint64, error) {
	if v.Type != xdr.ScValTypeScvU64 || v.U64 == nil {
		return 0, fmt.Errorf("soroban: expected u64 ScVal, got %s", v.Type)
	}
	return uint64(*v.U64), nil
}

// AsU128 unwraps an ScVal u128 into hi/lo words.
func AsU128(v xdr.ScVal) (hi, lo uint64, err error) {
	if v.Type != xdr.ScValTypeScvU128 || v.U128 == nil {
		return 0, 0, fmt.Errorf("soroban: expected u128 ScVal, got %s", v.Type)
	}
	return uint64(v.U128.Hi), uint64(v.U128.Lo), nil
}

// AsI128 unwraps an ScVal i128 into a signed hi word and lo word.
func AsI128(v xdr.ScVal) (hi int64, lo uint64, err error) {
	if v.Type != xdr.ScValTypeScvI128 || v.I128 == nil {
		return 0, 0, fmt.Errorf("soroban: expected i128 ScVal, got %s", v.Type)
	}
	return int64(v.I128.Hi), uint64(v.I128.Lo), nil
}

// ---------------------------------------------------------------------------
// Transaction lifecycle
// ---------------------------------------------------------------------------

func invokeHostFunction(contractID, fn string, args []xdr.ScVal) (xdr.HostFunction, error) {
	contractAddr, err := ScAddress(contractID)
	if err != nil {
		return xdr.HostFunction{}, err
	}
	return xdr.HostFunction{
		Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
		InvokeContract: &xdr.InvokeContractArgs{
			ContractAddress: contractAddr,
			FunctionName:    xdr.ScSymbol(fn),
			Args:            args,
		},
	}, nil
}

// View simulates a read-only contract call and returns the decoded ScVal result
// without submitting anything to the ledger. source shapes the simulation
// envelope (its sequence number is read) but never signs a transaction.
func (c *Client) View(ctx context.Context, source *keypair.Full, contractID, fn string, args []xdr.ScVal) (xdr.ScVal, error) {
	if source == nil {
		return xdr.ScVal{}, errors.New("soroban: view requires a source account")
	}
	op, err := newInvokeOp(contractID, fn, args)
	if err != nil {
		return xdr.ScVal{}, err
	}
	raw, _, err := c.simulationEnvelope(ctx, source, op)
	if err != nil {
		return xdr.ScVal{}, err
	}
	sim, err := c.rpc.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: raw})
	if err != nil {
		return xdr.ScVal{}, err
	}
	if sim.Error != "" {
		return xdr.ScVal{}, fmt.Errorf("soroban: simulation failed: %s", sim.Error)
	}
	if len(sim.Results) == 0 || sim.Results[0].ReturnValueXDR == nil {
		return xdr.ScVal{}, errors.New("soroban: simulation returned no value")
	}
	var out xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(*sim.Results[0].ReturnValueXDR, &out); err != nil {
		return xdr.ScVal{}, fmt.Errorf("soroban: decoding return value: %w", err)
	}
	return out, nil
}

// txStatusError is the SendTransaction status meaning stellar-core rejected
// the transaction during submission (the protocol package does not export a
// constant for it in this SDK version).
const txStatusError = "ERROR"

// Invoke executes a state-changing contract call. The transaction is signed
// locally with signer; no private material ever leaves this process.
// It returns the on-chain transaction hash.
func (c *Client) Invoke(ctx context.Context, signer *keypair.Full, contractID, fn string, args []xdr.ScVal) (string, error) {
	if signer == nil {
		return "", errors.New("soroban: invoke requires a local signer")
	}
	op, err := newInvokeOp(contractID, fn, args)
	if err != nil {
		return "", err
	}

	// 1. Build the pre-simulation envelope (no Soroban extension yet).
	raw, seq, err := c.simulationEnvelope(ctx, signer, op)
	if err != nil {
		return "", err
	}

	// 2. Simulate to obtain the resource footprint, resource fee and auth.
	sim, err := c.rpc.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: raw})
	if err != nil {
		return "", err
	}
	if sim.Error != "" {
		return "", fmt.Errorf("soroban: simulation failed: %s", sim.Error)
	}

	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(sim.TransactionDataXDR, &sorobanData); err != nil {
		return "", fmt.Errorf("soroban: decoding transaction data: %w", err)
	}

	// Apply the auth entries the simulator derived for this call.
	var auth []xdr.SorobanAuthorizationEntry
	if len(sim.Results) > 0 && sim.Results[0].AuthXDR != nil {
		for _, entry := range *sim.Results[0].AuthXDR {
			var a xdr.SorobanAuthorizationEntry
			if err := xdr.SafeUnmarshalBase64(entry, &a); err != nil {
				return "", fmt.Errorf("soroban: decoding auth entry: %w", err)
			}
			auth = append(auth, a)
		}
	}

	// 3. Attach the simulated footprint/resource fee (Ext V=1 is mandatory:
	// a V=0 extension is serialized without the Soroban data and core rejects
	// the prepared transaction at flood time) and the recorded authorization.
	op.Auth = auth
	op.Ext = xdr.TransactionExt{V: 1, SorobanData: &sorobanData}

	// 4. Rebuild with the same sequence number. NewTransaction reads op.Ext
	// via BuildTransactionExt, emits a v1 envelope, and adds the resource fee
	// to the base inclusion fee automatically.
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &txnbuild.SimpleAccount{
			AccountID: signer.Address(),
			Sequence:  seq,
		},
		IncrementSequenceNum: true,
		BaseFee:              defaultBaseFee,
		Preconditions: txnbuild.Preconditions{
			TimeBounds: txnbuild.NewTimeout(int64(txTimeout.Seconds())),
		},
		Operations: []txnbuild.Operation{op},
	})
	if err != nil {
		return "", err
	}

	signed, err := tx.Sign(c.passphrase, signer)
	if err != nil {
		return "", err
	}
	b64, err := signed.Base64()
	if err != nil {
		return "", err
	}
	if os.Getenv("DMESH_DUMP_TX") == "1" {
		env := signed.ToXDR()
		fmt.Fprintf(os.Stderr, "[dmesh-tx-dump] fn=%s fee=%d resource_fee=%d xdr=%s\n",
			fn, env.V1.Tx.Fee, sorobanData.ResourceFee, b64)
	}

	// 5. Submit and wait for inclusion.
	sendResp, err := c.rpc.SendTransaction(ctx, protocol.SendTransactionRequest{Transaction: b64})
	if err != nil {
		return "", err
	}
	if sendResp.Status == txStatusError {
		return "", fmt.Errorf("soroban: transaction rejected: %s", sendResp.ErrorResultXDR)
	}

	poll, err := c.rpc.PollTransaction(ctx, sendResp.Hash)
	if err != nil {
		return "", err
	}
	if poll.Status != protocol.TransactionStatusSuccess {
		return "", fmt.Errorf("soroban: transaction %s ended with status %s", sendResp.Hash, poll.Status)
	}
	return sendResp.Hash, nil
}

// newInvokeOp builds a contract invocation operation without a Soroban
// extension; the extension and authorization are attached after simulation.
func newInvokeOp(contractID, fn string, args []xdr.ScVal) (*txnbuild.InvokeHostFunction, error) {
	hostFn, err := invokeHostFunction(contractID, fn, args)
	if err != nil {
		return nil, err
	}
	return &txnbuild.InvokeHostFunction{HostFunction: hostFn}, nil
}

// simulationEnvelope builds the unsigned pre-simulation envelope for an invoke
// operation and returns its base64 XDR plus the account's on-chain sequence
// number, so the post-simulation rebuild reuses the exact same sequence.
func (c *Client) simulationEnvelope(ctx context.Context, signer *keypair.Full, op *txnbuild.InvokeHostFunction) (string, int64, error) {
	account, err := c.rpc.LoadAccount(ctx, signer.Address())
	if err != nil {
		return "", 0, fmt.Errorf("soroban: loading source account: %w", err)
	}
	seq, err := account.GetSequenceNumber()
	if err != nil {
		return "", 0, fmt.Errorf("soroban: reading sequence number: %w", err)
	}
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        account,
		IncrementSequenceNum: true,
		BaseFee:              defaultBaseFee,
		Preconditions: txnbuild.Preconditions{
			TimeBounds: txnbuild.NewTimeout(int64(txTimeout.Seconds())),
		},
		Operations: []txnbuild.Operation{op},
	})
	if err != nil {
		return "", 0, err
	}
	envelope := tx.ToXDR()
	raw, err := xdr.MarshalBase64(&envelope)
	if err != nil {
		return "", 0, err
	}
	return raw, seq, nil
}
