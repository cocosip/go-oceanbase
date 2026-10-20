package oceanbase

import (
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	http "net/http"
)

// Mode is an OceanBase compatibility mode. Auto is pinned after initialization.
type Mode = config.Mode

// Compatibility modes. Auto requires detection from the authenticated session.
const (
	ModeAuto   = config.ModeAuto
	ModeMySQL  = config.ModeMySQL
	ModeOracle = config.ModeOracle
)

// RowsAffectedMode selects matched or changed UPDATE counts.
type RowsAffectedMode = config.RowsAffectedMode

// UPDATE count policies; Oracle compatibility mode requires MatchedRows.
const (
	MatchedRows = config.MatchedRows
	ChangedRows = config.ChangedRows
)

// ResetMode selects the session reset policy used by database/sql.
type ResetMode = config.ResetMode

// Session reset policies.
const (
	ResetManaged = config.ResetManaged
	ResetFull    = config.ResetFull
)

// PrepareMode selects the parameterized execution strategy.
type PrepareMode = config.PrepareMode

// Preparation strategies. Client interpolation is always explicitly selected.
const (
	PrepareServer = config.PrepareServer
	PrepareClient = config.PrepareClient
)

// Limits bounds protocol messages, materialization and cleanup. Zero uses defaults.
type Limits = config.Limits

// SessionOptions describes the initial session baseline.
type SessionOptions = config.SessionOptions

// Config is copied by NewConnector. A nil Logger produces no driver logs.
type Config = config.Config

// ConfigError identifies an invalid field without retaining its sensitive value.
type ConfigError = config.ConfigError

// Authentication and mode errors are safe to inspect with errors.Is.
var (
	ErrTLSRequired  = dberrors.ErrTLSRequired
	ErrModeMismatch = dberrors.ErrModeMismatch
)

// Credentials are obtained for each new authentication, never during a transaction.
// ProxyUser selects an Oracle proxy-login target; empty authenticates as User alone.
type Credentials = config.Credentials

// AuthSecurity describes the transport needed before a plugin sees credentials.
type AuthSecurity = config.AuthSecurity

// Authentication transport policies.
const (
	AuthChallengeResponse = config.AuthChallengeResponse
	AuthRequireTLS        = config.AuthRequireTLS
)

// AuthChallenge contains copies of server data and driver-verified TLS state.
type AuthChallenge = config.AuthChallenge

// AuthPlugin produces authentication messages without access to the connection.
// Security must declare any stage that exposes credentials as AuthRequireTLS.
// If it implements io.Closer, the driver closes it on switch or completion,
// including failure. Close must release local state promptly without network I/O.
type AuthPlugin = config.AuthPlugin

// AuthConversation optionally exchanges raw tokens until Complete reports true.
// The driver preserves complete server packets, including their first byte,
// and still applies Security before every Respond. A nil continuation response
// sends no packet; a non-nil empty response sends an empty packet. Completion
// still requires a final server OK. Each use must have independent state.
type AuthConversation = config.AuthConversation

// AuthFactory creates independent mutable state for one authentication plugin use.
type AuthFactory = config.AuthFactory

// AuthOptions supplies pinned keys and per-connector authentication extensions.
// RSAKey is used only for SHA password encryption; untrusted key retrieval is disabled.
type AuthOptions = config.AuthOptions

// PAMChallenge describes one UTF-8 dialog prompt. Round starts at one for each
// plugin use. Last is a server hint; authentication still requires a final OK.
type PAMChallenge = config.PAMChallenge

// DiagnosticOptions bounds synchronous diagnostic delivery after releasing the driver gate.
// SQL and packets are represented by fingerprints and metadata, never by raw payloads.
type DiagnosticOptions = config.DiagnosticOptions

// RequestDiagnostic describes one physical protocol command, including consumer time for Rows.
type RequestDiagnostic = config.RequestDiagnostic

// PacketDiagnostic describes a logical message without exposing its payload.
type PacketDiagnostic = config.PacketDiagnostic

// ErrConnectorClosed means Connect was called after Connector.Close or sql.DB.Close.
var ErrConnectorClosed = dberrors.ErrConnectorClosed

// Topology is an owned discovery snapshot for one logical service.
// Explicit endpoint roles are required when Routing.Role is constrained.
type Topology = config.Topology

// TopologySource returns an owned snapshot and must honor ctx cancellation.
// Calls are serialized per Connector; sources shared across Connectors must be concurrency safe.
type TopologySource = config.TopologySource

// DiscoveryOptions refreshes candidates for new connections, never active sessions.
// Service is required; zero durations use 30s refresh, 5s request and 5m maximum age.
type DiscoveryOptions = config.DiscoveryOptions

// ParseAddressDescription extracts an inline TNS DESCRIPTION without file or network I/O.
func ParseAddressDescription(description string) (Topology, error) {
	return config.ParseAddressDescription(description)
}

// NewTNSFileSource reads the explicitly supplied regular file and alias on each lookup.
func NewTNSFileSource(path, alias string) (TopologySource, error) {
	return config.NewTNSFileSource(path, alias)
}

// NewOCPSource reads the Connector/J loadBalanceInfo JSON response.
func NewOCPSource(endpoint string, client *http.Client) (TopologySource, error) {
	return config.NewOCPSource(endpoint, client)
}

// ParseDSN parses an oceanbase URL and applies the same defaults as NewConnector.
func ParseDSN(dsn string) (Config, error) { return config.ParseDSN(dsn) }

// ExecutionOptions enables optional optimizations. Zero values keep uncached separate execution.
type ExecutionOptions = config.ExecutionOptions

// FeatureMode selects a negotiated capability. Auto uses it only when confirmed.
type FeatureMode = config.FeatureMode

// Feature policies never change the protocol after business commands have started.
const (
	FeatureDefault  = config.FeatureDefault
	FeatureOff      = config.FeatureOff
	FeatureRequired = config.FeatureRequired
	FeatureAuto     = config.FeatureAuto
)

// ProtocolOptions controls framing and result metadata at connection establishment.
// OB20 takes precedence over optional compression; requiring both is invalid.
type ProtocolOptions = config.ProtocolOptions

// EndpointRole labels the logical service exposed by an address, not SQL permissions.
type EndpointRole = config.EndpointRole

// Explicit roles never fall back to unlabelled or opposite-role endpoints.
const (
	RoleAny       = config.RoleAny
	RoleReadWrite = config.RoleReadWrite
	RoleReadOnly  = config.RoleReadOnly
)

// BalanceStrategy orders members within ascending priority groups.
type BalanceStrategy = config.BalanceStrategy

// Endpoint balancing strategies.
const (
	BalanceOrdered    = config.BalanceOrdered
	BalanceRandom     = config.BalanceRandom
	BalanceRoundRobin = config.BalanceRoundRobin
	BalanceAffinity   = config.BalanceAffinity
)

// Endpoint describes a caller-configured address and its logical service role.
type Endpoint = config.Endpoint

// RoutingOptions selects new connections and optionally validates idle pooled sessions.
// Endpoints replaces Addr; zero MaxAttempts makes one pass over eligible endpoints.
type RoutingOptions = config.RoutingOptions
