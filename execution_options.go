package oceanbase

import (
	"strconv"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ExecutionOptions enables optional optimizations. Zero values keep uncached separate execution.
type ExecutionOptions struct {
	PrepareExecute         bool
	StatementCacheSize     int
	StatementCacheSQLLimit int // SQL bytes; zero uses 1024 when the cache is enabled.
	ReuseParameterTypes    bool
	StatementChecksum      bool // SQL checksum, independent of transport CRC.
}

func (o *ExecutionOptions) normalize() error {
	if o.StatementCacheSize < 0 || o.StatementCacheSQLLimit < 0 {
		return &ConfigError{Field: "Execution"}
	}
	if o.StatementCacheSQLLimit == 0 {
		o.StatementCacheSQLLimit = 1024
	}
	return nil
}

func (o *ExecutionOptions) applyDSNOption(name, value string) error {
	var err error
	switch name {
	case "statementCacheSize":
		o.StatementCacheSize, err = strconv.Atoi(value)
	case "statementCacheSQLLimit":
		o.StatementCacheSQLLimit, err = strconv.Atoi(value)
	default:
		if value != dsnTrue && value != dsnFalse {
			return &ConfigError{Field: name}
		}
		v := value == dsnTrue
		switch name {
		case "prepareExecute":
			o.PrepareExecute = v
		case "reuseParameterTypes":
			o.ReuseParameterTypes = v
		case "statementChecksum":
			o.StatementChecksum = v
		}
	}
	if err != nil {
		return &ConfigError{Field: name}
	}
	return nil
}

func (c *conn) usePrepareExecute() bool {
	return c.cfg.Execution.PrepareExecute && c.session.mode == ModeOracle && supportsPrepareExecute(c.session.obVersion)
}

func (c *conn) executeOptions(query []byte, combined bool) wire.ExecuteOptions {
	o := wire.ExecuteOptions{}
	if c.cfg.Execution.StatementChecksum && c.session.mode == ModeOracle && (combined || supportsVersion(c.session.obVersion, 4, 1, 2)) {
		o.UseChecksum = true
		o.SQLChecksum = wire.SQLChecksum(query)
	}
	return o
}
