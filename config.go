package oceanbase

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"log/slog"
	"maps"
	"math/big"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Mode is an OceanBase compatibility mode. Auto is pinned after initialization.
type Mode uint8

// Compatibility modes. Auto requires detection from the authenticated session.
const (
	ModeAuto Mode = iota
	ModeMySQL
	ModeOracle
)

// RowsAffectedMode selects matched or changed UPDATE counts.
type RowsAffectedMode uint8

// UPDATE count policies; Oracle compatibility mode requires MatchedRows.
const (
	MatchedRows RowsAffectedMode = iota
	ChangedRows
)

// ResetMode selects the session reset policy used by database/sql.
type ResetMode uint8

// Session reset policies.
const (
	ResetManaged ResetMode = iota
	ResetFull
)

// PrepareMode selects the parameterized execution strategy.
type PrepareMode uint8

// Preparation strategies. Client interpolation is always explicitly selected.
const (
	PrepareServer PrepareMode = iota
	PrepareClient
)

// Limits bounds protocol messages, materialization and cleanup. Zero uses defaults.
type Limits struct {
	MaxMessageBytes int64
	MaxLOBBytes     int64
	FetchSize       int
	MaxResultItems  int
	CleanupTimeout  time.Duration
}

// SessionOptions describes the initial session baseline.
type SessionOptions struct {
	// ParseTime converts unzoned dates and timestamps using Location; invalid or ambiguous times fail.
	ParseTime bool
	Charset   string
	NCharset  string
	Location  *time.Location
	Variables map[string]string
	InitSQL   []string
}

// Config is copied by NewConnector. A nil Logger produces no driver logs.
type Config struct {
	Addr                 string
	User                 string
	Password             string
	Database             string
	Mode                 Mode
	Logger               *slog.Logger
	Diagnostics          DiagnosticOptions
	TLSConfig            *tls.Config
	ConnectTimeout       time.Duration
	DialContext          func(context.Context, string, string) (net.Conn, error)
	Credentials          func(context.Context) (Credentials, error)
	Auth                 AuthOptions
	Session              SessionOptions
	Protocol             ProtocolOptions
	Execution            ExecutionOptions
	Limits               Limits
	RowsAffectedMode     RowsAffectedMode
	ResetMode            ResetMode
	PrepareMode          PrepareMode
	AllowMultiStatements bool
}

// ConfigError identifies an invalid field without retaining its sensitive value.
type ConfigError struct{ Field string }

func (e *ConfigError) Error() string { return "oceanbase: invalid configuration: " + e.Field }

