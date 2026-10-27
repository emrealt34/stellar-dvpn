// Package store persists dmesh coordinator state. The smart contract is the
// only source of truth for money; this database holds coordination metadata
// (node directory, session lifecycle, traffic evidence) and can always be
// rebuilt from contract events.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store wraps the SQLite database.
type Store struct{ db *sql.DB }

// Node is a mesh participant identified solely by its Stellar G address.
type Node struct {
	ID          int64     `json:"id"`
	MeshAddress string    `json:"mesh_address"` // G... address, the only identity required
	Email       string    `json:"email,omitempty"` // optional, linked via dashboard OTP
	CountryISO  string    `json:"country_iso"`
	CountryName string    `json:"country"`
	ASN         uint      `json:"asn"`
	ASNOrg      string    `json:"asn_org"`
	Tier        string    `json:"tier"` // geo.TierResidential ("res") or geo.TierDatacenter ("dc")
	IP          string    `json:"-"` // never serialized to clients
	Status      string    `json:"status"` // "online" | "offline"
	UpBytes     int64     `json:"up_bytes"`
	DownBytes   int64    `json:"down_bytes"`
	LastSeen    time.Time `json:"last_seen"`
	CreatedAt   time.Time `json:"created_at"`
}

// Session mirrors the lifecycle of an on-chain settlement session. The
// contract session id is stored as decimal text because it is a u128.
type Session struct {
	ID                int64     `json:"id"`
	ContractSessionID string    `json:"contract_session_id"`
	ClientAddress     string    `json:"client_address"`
	NodeAddress       string    `json:"node_address"`
	TokenAddress      string    `json:"token_address"`
	Tier              string    `json:"tier"`
	NonceHex          string    `json:"nonce_hex"`
	ExpiresAt         time.Time `json:"expires_at"`
	Status      string    `json:"status"` // pending | opened | proposed | settled | cancelled | expired
	UpBytes           int64     `json:"up_bytes"`
	DownBytes         int64    `json:"down_bytes"`
	TxOpen            string    `json:"tx_open,omitempty"`
	TxPropose         string    `json:"tx_propose,omitempty"`
	TxConfirm         string    `json:"tx_confirm,omitempty"`
	TxCancel          string    `json:"tx_cancel,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// UsageRecord is one counter report submitted by a party.
type UsageRecord struct {
	ID        int64     `json:"id"`
	SessionID int64     `json:"session_id"`
	Reporter  string    `json:"reporter"` // "client" | "node"
	UpBytes   int64     `json:"up_bytes"`
	DownBytes int64     `json:"down_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

// AuthCode is an email OTP used by the dashboard only.
type AuthCode struct {
	Email     string
	CodeHash  string
	ExpiresAt time.Time
}

// Open initializes the database and runs the schema migration.
func Open(path string) (*Store, error) {
	// Ensure the parent directory exists (container/ephemeral filesystems).
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite serialised access
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS nodes (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	mesh_address  TEXT NOT NULL UNIQUE,
	email         TEXT NOT NULL DEFAULT '',
	country_iso   TEXT NOT NULL DEFAULT '',
	country_name  TEXT NOT NULL DEFAULT '',
	asn           INTEGER NOT NULL DEFAULT 0,
	asn_org       TEXT NOT NULL DEFAULT '',
	tier          TEXT NOT NULL DEFAULT 'res',
	ip            TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'offline',
	up_bytes      INTEGER NOT NULL DEFAULT 0,
	down_bytes    INTEGER NOT NULL DEFAULT 0,
	last_seen     TIMESTAMP NOT NULL,
	created_at    TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	id                  INTEGER PRIMARY KEY AUTOINCREMENT,
	contract_session_id TEXT NOT NULL DEFAULT '',
	client_address      TEXT NOT NULL,
	node_address        TEXT NOT NULL,
	token_address       TEXT NOT NULL,
	tier                TEXT NOT NULL,
	nonce_hex           TEXT NOT NULL UNIQUE,
	expires_at          TIMESTAMP NOT NULL,
	status              TEXT NOT NULL DEFAULT 'opened',
	up_bytes            INTEGER NOT NULL DEFAULT 0,
	down_bytes          INTEGER NOT NULL DEFAULT 0,
	tx_open             TEXT NOT NULL DEFAULT '',
	tx_propose          TEXT NOT NULL DEFAULT '',
	tx_confirm          TEXT NOT NULL DEFAULT '',
	tx_cancel           TEXT NOT NULL DEFAULT '',
	created_at          TIMESTAMP NOT NULL,
	updated_at          TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_client ON sessions(client_address);
CREATE INDEX IF NOT EXISTS idx_sessions_node ON sessions(node_address);
CREATE TABLE IF NOT EXISTS usage_records (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id INTEGER NOT NULL,
	reporter   TEXT NOT NULL,
	up_bytes   INTEGER NOT NULL,
	down_bytes INTEGER NOT NULL,
	created_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_codes (
	email      TEXT NOT NULL,
	code_hash  TEXT NOT NULL,
	expires_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS dashboard_links (
	mesh_address TEXT PRIMARY KEY,
	email        TEXT NOT NULL,
	linked_at    TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`)
	return err
}

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------

// GetSetting returns ErrNotFound when the key is absent.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetSetting upserts a key/value pair.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---------------------------------------------------------------------------
// nodes
// ---------------------------------------------------------------------------

const nodeColumns = `id, mesh_address, COALESCE(email,''), country_iso, country_name, asn, asn_org, tier, ip, status,
	up_bytes, down_bytes, last_seen, created_at`

func scanNode(row interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.MeshAddress, &n.Email, &n.CountryISO, &n.CountryName, &n.ASN, &n.ASNOrg,
		&n.Tier, &n.IP, &n.Status, &n.UpBytes, &n.DownBytes, &n.LastSeen, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

// UpsertNode registers a node by mesh address, stamping the backend-derived
// geo classification. Re-registration refreshes geo data and last_seen.
func (s *Store) UpsertNode(n *Node) (*Node, error) {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO nodes(mesh_address, country_iso, country_name, asn, asn_org, tier, ip, status, last_seen, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, 'online', ?, ?)
		 ON CONFLICT(mesh_address) DO UPDATE SET
		   country_iso = excluded.country_iso, country_name = excluded.country_name,
		   asn = excluded.asn, asn_org = excluded.asn_org, tier = excluded.tier,
		   ip = excluded.ip, status = 'online', last_seen = excluded.last_seen`,
		n.MeshAddress, n.CountryISO, n.CountryName, n.ASN, n.ASNOrg, n.Tier, n.IP, now, now)
	if err != nil {
		return nil, err
	}
	return s.NodeByMeshAddress(n.MeshAddress)
}

// NodeByMeshAddress fetches a node by its G address.
func (s *Store) NodeByMeshAddress(addr string) (*Node, error) {
	row := s.db.QueryRow(`SELECT `+nodeColumns+` FROM nodes WHERE mesh_address = ?`, addr)
	return scanNode(row)
}

// ListOnlineNodes returns the public directory of nodes still heartbeating.
func (s *Store) ListOnlineNodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeColumns + ` FROM nodes WHERE status = 'online' ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// Heartbeat updates liveness and cumulative counters.
func (s *Store) Heartbeat(meshAddress string, up, down int64) error {
	res, err := s.db.Exec(
		`UPDATE nodes SET status='online', up_bytes=?, down_bytes=?, last_seen=? WHERE mesh_address=?`,
		up, down, time.Now().UTC(), meshAddress)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkStaleNodes flips nodes past the heartbeat timeout to offline and
// returns how many were changed.
func (s *Store) MarkStaleNodes(timeout time.Duration) (int64, error) {
	res, err := s.db.Exec(
		`UPDATE nodes SET status='offline'
		 WHERE status='online' AND last_seen < ?`, time.Now().UTC().Add(-timeout))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LinkNodeEmail associates a dashboard-verified email with a node.
func (s *Store) LinkNodeEmail(meshAddress, email string) error {
	now := time.Now().UTC()
	if _, err := s.db.Exec(
		`UPDATE nodes SET email=? WHERE mesh_address=?`, email, meshAddress); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO dashboard_links(mesh_address, email, linked_at) VALUES(?, ?, ?)
		 ON CONFLICT(mesh_address) DO UPDATE SET email=excluded.email, linked_at=excluded.linked_at`,
		meshAddress, email, now)
	return err
}

// NodesByEmail lists every node managed by one dashboard account.
func (s *Store) NodesByEmail(email string) ([]Node, error) {
	rows, err := s.db.Query(`SELECT `+nodeColumns+` FROM nodes WHERE email = ? ORDER BY created_at DESC`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------

const sessionColumns = `id, contract_session_id, client_address, node_address, token_address, tier,
	nonce_hex, expires_at, status, up_bytes, down_bytes,
	tx_open, tx_propose, tx_confirm, tx_cancel, created_at, updated_at`

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	var v Session
	err := row.Scan(&v.ID, &v.ContractSessionID, &v.ClientAddress, &v.NodeAddress, &v.TokenAddress, &v.Tier,
		&v.NonceHex, &v.ExpiresAt, &v.Status, &v.UpBytes, &v.DownBytes,
		&v.TxOpen, &v.TxPropose, &v.TxConfirm, &v.TxCancel, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &v, err
}

// ErrSessionNotAttachable indicates an on-chain open tx cannot be attached to
// the session in its current lifecycle state.
var ErrSessionNotAttachable = errors.New("session cannot accept an on-chain open transaction in its current state")

// CreatePendingSession records a coordinator-issued session before the client
// submits the on-chain open_session transaction. The session starts in the
// "pending" state and cannot carry data-plane traffic until the client proves
// the on-chain open_session via AttachOpenTx, which promotes it to "opened".
func (s *Store) CreatePendingSession(v *Session) (*Session, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO sessions(contract_session_id, client_address, node_address, token_address, tier,
		   nonce_hex, expires_at, status, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		v.ContractSessionID, v.ClientAddress, v.NodeAddress, v.TokenAddress, v.Tier,
		v.NonceHex, v.ExpiresAt, now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetSession(id)
}

// GetSession fetches by internal id.
func (s *Store) GetSession(id int64) (*Session, error) {
	return scanSession(s.db.QueryRow(`SELECT `+sessionColumns+` FROM sessions WHERE id=?`, id))
}

// SessionByNonce fetches by the unique session nonce.
func (s *Store) SessionByNonce(nonceHex string) (*Session, error) {
	return scanSession(s.db.QueryRow(`SELECT `+sessionColumns+` FROM sessions WHERE nonce_hex=?`, nonceHex))
}

// NonceUsed reports whether a nonce was already coordinated.
func (s *Store) NonceUsed(nonceHex string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM sessions WHERE nonce_hex=?`, nonceHex).Scan(&n)
	return n > 0, err
}

// AttachOpenTx records the on-chain id and open tx once the client's
// transaction lands, promoting the session from "pending" to "opened". Only
// sessions that have not yet been proposed, settled or cancelled are
// attachable, so the data plane can only serve traffic backed by a real
// client-signed on-chain open_session.
func (s *Store) AttachOpenTx(id int64, contractSessionID, txHash string) error {
	res, err := s.db.Exec(
		`UPDATE sessions
		    SET contract_session_id=?, tx_open=?,
		        status=CASE WHEN status='pending' THEN 'opened' ELSE status END,
		        updated_at=?
		  WHERE id=? AND status IN ('pending','opened')`,
		contractSessionID, txHash, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSessionNotAttachable
	}
	return nil
}

// SetSessionStatus advances the lifecycle and records the related tx hash.
func (s *Store) SetSessionStatus(id int64, status, txHash string, up, down int64) error {
	var col string
	switch status {
	case "proposed":
		col = "tx_propose"
	case "settled":
		col = "tx_confirm"
	case "cancelled":
		col = "tx_cancel"
	default:
		col = ""
	}
	if col == "" {
		_, err := s.db.Exec(
			`UPDATE sessions SET status=?, up_bytes=?, down_bytes=?, updated_at=? WHERE id=?`,
			status, up, down, time.Now().UTC(), id)
		return err
	}
	_, err := s.db.Exec(
		fmt.Sprintf(`UPDATE sessions SET status=?, %s=?, up_bytes=?, down_bytes=?, updated_at=? WHERE id=?`, col),
		status, txHash, up, down, time.Now().UTC(), id)
	return err
}

// ExpireDueSessions marks un-settled, un-cancelled sessions past their TTL.
// Pending sessions (never opened on chain) are reaped as well.
func (s *Store) ExpireDueSessions() (int64, error) {
	res, err := s.db.Exec(
		`UPDATE sessions SET status='expired', updated_at=?
		 WHERE status IN ('pending','opened','proposed') AND expires_at < ?`,
		time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListSessionsByParty returns sessions where address is client or node.
func (s *Store) ListSessionsByParty(address string, limit int) ([]Session, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT `+sessionColumns+` FROM sessions
		 WHERE client_address=? OR node_address=? ORDER BY id DESC LIMIT ?`, address, address, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		v, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// InsertUsageRecord stores one counter report as off-chain evidence.
func (s *Store) InsertUsageRecord(sessionID int64, reporter string, up, down int64) error {
	_, err := s.db.Exec(
		`INSERT INTO usage_records(session_id, reporter, up_bytes, down_bytes, created_at)
		 VALUES(?, ?, ?, ?, ?)`, sessionID, reporter, up, down, time.Now().UTC())
	return err
}

// LastUsageRecord returns the most recent report from one party, or nil.
func (s *Store) LastUsageRecord(sessionID int64, reporter string) (*UsageRecord, error) {
	var r UsageRecord
	err := s.db.QueryRow(
		`SELECT id, session_id, reporter, up_bytes, down_bytes, created_at
		 FROM usage_records WHERE session_id=? AND reporter=?
		 ORDER BY id DESC LIMIT 1`, sessionID, reporter).
		Scan(&r.ID, &r.SessionID, &r.Reporter, &r.UpBytes, &r.DownBytes, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ---------------------------------------------------------------------------
// dashboard OTP
// ---------------------------------------------------------------------------

// SaveAuthCode stores a hashed OTP for an email.
func (s *Store) SaveAuthCode(email, codeHash string, expiresAt time.Time) error {
	_, err := s.db.Exec(`INSERT INTO auth_codes(email, code_hash, expires_at) VALUES(?, ?, ?)`,
		email, codeHash, expiresAt)
	return err
}

// ConsumeAuthCode atomically validates and deletes a single-use OTP.
func (s *Store) ConsumeAuthCode(email, codeHash string) (bool, error) {
	res, err := s.db.Exec(
		`DELETE FROM auth_codes WHERE email=? AND code_hash=? AND expires_at > ?`,
		email, codeHash, time.Now().UTC())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
