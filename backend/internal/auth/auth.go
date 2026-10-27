// Package auth provides passwordless authentication primitives:
// one-time codes (email/SMS), JWT session tokens and key hashing.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/smtp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the JWT payload for an authenticated dmesh account.
type Claims struct {
	AccountID int64  `json:"aid"`
	Email     string `json:"email"`
	Admin     bool   `json:"admin"`
	jwt.RegisteredClaims
}

func MakeToken(accountID int64, email string, admin bool, secret []byte, ttl time.Duration) (string, error) {
	now := time.Now()
	c := Claims{
		AccountID: accountID,
		Email:     email,
		Admin:     admin,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Subject:   fmt.Sprintf("%d", accountID),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(secret)
}

func ParseToken(token string, secret []byte) (*Claims, error) {
	c := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return c, nil
}

// NewOTP generates a 6-digit numeric one-time code.
func NewOTP() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return fmt.Sprintf("%06d", n%1_000_000), nil
}

// HashCode hashes an OTP with a server-side pepper so stored codes are not reusable.
func HashCode(code string, pepper []byte) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte("dmesh-otp:" + code))
	return hex.EncodeToString(mac.Sum(nil))
}

// HashToken returns the SHA-256 hex digest of an API key.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NormalizeEmail lowercases and trims an email address.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ---- Message senders ----

type Sender interface {
	Send(to, subject, body string) error
}

// ConsoleSender prints messages to the server log (default for testnet demos).
type ConsoleSender struct{}

func (ConsoleSender) Send(to, subject, body string) error {
	fmt.Printf("[dmesh-mail] to=%s subject=%q\n%s\n", to, subject, body)
	return nil
}

// SMTPSender delivers email through a configured SMTP relay (STARTTLS/plain auth).
type SMTPSender struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

func (s SMTPSender) Send(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	auth := smtp.PlainAuth("", s.User, s.Pass, s.Host)
	msg := strings.Join([]string{
		"From: dmesh <" + s.From + ">",
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")
	return smtp.SendMail(addr, auth, s.From, []string{to}, []byte(msg))
}
