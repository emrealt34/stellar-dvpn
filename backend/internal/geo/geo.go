// Package geo determines the physical country and network tier (residential
// vs datacenter) of an IP address. Both node and consuming client run this
// same, deterministic classification locally against the CI-published GeoLite2
// databases so the two peers independently agree on the tier. The coordinator
// does not classify anything; it only stores the node's signed claim.
package geo

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/oschwald/geoip2-golang"
)

// Tier is the pricing class assigned to a node's IP address.
type Tier string

const (
	// TierResidential prices traffic at the residential rate.
	TierResidential Tier = "res"
	// TierDatacenter prices traffic at the cheaper datacenter rate.
	TierDatacenter Tier = "dc"
)

// Info is the backend-verified geo classification of one IP.
type Info struct {
	CountryISO  string
	CountryName string
	ASN         uint
	ASNOrg      string
	Tier        Tier
}

// Lookup abstracts the geo database so tests can substitute a fixture.
type Lookup interface {
	Lookup(ip net.IP) (Info, error)
	Close() error
}

// hostingKeywords flags ASN organisations that are obviously networks of
// servers rather than end-user broadband. Matching is case-insensitive and
// word-anchored on the organisation name.
var hostingKeywords = []string{
	"hosting", "host", "cloud", "datacenter", "data center", "data-center",
	"colocation", "colo", "vps", "vpn", "proxy", "server", "servers",
	"dedicated", "webfarm", "web farm", "isp services", "network services",
	"amazon", "aws", "google", "microsoft", "azure", "oracle cloud",
	"digitalocean", "ovh", "hetzner", "linode", "akamai", "cloudflare",
	"vultr", "contabo", "leaseweb", "choopa", "m247", "scaleway",
}

// Classify decides the tier from an ASN organisation string. It is a pure
// function so the policy is unit-testable without any database files.
func Classify(asnOrg string) Tier {
	org := strings.ToLower(asnOrg)
	for _, kw := range hostingKeywords {
		if containsWord(org, kw) {
			return TierDatacenter
		}
	}
	return TierResidential
}

// containsWord reports whether s contains kw on a word boundary, so that
// e.g. "server" matches "Global Server Hosting" but not "reservation".
func containsWord(s, kw string) bool {
	idx := 0
	for {
		j := strings.Index(s[idx:], kw)
		if j < 0 {
			return false
		}
		start := idx + j
		end := start + len(kw)
		leftOK := start == 0 || !isAlphaNum(rune(s[start-1]))
		rightOK := end == len(s) || !isAlphaNum(rune(s[end]))
		if leftOK && rightOK {
			return true
		}
		idx = end
	}
}

func isAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// MaxMindLookup reads GeoLite2-Country and GeoLite2-ASN databases. Both
// files are required; CI refreshes them on a daily schedule.
type MaxMindLookup struct {
	country *geoip2.Reader
	asn     *geoip2.Reader
}

// Open loads the two MaxMind binary databases.
func Open(countryDBPath, asnDBPath string) (*MaxMindLookup, error) {
	if countryDBPath == "" || asnDBPath == "" {
		return nil, errors.New("geo: country and ASN database paths are both required")
	}
	country, err := geoip2.Open(countryDBPath)
	if err != nil {
		return nil, fmt.Errorf("geo: opening country db: %w", err)
	}
	asn, err := geoip2.Open(asnDBPath)
	if err != nil {
		country.Close()
		return nil, fmt.Errorf("geo: opening asn db: %w", err)
	}
	return &MaxMindLookup{country: country, asn: asn}, nil
}

// Close releases both database readers.
func (m *MaxMindLookup) Close() error {
	errC := m.country.Close()
	errA := m.asn.Close()
	if errC != nil {
		return errC
	}
	return errA
}

// Lookup classifies a single IP.
func (m *MaxMindLookup) Lookup(ip net.IP) (Info, error) {
	if ip == nil {
		return Info{}, errors.New("geo: nil ip")
	}
	country, err := m.country.Country(ip)
	if err != nil {
		return Info{}, fmt.Errorf("geo: country lookup: %w", err)
	}
	asn, err := m.asn.ASN(ip)
	if err != nil {
		return Info{}, fmt.Errorf("geo: asn lookup: %w", err)
	}
	org := asn.AutonomousSystemOrganization
	info := Info{
		CountryISO:  country.Country.IsoCode,
		CountryName: country.Country.Names["en"],
		ASN:         asn.AutonomousSystemNumber,
		ASNOrg:      org,
		Tier:        Classify(org),
	}
	return info, nil
}

// StaticLookup is used in tests and local development with explicit entries.
type StaticLookup map[string]Info

// Lookup returns the stored entry keyed by IP string.
func (s StaticLookup) Lookup(ip net.IP) (Info, error) {
	if info, ok := s[ip.String()]; ok {
		return info, nil
	}
	return Info{}, fmt.Errorf("geo: no static entry for %s", ip.String())
}

// Close satisfies Lookup.
func (s StaticLookup) Close() error { return nil }