func normalizeConfig(cfg Config) (Config, error) {
	if (cfg.User == "" && cfg.Credentials == nil) || strings.ContainsRune(cfg.User, 0) {
		return Config{}, &ConfigError{Field: "User"}
	}
	if strings.ContainsRune(cfg.Database, 0) {
		return Config{}, &ConfigError{Field: "Database"}
	}
	if cfg.Mode > ModeOracle {
		return Config{}, &ConfigError{Field: "Mode"}
	}
	if cfg.RowsAffectedMode > ChangedRows || (cfg.Mode == ModeOracle && cfg.RowsAffectedMode == ChangedRows) {
		return Config{}, &ConfigError{Field: "RowsAffectedMode"}
	}
	if cfg.ResetMode > ResetFull {
		return Config{}, &ConfigError{Field: "ResetMode"}
	}
	if cfg.PrepareMode > PrepareClient {
		return Config{}, &ConfigError{Field: "PrepareMode"}
	}
	if err := cfg.validateDriverOptions(); err != nil {
		return Config{}, err
	}
	if cfg.ConnectTimeout < 0 {
		return Config{}, &ConfigError{Field: "ConnectTimeout"}
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	var err error
	cfg.Addr, err = normalizeAddress(cfg.Addr)
	if err != nil {
		return Config{}, err
	}
	if cfg.Limits.MaxMessageBytes < 0 || cfg.Limits.MaxLOBBytes < 0 || cfg.Limits.FetchSize < 0 || cfg.Limits.MaxResultItems < 0 || cfg.Limits.CleanupTimeout < 0 {
		return Config{}, &ConfigError{Field: "Limits"}
	}
	if cfg.Limits.MaxMessageBytes == 0 {
		cfg.Limits.MaxMessageBytes = 64 << 20
	}
	if cfg.Limits.MaxLOBBytes == 0 {
		cfg.Limits.MaxLOBBytes = 16 << 20
	}
	if cfg.Limits.FetchSize == 0 {
		cfg.Limits.FetchSize = 128
	}
	if cfg.Limits.MaxResultItems == 0 {
		cfg.Limits.MaxResultItems = 1024
	}
	if cfg.Limits.CleanupTimeout == 0 {
		cfg.Limits.CleanupTimeout = time.Second
	}
	cfg.Session, err = cloneSessionOptions(cfg.Session)
	if err != nil {
		return Config{}, err
	}
	cfg.Auth, err = cloneAuthOptions(cfg.Auth)
	if err != nil {
		return Config{}, err
	}
	if cfg.TLSConfig != nil {
		if cfg.TLSConfig.InsecureSkipVerify {
			return Config{}, &ConfigError{Field: "TLSConfig.InsecureSkipVerify"}
		}
		cfg.TLSConfig = cloneTLSConfig(cfg.TLSConfig)
	}
	return cfg, nil
}

func (cfg *Config) validateDriverOptions() error {
	if err := cfg.Diagnostics.validate(); err != nil {
		return err
	}
	if err := validateProtocolOptions(cfg.Protocol); err != nil {
		return err
	}
	return cfg.Execution.normalize()
}

func cloneAuthOptions(options AuthOptions) (AuthOptions, error) {
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

func normalizeAddress(addr string) (string, error) {
	invalid := &ConfigError{Field: "Addr"}
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
			if _, parseErr := netip.ParseAddr(host); parseErr != nil {
				return "", invalid
			}
		}
		port = "2881"
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n/\\?#@[],") {
		return "", invalid
	}
	if strings.Contains(host, ":") {
		if _, err = netip.ParseAddr(host); err != nil {
			return "", invalid
		}
	}
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNum == 0 {
		return "", invalid
	}
	return net.JoinHostPort(host, strconv.FormatUint(portNum, 10)), nil
}

//nolint:staticcheck // Snapshot the caller legacy certificate map without changing TLS selection.
func cloneTLSConfig(source *tls.Config) *tls.Config {
	clone := source.Clone()
	if source.RootCAs != nil {
		clone.RootCAs = source.RootCAs.Clone()
	}
	if source.ClientCAs != nil {
		clone.ClientCAs = source.ClientCAs.Clone()
	}
	clone.NextProtos = slices.Clone(source.NextProtos)
	clone.CipherSuites = slices.Clone(source.CipherSuites)
	clone.CurvePreferences = slices.Clone(source.CurvePreferences)
	clone.EncryptedClientHelloConfigList = slices.Clone(source.EncryptedClientHelloConfigList)
	clone.Certificates = make([]tls.Certificate, len(source.Certificates))
	for i, cert := range source.Certificates {
		clone.Certificates[i] = cloneCertificate(cert)
	}
	if source.NameToCertificate != nil {
		clone.NameToCertificate = make(map[string]*tls.Certificate, len(source.NameToCertificate))
		for name, cert := range source.NameToCertificate {
			if cert == nil {
				clone.NameToCertificate[name] = nil
				continue
			}
			copyCert := cloneCertificate(*cert)
			clone.NameToCertificate[name] = &copyCert
		}
	}
	return clone
}

func cloneCertificate(cert tls.Certificate) tls.Certificate {
	cert.Certificate = cloneBytesList(cert.Certificate)
	cert.OCSPStaple = slices.Clone(cert.OCSPStaple)
	cert.SignedCertificateTimestamps = cloneBytesList(cert.SignedCertificateTimestamps)
	cert.SupportedSignatureAlgorithms = slices.Clone(cert.SupportedSignatureAlgorithms)
	return cert
}

func cloneBytesList(source [][]byte) [][]byte {
	if source == nil {
		return nil
	}
	clone := make([][]byte, len(source))
	for i, b := range source {
		clone[i] = slices.Clone(b)
	}
	return clone
}
