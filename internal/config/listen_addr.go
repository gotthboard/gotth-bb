package config

import (
	"fmt"
	"net/netip"
)

// ParseListenAddr validates an explicit numeric bind address and prevents a
// production service from bypassing the loopback-only Caddy boundary.
//
// Complexity: for n input bytes, time O(n), Omega(1) on an early parse failure,
// and tight Theta(n) for a valid address; auxiliary space O(n) in delegated
// parse-error construction, Omega(1), and tight Theta(1) for valid input.
// netip.ParseAddrPort performs the address scan and returns a fixed-width value.
func ParseListenAddr(raw string, environment Environment) (netip.AddrPort, error) {
	if environment != EnvironmentDevelopment &&
		environment != EnvironmentTest &&
		environment != EnvironmentProduction {
		return netip.AddrPort{}, fmt.Errorf("LISTEN_ADDR requires a valid APP_ENV")
	}

	address, err := netip.ParseAddrPort(raw)
	if err != nil || address.Port() == 0 || address.Addr().Zone() != "" {
		return netip.AddrPort{}, fmt.Errorf("LISTEN_ADDR must be a numeric IP and nonzero port")
	}
	if environment == EnvironmentProduction && !address.Addr().IsLoopback() {
		return netip.AddrPort{}, fmt.Errorf("LISTEN_ADDR must be loopback in production")
	}

	return address, nil
}
