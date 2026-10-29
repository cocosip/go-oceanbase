package config

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	dsnTrue  = "true"
	dsnFalse = "false"
)

// ParseDSN parses an oceanbase URL and applies the same defaults as NewConnector.
func ParseDSN(dsn string) (Config, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "oceanbase" || u.Opaque != "" || u.User == nil || u.Fragment != "" || strings.Contains(dsn, "#") {
		return Config{}, &ConfigError{Field: "DSN"}
	}
	if strings.Contains(strings.TrimPrefix(u.EscapedPath(), "/"), "/") {
		return Config{}, &ConfigError{Field: configDatabaseField}
	}
	cfg := Config{Addr: u.Host, User: u.User.Username(), Database: strings.TrimPrefix(u.Path, "/")}
	cfg.Password, _ = u.User.Password()
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Config{}, &ConfigError{Field: "DSN options"}
	}
	values = normalizeDSNOptionNames(values)
	hasDescription := values.Has(addressDescriptionOption)
	if err := parseTransportDSN(&cfg, values); err != nil {
		return Config{}, err
	}
	if err := parseDiscoveryDSN(&cfg, values); err != nil {
		return Config{}, err
	}
	for name, list := range values {
		if len(list) != 1 || list[0] == "" {
			return Config{}, &ConfigError{Field: "DSN options"}
		}
		if err := applyDSNOption(&cfg, name, list[0]); err != nil {
			return Config{}, err
		}
	}
	for i := range cfg.Routing.Endpoints {
		if !hasDescription && cfg.Routing.Endpoints[i].Role == RoleAny {
			cfg.Routing.Endpoints[i].Role = cfg.Routing.Role
		}
	}
	return Normalize(cfg)
}

// normalizeDSNOptionNames makes the query-key boundary case-insensitive while
// leaving decoded values untouched. Repeated keys remain in one value list so
// the existing option-specific validation rejects them before selecting one.
func normalizeDSNOptionNames(values url.Values) url.Values {
	result := make(url.Values, len(values))
	for name, list := range values {
		key := strings.ToLower(name)
		if canonical, ok := dsnOptionCanonicalNames[key]; ok {
			key = canonical
		}
		result[key] = append(result[key], list...)
	}
	return result
}

var dsnOptionCanonicalNames = newDSNOptionCanonicalNames()

//nolint:goconst // canonical names intentionally mirror the existing parser switches.
func newDSNOptionCanonicalNames() map[string]string {
	return map[string]string{
		"interactive":              "interactive",
		"proxyuser":                "proxyUser",
		"createdatabaseifmissing":  "createDatabaseIfMissing",
		"charset":                  "charset",
		"collation":                "collation",
		"ncharset":                 "ncharset",
		"location":                 "location",
		"parsetime":                "parseTime",
		"skipversionprobe":         "skipVersionProbe",
		"compatiblemysqlversion":   "compatibleMysqlVersion",
		"allowoldpassword":         "allowOldPassword",
		"passwordcharset":          "passwordCharset",
		"serviceprincipalname":     "servicePrincipalName",
		"compression":              "compression",
		"ob20":                     "ob20",
		"checksum":                 "checksum",
		"newmetadata":              "newMetadata",
		"proxychecksum":            "proxyChecksum",
		"fulllinktrace":            "fullLinkTrace",
		"prepareexecute":           "prepareExecute",
		"statementcachesize":       "statementCacheSize",
		"statementcachesqllimit":   "statementCacheSQLLimit",
		"reuseparametertypes":      "reuseParameterTypes",
		"statementchecksum":        "statementChecksum",
		"addresses":                "addresses",
		"role":                     "role",
		"balance":                  "balance",
		"maxattempts":              "maxAttempts",
		"attempttimeout":           "attemptTimeout",
		"retrydelay":               "retryDelay",
		"blacklisttimeout":         "blacklistTimeout",
		"validationinterval":       "validationInterval",
		"mode":                     "mode",
		"connecttimeout":           "connectTimeout",
		"readtimeout":              "readTimeout",
		"cleanuptimeout":           "cleanupTimeout",
		"maxmessagebytes":          "maxMessageBytes",
		"maxlobbytes":              "maxLOBBytes",
		"fetchsize":                "fetchSize",
		"maxresultitems":           "maxResultItems",
		"rowsaffectedmode":         "rowsAffectedMode",
		"preparemode":              "prepareMode",
		"resetmode":                "resetMode",
		"allowmultistatements":     "allowMultiStatements",
		"network":                  "network",
		"socket":                   "socket",
		"tnsfile":                  "tnsFile",
		"tnsalias":                 "tnsAlias",
		"ocpurl":                   "ocpURL",
		"addressdescription":       addressDescriptionOption,
		"discoveryservice":         "discoveryService",
		"discoveryrefreshinterval": "discoveryRefreshInterval",
		"discoverytimeout":         "discoveryTimeout",
		"discoverymaxage":          "discoveryMaxAge",
	}
}

