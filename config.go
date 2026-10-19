package oceanbase

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"log/slog"
	"maps"
	"math/big"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/routing"
)

const configDatabaseField = "Database"

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
	// SkipVersionProbe omits ob_version() during initialization; default false.
	// Version-dependent features stay disabled or unsupported when the version is unknown.
	SkipVersionProbe bool
	// ParseTime converts unzoned dates and timestamps using Location; invalid or ambiguous times fail.
	ParseTime bool
	// Charset encodes SQL and handshake user/database names; default utf8mb4.
	Charset string
	// Collation optionally selects a MySQL-mode collation for Charset; empty uses its default.
	Collation string
	NCharset  string
	Location  *time.Location
	// Variables uses integer literals for decimal int64/uint64 input when the
	// MySQL variable's current result metadata is numeric. Other values are quoted.
	Variables map[string]string
	InitSQL   []string
}

// Config is copied by NewConnector. A nil Logger produces no driver logs.
type Config struct {
	// Network defaults to tcp; unix, pipe and sharedmemory use Addr as the IPC endpoint.
	// Named pipe addresses must be valid UTF-8 to preserve their Windows UTF-16 identity.
	// Shared memory allocation waits for the server's uncorrelated answer even after cancellation;
	// an unresponsive server can exceed the connection timeout in that phase.
	// DialContext receives the selected network and address without TLS exemptions.
	// It can use net.Dialer and net.TCPConn to configure TCP before returning the raw transport.
	Network  string
	Addr     string
	User     string
	Password string
	// ProxyUser is an Oracle proxy-login target, separate from the authenticating User.
	// Empty disables proxy login. Credentials callbacks replace all three identity fields.
	ProxyUser   string
	Database    string
	Mode        Mode
	Logger      *slog.Logger
	Diagnostics DiagnosticOptions
	TLSConfig   *tls.Config
	// ConnectTimeout bounds connection setup; zero uses 10 seconds, negative values fail.
	ConnectTimeout time.Duration
	// DialContext returns a raw transport. Nil and typed-nil connections are rejected.
	// A non-nil connection returned with an error is closed by the driver.
	DialContext          func(context.Context, string, string) (net.Conn, error)
	Credentials          func(context.Context) (Credentials, error)
	Auth                 AuthOptions
	Session              SessionOptions
	Protocol             ProtocolOptions
	Execution            ExecutionOptions
	Routing              RoutingOptions
	Discovery            DiscoveryOptions
	Limits               Limits
	RowsAffectedMode     RowsAffectedMode
	ResetMode            ResetMode
	PrepareMode          PrepareMode
	AllowMultiStatements bool
	// Interactive requests the server's interactive session timeout; default false.
	Interactive bool
	// ConnectionAttributes supplies application metadata, encoded using Session.Charset.
	// Keys starting with "_" and "ob_capability_flag" are reserved. Nil adds no
	// application attributes; protocol and proxy identity attributes are generated separately.
	ConnectionAttributes map[string]string
	// CreateDatabaseIfMissing creates and selects Database on new MySQL-mode connections.
	// It is opt-in, requires a nonempty Database, and never applies to ChangeUser or CancelQuery.
	CreateDatabaseIfMissing bool
	// ReadTimeout bounds each underlying network read; zero disables it.
	// Earlier context deadlines still apply. A timeout makes the connection unusable.
	ReadTimeout time.Duration
}

// ConfigError identifies an invalid field without retaining its sensitive value.
type ConfigError struct{ Field string }

func (e *ConfigError) Error() string { return "oceanbase: invalid configuration: " + e.Field }

func normalizeConfig(cfg Config) (Config, error) {
	if (cfg.User == "" && cfg.Credentials == nil) || strings.ContainsRune(cfg.User, 0) {
		return Config{}, &ConfigError{Field: "User"}
	}
	if strings.ContainsRune(cfg.Database, 0) {
		return Config{}, &ConfigError{Field: configDatabaseField}
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
	if cfg.ReadTimeout < 0 {
		return Config{}, &ConfigError{Field: "ReadTimeout"}
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	var err error
	if err = cfg.normalizeTransport(); err != nil {
		return Config{}, err
	}
	if err = cfg.normalizeRouting(); err != nil {
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
	return cloneConfigOptions(cfg)
}

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
	if _, err = (&connector{cfg: cfg}).connectionAttributes(cfg.ProxyUser); err != nil {
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
		cfg.TLSConfig = cloneTLSConfig(cfg.TLSConfig)
	}
	return cfg, nil
}

func (cfg *Config) validateDriverOptions() error {
	if err := cfg.Discovery.normalize(); err != nil {
		return err
	}
	if err := cfg.Diagnostics.validate(); err != nil {
		return err
	}
	if err := validateProtocolOptions(cfg.Protocol); err != nil {
		return err
	}
	return cfg.Execution.normalize()
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

func normalizeAddress(addr string) (string, error) {
	value, err := routing.NormalizeAddress(addr)
	if err != nil {
		return "", &ConfigError{Field: configAddrField}
	}
	return value, nil
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
