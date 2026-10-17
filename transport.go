package oceanbase

import (
	"context"
	"net"
	"net/url"
	"strings"
)

const (
	networkTCP      = "tcp"
	networkUnix     = "unix"
	networkPipe     = "pipe"
	configAddrField = "Addr"
)

func parseTransportDSN(cfg *Config, values url.Values) error {
	for _, name := range []string{"network", "socket"} {
		if list, ok := values[name]; ok && (len(list) != 1 || list[0] == "") {
			return &ConfigError{Field: "DSN transport"}
		}
	}
	network, socket := values.Get("network"), values.Get("socket")
	if network == "" || network == networkTCP {
		if socket != "" {
			return &ConfigError{Field: "socket with TCP"}
		}
	} else if socket == "" {
		return &ConfigError{Field: "socket"}
	}
	cfg.Network = network
	if socket != "" {
		cfg.Addr = socket
	}
	delete(values, "network")
	delete(values, "socket")
	return nil
}

func (cfg *Config) normalizeTransport() error {
	if cfg.Network == "" {
		cfg.Network = networkTCP
	}
	if cfg.Network == networkTCP {
		addr, err := normalizeAddress(cfg.Addr)
		cfg.Addr = addr
		return err
	}
	if cfg.Network != networkUnix && cfg.Network != networkPipe {
		return &ConfigError{Field: "Network"}
	}
	if cfg.Addr == "" || strings.ContainsRune(cfg.Addr, 0) {
		return &ConfigError{Field: configAddrField}
	}
	if cfg.Network == networkPipe && !validPipeAddress(cfg.Addr) {
		return &ConfigError{Field: configAddrField}
	}
	if len(cfg.Routing.Endpoints) != 0 || cfg.Discovery.Source != nil {
		return &ConfigError{Field: "Routing/Discovery with IPC"}
	}
	if cfg.TLSConfig != nil && cfg.TLSConfig.ServerName == "" {
		return &ConfigError{Field: "TLSConfig.ServerName"}
	}
	return nil
}

func dialTransport(ctx context.Context, network, addr string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if network == networkPipe {
		return dialNamedPipe(ctx, addr)
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func validPipeAddress(addr string) bool {
	if !strings.HasPrefix(addr, `\\`) {
		return false
	}
	parts := strings.SplitN(addr[2:], `\`, 3)
	if len(parts) != 3 || parts[0] == "" || !strings.EqualFold(parts[1], networkPipe) || parts[2] == "" || strings.ContainsAny(parts[0], "/\x00?:*<>|\"") {
		return false
	}
	for _, component := range strings.FieldsFunc(parts[2], func(r rune) bool { return r == '\\' || r == '/' }) {
		if component == "." || component == ".." {
			return false
		}
	}
	return !strings.ContainsRune(parts[2], 0)
}
