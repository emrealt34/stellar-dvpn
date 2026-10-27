package geo

import (
	"net"
	"testing"
)

func TestClassifyDatacenterOrgs(t *testing.T) {
	cases := []string{
		"AMAZON-02 AWS EC2",
		"Google Cloud",
		"DIGITALOCEAN-ASN",
		"OVH SAS",
		"Hetzner Online GmbH",
		"Cloudflare, Inc.",
		"Contabo Asia Private Limited",
		"LeaseWeb Netherlands B.V.",
		"M247 Europe SRL",
		"Akamai International B.V.",
	}
	for _, org := range cases {
		if got := Classify(org); got != TierDatacenter {
			t.Errorf("Classify(%q) = %s, want %s", org, got, TierDatacenter)
		}
	}
}

func TestClassifyResidentialOrgs(t *testing.T) {
	cases := []string{
		"Turk Telekom",
		"Deutsche Telekom AG",
		"Comcast Cable Communications",
		"Virgin Media Limited",
		"Türk Telekomünikasyon A.Ş.",
	}
	for _, org := range cases {
		if got := Classify(org); got != TierResidential {
			t.Errorf("Classify(%q) = %s, want %s", org, got, TierResidential)
		}
	}
}

func TestWordBoundaryAvoidsFalsePositives(t *testing.T) {
	// "reservation" contains "server" as a substring but must not match.
	if got := Classify("Hotel Reservation Network"); got != TierResidential {
		t.Fatalf("expected residential, got %s", got)
	}
	if got := Classify(""); got != TierResidential {
		t.Fatalf("empty org should default to residential, got %s", got)
	}
}

func TestStaticLookup(t *testing.T) {
	lk := StaticLookup{
		"203.0.113.7": {
			CountryISO:  "TR",
			CountryName: "Turkey",
			ASN:         47331,
			ASNOrg:      "Mehmet's Broadband",
			Tier:        TierResidential,
		},
	}
	info, err := lk.Lookup(net.ParseIP("203.0.113.7"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Tier != TierResidential || info.CountryISO != "TR" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if _, err := lk.Lookup(net.ParseIP("198.51.100.1")); err == nil {
		t.Fatal("expected error for unknown IP")
	}
}
