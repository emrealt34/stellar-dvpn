// Package stellarx wraps the Stellar testnet Horizon client used by dmesh.
// There is intentionally no mock implementation: every operation hits a real
// Stellar network. All user secret keys stay on user devices; this package
// only holds the platform treasury key needed to settle node payouts.
package stellarx

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/horizonclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/protocols/horizon/operations"
	"github.com/stellar/go-stellar-sdk/txnbuild"
)

type Client struct {
	HTTP              *http.Client
	Horizon           *horizonclient.Client
	BaseURL           string
	NetworkPassphrase string
}

func New(horizonURL, passphrase string) *Client {
	hc := &http.Client{Timeout: 30 * time.Second}
	return &Client{
		HTTP:              hc,
		Horizon:           &horizonclient.Client{HorizonURL: horizonURL, HTTP: hc},
		BaseURL:           horizonURL,
		NetworkPassphrase: passphrase,
	}
}

// GenerateKeypair creates a fresh Stellar ed25519 keypair.
func (c *Client) GenerateKeypair() (pub, secret string, err error) {
	kp, err := keypair.Random()
	if err != nil {
		return "", "", fmt.Errorf("keypair generation failed: %w", err)
	}
	return kp.Address(), kp.Seed(), nil
}

func ValidSecret(secret string) bool {
	_, err := keypair.ParseFull(secret)
	return err == nil
}

func ValidAddress(addr string) bool {
	_, err := keypair.ParseAddress(addr)
	return err == nil
}

// FundTestnetAccount funds an account through the testnet friendbot (10k test XLM).
func (c *Client) FundTestnetAccount(pub string) error {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return err
	}
	u.Path = "/friendbot"
	q := u.Query()
	q.Set("addr", pub)
	u.RawQuery = q.Encode()
	resp, err := c.HTTP.Get(u.String())
	if err != nil {
		return fmt.Errorf("friendbot request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("friendbot returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) source(secret string) (*keypair.Full, *txnbuild.SimpleAccount, error) {
	kp, err := keypair.ParseFull(secret)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid secret key: %w", err)
	}
	acc, err := c.Horizon.AccountDetail(horizonclient.AccountRequest{AccountID: kp.Address()})
	if err != nil {
		return nil, nil, wrapHorizon("account detail failed", err)
	}
	seq, err := acc.GetSequenceNumber()
	if err != nil {
		return nil, nil, fmt.Errorf("sequence read failed: %w", err)
	}
	return kp, &txnbuild.SimpleAccount{AccountID: kp.Address(), Sequence: seq}, nil
}

