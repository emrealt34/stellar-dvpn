// Package nat classifies the network path a node is reachable over. It sends
// RFC 5389 STUN binding requests to independent public STUN servers, reads the
// reflexive transport address the servers observe, and distinguishes an open
// public address, a cone NAT (hole-punch friendly) and a symmetric NAT/CGNAT
// (which needs port forwarding or a relay). The coordinator never performs
// this probe: the node runs it locally and signs the result into its claim.
package nat

import (
	"errors"
	"fmt"
	"net"
	"time"
)

// Classification labels understood by the directory and the website.
const (
	Public      = "public"    // no NAT, the local socket is publicly reachable
	Cone        = "cone"      // endpoint-independent mapping: full/restricted/port-restricted cone
	Symmetric   = "symmetric" // mapping depends on the destination; typical of CGNAT
	UDPBlocked  = "udp-blocked"
	ProbeFailed = "probe-failed"
)

// Connectivity hints tell a client how a node can be reached.
const (
	Direct        = "direct"         // open inbound TCP once the port is listening
	HolePunch     = "hole-punch"     // reachable with NAT traversal / port mapping
	RelayRequired = "relay-required" // no direct inbound path without an external relay
	Unknown       = "unknown"
)

// DefaultSTUN are independent anycast/unicast STUN endpoints run by large
// providers; two different operators are needed to detect symmetric NAT.
var DefaultSTUN = []string{
	"stun.l.google.com:19302",
	"stun.cloudflare.com:3478",
	"stun1.l.google.com:19302",
	"stun.nextcloud.com:3478",
}

// Observation is one STUN server's view of our socket.
type Observation struct {
	Server     string `json:"server"`
	PublicIP   string `json:"public_ip"`
	PublicPort int    `json:"public_port"`
	RTTMS      int64  `json:"rtt_ms"`
}

// Report is the result of a full NAT probe.
type Report struct {
	LocalIP      string        `json:"local_ip"`
	NATType      string        `json:"nat_type"`
	CGNAT        bool          `json:"cgnat"`
	Connectivity string        `json:"connectivity"`
	PublicIP     string        `json:"public_ip"`
	PublicPort   int           `json:"public_port"`
	Observations []Observation `json:"observations"`
	Hints        []string      `json:"hints"`
}

const (
	stunHeaderSize  = 20
	stunMagicCookie = 0x2112A442
	stunBindingReq  = 0x0001
	stunBindingResp = 0x0101
	attrMappedAddr  = 0x0001
	attrXorMapped   = 0x0020
	attrFamilyIPv4  = 0x01
	defaultTimeout  = 3 * time.Second
)

// Timeout bounds each STUN request; tests may shorten it.
var Timeout = defaultTimeout

// BindingRequest builds a minimal RFC 5389 binding request with a random
// transaction ID. No attributes are required for an unauthenticated STUN server.
func BindingRequest(txID [12]byte) []byte {
	pkt := make([]byte, stunHeaderSize)
	// message type (big endian uint16), message length 0, magic cookie, tx id
	pkt[0], pkt[1] = byte(stunBindingReq>>8), byte(stunBindingReq)
	pkt[4], pkt[5], pkt[6], pkt[7] = 0x21, 0x12, 0xA4, 0x42
	copy(pkt[8:20], txID[:])
	return pkt
}

// ParseBindingResponse extracts the reflexive address from a binding response,
// preferring XOR-MAPPED-ADDRESS and falling back to legacy MAPPED-ADDRESS.
func ParseBindingResponse(pkt []byte, txID [12]byte) (net.IP, int, error) {
	if len(pkt) < stunHeaderSize {
		return nil, 0, errors.New("stun: short response")
	}
	msgType := uint16(pkt[0])<<8 | uint16(pkt[1])
	if msgType != stunBindingResp {
		return nil, 0, fmt.Errorf("stun: unexpected message type 0x%04x", msgType)
	}
	cookie := uint32(pkt[4])<<24 | uint32(pkt[5])<<16 | uint32(pkt[6])<<8 | uint32(pkt[7])
	if cookie != stunMagicCookie {
		return nil, 0, errors.New("stun: bad magic cookie")
	}
	var gotTx [12]byte
	copy(gotTx[:], pkt[8:20])
	if gotTx != txID {
		return nil, 0, errors.New("stun: transaction id mismatch")
	}
	body := pkt[stunHeaderSize:]
	for len(body) >= 4 {
		attrType := uint16(body[0])<<8 | uint16(body[1])
		attrLen := int(body[2])<<8 | int(body[3])
		if len(body) < 4+attrLen {
			return nil, 0, errors.New("stun: truncated attribute")
		}
		value := body[4 : 4+attrLen]
		if attrType == attrXorMapped || attrType == attrMappedAddr {
			if attrLen < 8 || value[1] != attrFamilyIPv4 {
				return nil, 0, errors.New("stun: only IPv4 mapped addresses are supported")
			}
			port := int(value[2])<<8 | int(value[3])
			b0, b1, b2, b3 := value[4], value[5], value[6], value[7]
			if attrType == attrXorMapped {
				port ^= stunMagicCookie >> 16
				b0 ^= 0x21
				b1 ^= 0x12
				b2 ^= 0xA4
				b3 ^= 0x42
			}
			return net.IPv4(b0, b1, b2, b3), port, nil
		}
		body = body[4+attrLen:]
	}
	return nil, 0, errors.New("stun: no mapped address attribute")
}

