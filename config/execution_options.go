package config

import (
	"strconv"
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
