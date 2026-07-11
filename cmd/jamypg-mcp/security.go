package main

import (
	"fmt"
	"net"
	"strings"
)

// validateHTTPExposure prevents standalone mode from becoming remotely
// reachable merely because -addr was changed. The master admin token is not a
// substitute for this opt-in: in standalone mode it protects mutations and DB
// execution, but intentionally leaves read-only catalog MCP tools anonymous.
func validateHTTPExposure(transport, addr, metaDSN, adminToken string, publicMCP bool) error {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "stdio":
		return nil
	case "http", "streamable-http":
		// Continue below.
	default:
		return fmt.Errorf("unsupported transport %q: use http or stdio", transport)
	}

	loopback, err := isLoopbackListenAddress(addr)
	if err != nil {
		return err
	}
	if loopback || strings.TrimSpace(metaDSN) != "" {
		return nil
	}
	if publicMCP && strings.TrimSpace(adminToken) != "" {
		return nil
	}
	if publicMCP {
		return fmt.Errorf(
			"refusing public standalone HTTP on %q without an admin token: configure -admin-token/JAMYPG_ADMIN_TOKEN, or use -meta-db for full authentication",
			addr,
		)
	}
	return fmt.Errorf(
		"refusing standalone HTTP on non-loopback address %q: use a loopback -addr, configure -meta-db authentication, or explicitly acknowledge public MCP exposure with -public-mcp plus -admin-token",
		addr,
	)
}

// isLoopbackListenAddress deliberately accepts only localhost and literal
// loopback IPs. Empty/wildcard hosts, interface addresses, and arbitrary
// hostnames are remotely reachable (or resolution-dependent) and therefore
// require the explicit public-MCP opt-in in standalone mode.
func isLoopbackListenAddress(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false, fmt.Errorf("invalid HTTP listen address %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), nil
}