func applyDSNOption(cfg *Config, name, value string) error {
	switch name {
	case "interactive":
		if value != dsnTrue && value != dsnFalse {
			return &ConfigError{Field: name}
		}
		cfg.Interactive = value == dsnTrue
		return nil
	case "proxyUser":
		cfg.ProxyUser = value
		return nil
	case "createDatabaseIfMissing":
		if value != dsnTrue && value != dsnFalse {
			return &ConfigError{Field: name}
		}
		cfg.CreateDatabaseIfMissing = value == dsnTrue
		return nil
	case "charset", "collation", "ncharset", "location", "parseTime", "skipVersionProbe", "compatibleMysqlVersion":
		return cfg.Session.applyDSNOption(name, value)
	case "allowOldPassword", "passwordCharset", "servicePrincipalName":
		return cfg.Auth.applyDSNOption(name, value)
	case "compression", "ob20", "checksum", "newMetadata", "proxyChecksum", "fullLinkTrace":
		return cfg.Protocol.applyDSNOption(name, value)
	case "prepareExecute", "statementCacheSize", "statementCacheSQLLimit", "reuseParameterTypes", "statementChecksum":
		return cfg.Execution.applyDSNOption(name, value)
	case "addresses", "role", "balance", "maxAttempts", "attemptTimeout", "retryDelay", "blacklistTimeout", "validationInterval":
		return cfg.Routing.applyDSNOption(name, value)
	default:
		return applyConnectionDSNOption(cfg, name, value)
	}
}

func applyConnectionDSNOption(cfg *Config, name, value string) error {
	var err error
	switch name {
	case "mode":
		switch value {
		case "auto":
			cfg.Mode = ModeAuto
		case "mysql":
			cfg.Mode = ModeMySQL
		case "oracle":
			cfg.Mode = ModeOracle
		default:
			err = &ConfigError{Field: "mode"}
		}
	case "connectTimeout":
		cfg.ConnectTimeout, err = time.ParseDuration(value)
	case "readTimeout":
		cfg.ReadTimeout, err = time.ParseDuration(value)
	case "cleanupTimeout":
		cfg.Limits.CleanupTimeout, err = time.ParseDuration(value)
	case "maxMessageBytes":
		cfg.Limits.MaxMessageBytes, err = strconv.ParseInt(value, 10, 64)
	case "maxLOBBytes":
		cfg.Limits.MaxLOBBytes, err = strconv.ParseInt(value, 10, 64)
	case "fetchSize":
		cfg.Limits.FetchSize, err = strconv.Atoi(value)
	case "maxResultItems":
		cfg.Limits.MaxResultItems, err = strconv.Atoi(value)
	case "rowsAffectedMode":
		switch value {
		case "matched":
			cfg.RowsAffectedMode = MatchedRows
		case "changed":
			cfg.RowsAffectedMode = ChangedRows
		default:
			err = &ConfigError{Field: "rowsAffectedMode"}
		}
	case "prepareMode":
		switch value {
		case "server":
			cfg.PrepareMode = PrepareServer
		case "client":
			cfg.PrepareMode = PrepareClient
		default:
			err = &ConfigError{Field: "prepareMode"}
		}
	case "resetMode":
		switch value {
		case "managed":
			cfg.ResetMode = ResetManaged
		case "full":
			cfg.ResetMode = ResetFull
		default:
			err = &ConfigError{Field: "resetMode"}
		}
	case "allowMultiStatements":
		switch value {
		case dsnTrue:
			cfg.AllowMultiStatements = true
		case dsnFalse:
			cfg.AllowMultiStatements = false
		default:
			err = &ConfigError{Field: "allowMultiStatements"}
		}
	default:
		return &ConfigError{Field: "DSN option"}
	}
	if err != nil {
		return &ConfigError{Field: name}
	}
	return nil
}

func (o *SessionOptions) applyDSNOption(name, value string) error {
	var err error
	switch name {
	case "charset":
		o.Charset = value
	case "collation":
		o.Collation = value
	case "ncharset":
		o.NCharset = value
	case "location":
		o.Location, err = time.LoadLocation(value)
	case "parseTime":
		if value != dsnTrue && value != dsnFalse {
			return &ConfigError{Field: name}
		}
		o.ParseTime = value == dsnTrue
	case "skipVersionProbe":
		if value != dsnTrue && value != dsnFalse {
			return &ConfigError{Field: name}
		}
		o.SkipVersionProbe = value == dsnTrue
	case "compatibleMysqlVersion":
		o.CompatibleMysqlVersion, err = strconv.Atoi(value)
		if err == nil && o.CompatibleMysqlVersion != 5 && o.CompatibleMysqlVersion != 8 {
			return &ConfigError{Field: name}
		}
	default:
		return &ConfigError{Field: name}
	}
	if err != nil {
		return &ConfigError{Field: name}
	}
	return nil
}

func (o *AuthOptions) applyDSNOption(name, value string) error {
	switch name {
	case "servicePrincipalName":
		o.ServicePrincipalName = value
	case "allowOldPassword":
		if value != dsnTrue && value != dsnFalse {
			return &ConfigError{Field: name}
		}
		o.AllowOldPassword = value == dsnTrue
	case "passwordCharset":
		o.PasswordCharset = value
	default:
		return &ConfigError{Field: name}
	}
	return nil
}

func (p *ProtocolOptions) applyDSNOption(name, value string) error {
	policy, err := parseFeatureMode(value)
	if err != nil {
		return &ConfigError{Field: name}
	}
	switch name {
	case "compression":
		p.Compression = policy
	case "ob20":
		p.OB20 = policy
	case "checksum":
		p.Checksum = policy
	case "newMetadata":
		p.NewMetadata = policy
	case "proxyChecksum":
		p.ProxyChecksum = policy
	case "fullLinkTrace":
		p.FullLinkTrace = policy
	}
	return nil
}
