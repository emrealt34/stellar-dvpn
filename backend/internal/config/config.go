// Package config loads dmesh service settings from environment variables.
package config

import (
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultJWTSecret is the documented local-development placeholder. It must
// never be accepted in a production (non-dev) deployment.
const defaultJWTSecret = "change-me-in-production-32-bytes-minimum-secret"

// ValidateProduction fails fast when the service starts without a real JWT
// secret while dev OTP codes are disabled. A predictable secret would let an
// attacker forge dashboard tokens.
func (c *Config) ValidateProduction() error {
	secret := strings.TrimSpace(string(c.JWTSecret))
	if !c.DevCodes && (secret == "" || secret == defaultJWTSecret || len(secret) < 32) {
		return errors.New("JWT_SECRET must be set to a random value of at least 32 bytes when DEV_OTP_CODES is false")
	}
	return nil
}

type Config struct {
	Addr              string
	DBPath            string
	JWTSecret         []byte
	HorizonURL        string
	SorobanRPCURL     string
	NetworkPassphrase string
	// SettlementContractID is the C... address of the deployed dmesh
	// settlement contract. Empty until deployment is performed.
	SettlementContractID string
	// AdminStellarSecret is used only for contract administration calls
	// (tier and oracle-price updates). It never custody-creates user funds.
	AdminStellarSecret string
	TokenTTL           time.Duration
	OTPTTL             time.Duration
	DevCodes           bool
	AdminEmails        map[string]bool
	SMTPHost           string
	SMTPPort           int
	SMTPUser           string
	SMTPPass           string
	MailFrom           string
	MaxBandwidthBps    int64 // anti-fraud ceiling: bytes/sec per session
	SessionDefaultTTL  time.Duration
	HeartbeatTimeout   time.Duration
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("warning: invalid int %s=%q, using default %d", key, v, def)
	}
	return def
}

func Load() *Config {
	admins := map[string]bool{}
	for _, e := range strings.Split(os.Getenv("ADMIN_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			admins[e] = true
		}
	}
	// Bind address: explicit APP_ADDR wins; otherwise honor the platform
	// injected PORT (Render sets $PORT), defaulting to 8080 locally.
	addr := os.Getenv("APP_ADDR")
	if addr == "" {
		addr = ":" + env("PORT", "8080")
	}
	return &Config{
		Addr:                 addr,
		DBPath:               env("DB_PATH", "data/dmesh.db"),
		JWTSecret:            []byte(env("JWT_SECRET", defaultJWTSecret)),
		HorizonURL:           env("HORIZON_URL", "https://horizon-testnet.stellar.org"),
		SorobanRPCURL:        env("SOROBAN_RPC_URL", "https://soroban-testnet.stellar.org"),
		NetworkPassphrase:    env("NETWORK_PASSPHRASE", "Test SDF Network ; September 2015"),
		SettlementContractID: os.Getenv("SETTLEMENT_CONTRACT_ID"),
		AdminStellarSecret:   os.Getenv("ADMIN_STELLAR_SECRET"),
		TokenTTL:             time.Duration(envInt("TOKEN_TTL_HOURS", 720)) * time.Hour,
		OTPTTL:               time.Duration(envInt("OTP_TTL_MINUTES", 10)) * time.Minute,
		DevCodes:             env("DEV_OTP_CODES", "true") == "true",
		AdminEmails:          admins,
		SMTPHost:             os.Getenv("SMTP_HOST"),
		SMTPPort:             envInt("SMTP_PORT", 587),
		SMTPUser:             os.Getenv("SMTP_USER"),
		SMTPPass:             os.Getenv("SMTP_PASS"),
		MailFrom:             env("MAIL_FROM", "no-reply@dmesh.network"),
		MaxBandwidthBps:      int64(envInt("MAX_BANDWIDTH_BPS", 1_500_000_000)), // ~12 Gbps ceiling
		SessionDefaultTTL:    time.Duration(envInt("SESSION_TTL_MINUTES", 60)) * time.Minute,
		HeartbeatTimeout:     time.Duration(envInt("HEARTBEAT_TIMEOUT_MINUTES", 5)) * time.Minute,
	}
}