// queryOne sends one binding request over the shared socket and returns the
// server-observed reflexive address.
func queryOne(conn *net.UDPConn, server string, timeout time.Duration) (Observation, error) {
	var txID [12]byte
	if _, err := readRand(txID[:]); err != nil {
		return Observation{}, err
	}
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return Observation{}, err
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return Observation{}, err
	}
	start := time.Now()
	if _, err := conn.WriteToUDP(BindingRequest(txID), addr); err != nil {
		return Observation{}, err
	}
	buf := make([]byte, 1500)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return Observation{}, err
	}
	ip, port, err := ParseBindingResponse(buf[:n], txID)
	if err != nil {
		return Observation{}, err
	}
	return Observation{
		Server:     server,
		PublicIP:   ip.String(),
		PublicPort: port,
		RTTMS:      time.Since(start).Milliseconds(),
	}, nil
}

// Classify probes the supplied STUN servers (defaults when nil) and returns
// the node's NAT classification and connectivity guidance.
func Classify(servers []string) Report {
	if len(servers) == 0 {
		servers = DefaultSTUN
	}
	rep := Report{NATType: ProbeFailed, Connectivity: Unknown}

	// Force IPv4: the STUN vocabulary here is IPv4 (RFC 5389 mapped-address),
	// and dual-stack listeners report an unspecified "::" local address.
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		rep.Hints = append(rep.Hints, "cannot open a local UDP socket: "+err.Error())
		return rep
	}
	defer conn.Close()
	if local, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		rep.LocalIP = local.IP.String()
	}

	var obs []Observation
	for _, srv := range servers {
		o, err := queryOne(conn, srv, Timeout)
		if err != nil {
			continue
		}
		obs = append(obs, o)
		if len(obs) >= 2 {
			break // two successful, independent observations suffice
		}
	}
	rep.Observations = obs
	if len(obs) == 0 {
		rep.NATType = UDPBlocked
		rep.Connectivity = RelayRequired
		rep.Hints = append(rep.Hints, "outbound UDP to all STUN servers failed; NAT type cannot be observed and inbound hole punching will not work")
		return rep
	}

	first := obs[0]
	rep.PublicIP, rep.PublicPort = first.PublicIP, first.PublicPort
	rep.CGNAT = isCGNAT(rep.LocalIP)

	sameMapping := true
	for _, o := range obs[1:] {
		if o.PublicIP != first.PublicIP || o.PublicPort != first.PublicPort {
			sameMapping = false
			break
		}
	}

	localIsPublic := net.ParseIP(rep.LocalIP) != nil && isGlobalUnicast(rep.LocalIP)
	switch {
	case localIsPublic && rep.LocalIP == first.PublicIP:
		rep.NATType = Public
		rep.Connectivity = Direct
		rep.Hints = append(rep.Hints, "host has a public interface address; inbound TCP works directly once the port is open")
	case !sameMapping:
		rep.NATType = Symmetric
		rep.Connectivity = RelayRequired
		rep.Hints = append(rep.Hints, "reflexive mapping changes per destination (symmetric NAT); enable router port forwarding/UPnP, or run this host as a client instead of a node")
		if rep.CGNAT {
			rep.Hints = append(rep.Hints, "local address is inside RFC6598 100.64.0.0/10: carrier-grade NAT confirmed, port forwarding is usually impossible without ISP cooperation")
		}
	default:
		// One stable mapping across independent servers: an endpoint-
		// independent (cone) mapping. Restricted variants cannot be told
		// apart without a second UDP test server, so report cone honestly.
		rep.NATType = Cone
		rep.Connectivity = HolePunch
		rep.Hints = append(rep.Hints, "stable endpoint-independent mapping; simultaneous-open hole punching or a static port forward should succeed")
		if isPrivate(rep.LocalIP) {
			rep.Hints = append(rep.Hints, "host is behind a home/router NAT (RFC1918 local address); forward the listening port for always-on reachability")
		}
	}
	return rep
}

// isCGNAT reports whether ip is inside the RFC6598 shared carrier-grade range.
func isCGNAT(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() == nil {
		return false
	}
	v4 := ip.To4()
	return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

func isPrivate(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	return ip != nil && (ip.IsPrivate() || isCGNAT(ipStr))
}

// isGlobalUnicast is an explicit check that avoids treating the RFC6598 range
// as globally reachable (Go's IsGlobalUnicast historically marks it global).
func isGlobalUnicast(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast() || isCGNAT(ipStr) {
		return false
	}
	return true
}
