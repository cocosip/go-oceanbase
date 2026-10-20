package config

import (
	"crypto/rsa"
	"maps"
	"math/big"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/configspec"
)

func cloneConfigOptions(cfg Config) (Config, error) {
	var err error
	cfg.Session, err = cloneSessionOptions(cfg.Session)
	if err != nil {
		return Config{}, err
	}
	if cfg.Mode == ModeOracle && cfg.Session.Collation != "" {
		return Config{}, &ConfigError{Field: "Session.Collation"}
	}
	if cfg.CreateDatabaseIfMissing {
		if cfg.Database == "" || cfg.Mode == ModeOracle {
			return Config{}, &ConfigError{Field: "CreateDatabaseIfMissing"}
		}
		if _, err = charset.Encode(cfg.Session.Charset, cfg.Database); err != nil {
			return Config{}, &ConfigError{Field: configDatabaseField}
		}
	}
	if _, err = connectionAttributes(cfg, cfg.ProxyUser); err != nil {
		return Config{}, err
	}
	cfg.ConnectionAttributes = maps.Clone(cfg.ConnectionAttributes)
	cfg.Auth, err = cloneAuthOptions(cfg.Auth)
	if err != nil {
		return Config{}, err
	}
	if cfg.TLSConfig != nil {
		if cfg.TLSConfig.InsecureSkipVerify {
			return Config{}, &ConfigError{Field: "TLSConfig.InsecureSkipVerify"}
		}
		cfg.TLSConfig = configspec.CloneTLSConfig(cfg.TLSConfig)
	}
	return cfg, nil
}

func cloneAuthOptions(options AuthOptions) (AuthOptions, error) {
	if options.ServicePrincipalName != "" && !auth.ValidGSSPrincipal(options.ServicePrincipalName) {
		return AuthOptions{}, &ConfigError{Field: "Auth.ServicePrincipalName"}
	}
	if options.PasswordCharset == "" {
		options.PasswordCharset = "utf8mb4"
	}
	options.PasswordCharset = strings.ToLower(options.PasswordCharset)
	if _, err := charset.Encode(options.PasswordCharset, ""); err != nil {
		return AuthOptions{}, &ConfigError{Field: "Auth.PasswordCharset"}
	}
	options.Plugins = maps.Clone(options.Plugins)
	for name, factory := range options.Plugins {
		if name == "" || len(name) > 128 || strings.ContainsRune(name, 0) || factory == nil {
			return AuthOptions{}, &ConfigError{Field: "Auth.Plugins"}
		}
	}
	if key := options.RSAKey; key != nil {
		if key.N == nil || key.N.Sign() <= 0 || key.N.BitLen() < 2048 || key.E < 3 || key.E%2 == 0 {
			return AuthOptions{}, &ConfigError{Field: "Auth.RSAKey"}
		}
		options.RSAKey = &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
	}
	return options, nil
}
