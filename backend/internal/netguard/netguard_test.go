package netguard

import (
	"net"
	"strings"
	"testing"
)

func TestIsPublic(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.0.0.5", false},
		{"192.168.1.10", false},
		{"172.16.0.1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"fe80::1", false},         // link-local, also IPv6 metadata range
		{"fd00:ec2::254", false},   // unique-local
		{"224.0.0.1", false},       // multicast
		{"0.0.0.0", false},         // unspecified
	}
	for _, tc := range cases {
		if got := IsPublic(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("IsPublic(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestCheckHost(t *testing.T) {
	// Blocked by literal address without any DNS.
	for _, host := range []string{"127.0.0.1", "127.0.0.1:8080", "169.254.169.254", "10.1.2.3:443"} {
		if err := CheckHost(host); err == nil {
			t.Errorf("CheckHost(%q) expected error, got nil", host)
		}
	}
	// A public literal passes.
	if err := CheckHost("8.8.8.8:53"); err != nil {
		t.Errorf("CheckHost public address: %v", err)
	}
}

func TestSafeDialerRefusesLocal(t *testing.T) {
	d := SafeDialer()
	_, err := d.Dial("tcp", "127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("expected blocked destination error, got %v", err)
	}
}
