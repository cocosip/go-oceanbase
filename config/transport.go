package config

import (
	"net/url"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/configspec"
)

const (
	networkTCP          = "tcp"
	networkUnix         = "unix"
	networkPipe         = "pipe"
	networkSharedMemory = "sharedmemory"
	configAddrField     = "Addr"
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
	if cfg.Network != networkUnix && cfg.Network != networkPipe && cfg.Network != networkSharedMemory {
		return &ConfigError{Field: "Network"}
	}
	if cfg.Addr == "" || strings.ContainsRune(cfg.Addr, 0) {
		return &ConfigError{Field: configAddrField}
	}
	if cfg.Network == networkPipe && !configspec.ValidPipeAddress(cfg.Addr) {
		return &ConfigError{Field: configAddrField}
	}
	if cfg.Network == networkSharedMemory && !configspec.ValidSharedMemoryName(cfg.Addr) {
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
