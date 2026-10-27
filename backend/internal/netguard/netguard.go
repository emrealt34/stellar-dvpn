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
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if !IsPublic(ip) {
		return ErrBlockedDestination
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
