package nat

import (
	"net"
	"testing"
	"time"
)

// xorMapped builds an RFC 5389 XOR-MAPPED-ADDRESS attribute value.
func xorMapped(ip net.IP, port int) []byte {
	v := make([]byte, 8)
	v[1] = attrFamilyIPv4
	v[2], v[3] = byte(port>>8), byte(port)
	v4 := ip.To4()
	v[4], v[5], v[6], v[7] = v4[0]^0x21, v4[1]^0x12, v4[2]^0xA4, v4[3]^0x42
	v[2] ^= byte((stunMagicCookie >> 24) & 0xff)
	v[3] ^= byte((stunMagicCookie >> 16) & 0xff)
	return v
}

func writeResponse(t *testing.T, txID [12]byte, ip net.IP, port int) []byte {
	t.Helper()
	attr := xorMapped(ip, port)
	pkt := make([]byte, stunHeaderSize+4+len(attr))
	pkt[0], pkt[1] = byte(stunBindingResp>>8), byte(stunBindingResp&0xff)
	pkt[2], pkt[3] = 0, byte(4+len(attr))
	pkt[4], pkt[5], pkt[6], pkt[7] = 0x21, 0x12, 0xA4, 0x42
	copy(pkt[8:20], txID[:])
	pkt[20], pkt[21] = byte(attrXorMapped>>8), byte(attrXorMapped)
	pkt[22], pkt[23] = 0, byte(len(attr))
	copy(pkt[24:], attr)
	return pkt
}

// fakeSTUN replies with a fixed reflexive IP/port regardless of the peer.
func fakeSTUN(t *testing.T, ip net.IP, port int) string {
	t.Helper()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			n, peer, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < stunHeaderSize {
				continue
			}
			var tx [12]byte
			copy(tx[:], buf[8:20])
			_, _ = conn.WriteToUDP(writeResponse(t, tx, ip, port), peer)
		}
	}()
	return conn.LocalAddr().String()
}

func TestParseBindingResponse(t *testing.T) {
	var tx [12]byte
	for i := range tx {
		tx[i] = byte(i + 1)
	}
	ip := net.IPv4(203, 0, 113, 7)
	pkt := writeResponse(t, tx, ip, 54321)
	gotIP, gotPort, err := ParseBindingResponse(pkt, tx)
	if err != nil {
		t.Fatal(err)
	}
	if gotIP.String() != ip.String() || gotPort != 54321 {
		t.Fatalf("got %s:%d, want %s:54321", gotIP, gotPort, ip)
	}
}

func TestClassifyCone(t *testing.T) {
	Timeout = 250 * time.Millisecond
	t.Cleanup(func() { Timeout = defaultTimeout })
	ip := net.IPv4(203, 0, 113, 7)
	s1 := fakeSTUN(t, ip, 40001)
	s2 := fakeSTUN(t, ip, 40001) // same reflexive mapping from a second server
	rep := Classify([]string{s1, s2})
	if rep.NATType != Cone {
		t.Fatalf("expected cone, got %s (hints=%v)", rep.NATType, rep.Hints)
	}
	if rep.Connectivity != HolePunch {
		t.Fatalf("expected hole-punch connectivity, got %s", rep.Connectivity)
	}
}

func TestClassifySymmetric(t *testing.T) {
	Timeout = 250 * time.Millisecond
	t.Cleanup(func() { Timeout = defaultTimeout })
	ip := net.IPv4(203, 0, 113, 7)
	s1 := fakeSTUN(t, ip, 40001)
	s2 := fakeSTUN(t, ip, 49999) // different reflexive port per destination
	rep := Classify([]string{s1, s2})
	if rep.NATType != Symmetric {
		t.Fatalf("expected symmetric, got %s", rep.NATType)
	}
	if rep.Connectivity != RelayRequired {
		t.Fatalf("expected relay-required, got %s", rep.Connectivity)
	}
}

func TestClassifyUDPBlocked(t *testing.T) {
	Timeout = 100 * time.Millisecond
	t.Cleanup(func() { Timeout = defaultTimeout })
	// Nothing listens on this port; all probes time out.
	rep := Classify([]string{"127.0.0.1:9"})
	if rep.NATType != UDPBlocked {
		t.Fatalf("expected udp-blocked, got %s", rep.NATType)
	}
}

func TestCGNATRange(t *testing.T) {
	cases := map[string]bool{
		"100.64.0.1": true, "100.127.255.254": true, "100.128.0.1": false,
		"10.0.0.1": false, "8.8.8.8": false,
	}
	for in, want := range cases {
		if got := isCGNAT(in); got != want {
			t.Errorf("isCGNAT(%s)=%v want %v", in, got, want)
		}
	}
}
