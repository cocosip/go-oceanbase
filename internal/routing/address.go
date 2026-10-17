// Package routing selects bounded connection candidates without owning sockets or sessions.
package routing

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// ErrInvalid identifies invalid routing input without retaining its value.
var ErrInvalid = errors.New("routing: invalid configuration")

// Address is normalized by the caller; lower-numbered groups are attempted first.
type Address struct {
	Addr          string
	Group, Weight uint16
}

// NormalizeAddress accepts DNS/IPv4/IPv6 with an optional port, defaulting to 2881.
func NormalizeAddress(addr string) (string, error) {
	if addr == "" {
		return "127.0.0.1:2881", nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
		if strings.Contains(host, ":") {
			if _, e := netip.ParseAddr(host); e != nil {
				return "", ErrInvalid
			}
		}
		port = "2881"
	}
	if host == "" || strings.ContainsAny(host, "\x00 \t\r\n/\\?#@[],") {
		return "", ErrInvalid
	}
	if strings.Contains(host, ":") {
		ip, e := netip.ParseAddr(host)
		if e != nil {
			return "", ErrInvalid
		}
		host = ip.String()
	} else {
		host = strings.ToLower(host)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", ErrInvalid
	}
	return net.JoinHostPort(host, strconv.FormatUint(n, 10)), nil
}
