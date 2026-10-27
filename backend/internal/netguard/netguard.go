// Package netguard constrains the agent egress proxy so it cannot be turned
// into an SSRF/open-proxy into the coordinator's own network. Public internet
// destinations work normally; loopback, private, link-local (including cloud
// metadata endpoints), multicast and unspecified addresses are rejected.
package netguard

import (
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"
)

// ErrBlockedDestination is returned when a resolved peer address is not public.
var ErrBlockedDestination = errors.New("netguard: destination resolves to a non-public address (private, loopback, link-local or metadata range)")

// ErrBlockedPort is returned for ports the exit policy forbids regardless of
// host, mirroring the abuse-focused blocks Tor exits apply.
var ErrBlockedPort = errors.New("netguard: destination port blocked by exit policy")

// blockedPorts are abuse-prone services the proxy must not relay: email
// submission (spam), Windows file sharing/RPC, legacy remote shells and
// unauthenticated file/boot/amplification services.
var blockedPorts = map[uint16]bool{
	25: true, 465: true, 587: true, // SMTP / SMTPS / message submission
	135: true, 137: true, 138: true, 139: true, 445: true, // MSRPC, NetBIOS, SMB
	17: true, 19: true, // quote/chargen amplification
	69:  true,                       // TFTP
	111: true,                       // portmap / Sun RPC
	512: true, 513: true, 514: true, // rexec, rlogin, rsh
	2049: true, // NFS
}

// CheckPort enforces the exit policy. Port 0 (unknown) is rejected.
func CheckPort(port uint16) error {
	if port == 0 || blockedPorts[port] {
		return ErrBlockedPort
	}
	return nil
}

// IsPublic reports whether ip is a routable public unicast address. Every
// non-public range is blocked, which also covers the cloud metadata endpoints
// 169.254.169.254 (link-local) and fd00:ec2::254 / fe80::a9fe:a9fe.
func IsPublic(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	return true
}

// control is a net.Dialer Control hook: it runs against the exact IP the
// kernel is about to dial, closing the DNS-rebinding window between an earlier
// lookup and the actual connection.
func control(_, address string, _ syscall.RawConn) error {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if !IsPublic(ip) {
		return ErrBlockedDestination
	}
	if portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 0 || port > 65535 {
			return ErrBlockedPort
		}
		if err := CheckPort(uint16(port)); err != nil {
			return err
		}
	}
	return nil
}

// CheckHostPort validates both the public-address rule and the exit-policy
// port list. A missing port is accepted (the caller's default applies); an
// explicit blocked port is rejected.
func CheckHostPort(hostOrHostport string) error {
	host := hostOrHostport
	var port uint16
	if h, p, err := net.SplitHostPort(hostOrHostport); err == nil {
		host = h
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 65535 {
			return ErrBlockedPort
		}
		port = uint16(n)
	}
	if err := CheckHost(host); err != nil {
		return err
	}
	if port != 0 {
		return CheckPort(port)
	}
	return nil
}

// SafeDialer returns a dialer that refuses non-public destinations and bounds
// every TCP attempt. The Control hook validates the resolved address at dial
// time, so a hostname that re-resolves to an internal IP is still blocked.
func SafeDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   control,
	}
}

// CheckHost resolves hostOrHostport and confirms every returned address is
// public. It is used for an early, user-friendly rejection before dialing.
func CheckHost(hostOrHostport string) error {
	host := hostOrHostport
	if h, _, err := net.SplitHostPort(hostOrHostport); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		if !IsPublic(ip) {
			return ErrBlockedDestination
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return &net.DNSError{Err: "lookup failed", Name: host, IsNotFound: true}
	}
	for _, ip := range ips {
		if !IsPublic(ip) {
			return ErrBlockedDestination
		}
	}
	return nil
}

// SplitHostPort is a small helper for CONNECT targets without a port.
func SplitHostPort(hostport, defaultPort string) (string, string, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// Bare host: assume the default port and re-parse.
		return net.SplitHostPort(net.JoinHostPort(hostport, defaultPort))
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", err
	}
	return host, port, nil
}