// AddTrustline builds, signs and submits a ChangeTrust transaction.
func (c *Client) AddTrustline(userSecret, issuerPub, assetCode string) (string, error) {
	kp, source, err := c.source(userSecret)
	if err != nil {
		return "", err
	}
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        source,
		IncrementSequenceNum: true,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		Operations: []txnbuild.Operation{
			&txnbuild.ChangeTrust{Line: txnbuild.ChangeTrustAssetWrapper{
				Asset: txnbuild.CreditAsset{Code: assetCode, Issuer: issuerPub},
			}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("trustline tx build failed: %w", err)
	}
	tx, err = tx.Sign(c.NetworkPassphrase, kp)
	if err != nil {
		return "", fmt.Errorf("trustline sign failed: %w", err)
	}
	resp, err := c.Horizon.SubmitTransaction(tx)
	if err != nil {
		return "", wrapHorizon("trustline submit failed", err)
	}
	return resp.Hash, nil
}

type PaymentParams struct {
	FromSecret string
	To         string
	Amount     string
	AssetCode  string
	IssuerPub  string // empty = native XLM
	MemoText   string
}

// SendPayment builds, signs and submits a payment, returning the transaction hash.
func (c *Client) SendPayment(p PaymentParams) (string, error) {
	kp, source, err := c.source(p.FromSecret)
	if err != nil {
		return "", err
	}
	var asset txnbuild.Asset
	if p.AssetCode == "" || p.AssetCode == "XLM" {
		asset = txnbuild.NativeAsset{}
	} else {
		asset = txnbuild.CreditAsset{Code: p.AssetCode, Issuer: p.IssuerPub}
	}
	params := txnbuild.TransactionParams{
		SourceAccount:        source,
		IncrementSequenceNum: true,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		Operations: []txnbuild.Operation{
			&txnbuild.Payment{Destination: p.To, Amount: p.Amount, Asset: asset},
		},
	}
	if p.MemoText != "" {
		params.Memo = txnbuild.MemoText(p.MemoText)
	}
	tx, err := txnbuild.NewTransaction(params)
	if err != nil {
		return "", fmt.Errorf("payment tx build failed: %w", err)
	}
	tx, err = tx.Sign(c.NetworkPassphrase, kp)
	if err != nil {
		return "", fmt.Errorf("payment sign failed: %w", err)
	}
	resp, err := c.Horizon.SubmitTransaction(tx)
	if err != nil {
		return "", wrapHorizon("payment submit failed", err)
	}
	return resp.Hash, nil
}

type AssetBalance struct {
	Asset   string  `json:"asset"`
	Issuer  string  `json:"issuer,omitempty"`
	Balance float64 `json:"balance"`
}

func (c *Client) Balances(pub string) ([]AssetBalance, error) {
	acc, err := c.Horizon.AccountDetail(horizonclient.AccountRequest{AccountID: pub})
	if err != nil {
		return nil, wrapHorizon("balances failed", err)
	}
	out := make([]AssetBalance, 0, len(acc.Balances))
	for _, b := range acc.Balances {
		amt, err := strconv.ParseFloat(b.Balance, 64)
		if err != nil {
			continue
		}
		if b.Asset.Type == "native" {
			out = append(out, AssetBalance{Asset: "XLM", Balance: amt})
		} else {
			out = append(out, AssetBalance{Asset: b.Asset.Code, Issuer: b.Asset.Issuer, Balance: amt})
		}
	}
	return out, nil
}

// IncomingPayment is a filtered payment observed on the treasury account.
type IncomingPayment struct {
	TxHash string
	Memo   string
	Asset  string
	Issuer string
	Amount float64
	Ledger int64
}

// IncomingPayments lists successful payment operations received by accountID,
// oldest first, supporting cursor-based polling through the Horizon cursor.
func (c *Client) IncomingPayments(accountID, cursor string, limit int) ([]IncomingPayment, string, error) {
	req := horizonclient.TransactionRequest{
		ForAccount: accountID,
		Order:      horizonclient.OrderAsc,
		Limit:      uint(limit),
	}
	if cursor != "" {
		req.Cursor = cursor
	}
	page, err := c.Horizon.Transactions(req)
	if err != nil {
		return nil, "", wrapHorizon("transactions failed", err)
	}
	var out []IncomingPayment
	next := cursor
	for _, tx := range page.Embedded.Records {
		next = tx.PagingToken()
		if !tx.Successful {
			continue
		}
		ops, err := c.Horizon.Operations(horizonclient.OperationRequest{ForTransaction: tx.ID})
		if err != nil {
			return nil, next, wrapHorizon("operations failed", err)
		}
		for _, op := range ops.Embedded.Records {
			pay, ok := op.(operations.Payment)
			if !ok {
				continue
			}
			if pay.To != accountID {
				continue
			}
			amt, err := strconv.ParseFloat(pay.Amount, 64)
			if err != nil {
				continue
			}
			ip := IncomingPayment{
				TxHash: tx.ID,
				Memo:   tx.Memo,
				Amount: amt,
				Ledger: int64(tx.Ledger),
			}
			if pay.Asset.Type == "native" {
				ip.Asset = "XLM"
			} else {
				ip.Asset = pay.Asset.Code
				ip.Issuer = pay.Asset.Issuer
			}
			out = append(out, ip)
		}
	}
	return out, next, nil
}

func wrapHorizon(prefix string, err error) error {
	var hErr *horizonclient.Error
	if errors.As(err, &hErr) {
		return fmt.Errorf("%s: %s — %s", prefix, hErr.Problem.Title, hErr.Problem.Detail)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}
