package config

import (
	"github.com/cocosip/go-oceanbase/internal/configspec"
)

// FeatureMode selects a negotiated capability. Auto uses it only when confirmed.
type FeatureMode uint8

// Feature policies never trigger a change of protocol after business commands have started.
const (
	// FeatureDefault uses the option's default: compression off, other features auto.
	FeatureDefault FeatureMode = iota
	FeatureOff
	FeatureRequired
	FeatureAuto
)

// ProtocolOptions controls framing and result metadata at connection establishment.
// OB20 takes precedence over optional compression; requiring both is invalid.
type ProtocolOptions struct {
	// Compression defaults off, matching Connector/J. Auto opts in with fallback;
	// Required opts in without fallback and is a Go-specific extension.
	Compression FeatureMode
	OB20        FeatureMode
	Checksum    FeatureMode
	NewMetadata FeatureMode
	// ProxyChecksum requests the legacy proxy checksum capabilities, independently of OB20 CRC.
	ProxyChecksum FeatureMode
	// FullLinkTrace negotiates OB20 trace extras independently of logging.
	FullLinkTrace FeatureMode
}

func (p *ProtocolOptions) normalize() {
	if p.Compression == FeatureDefault {
		p.Compression = FeatureOff
	}
	for _, feature := range []*FeatureMode{&p.OB20, &p.Checksum, &p.NewMetadata, &p.ProxyChecksum, &p.FullLinkTrace} {
		if *feature == FeatureDefault {
			*feature = FeatureAuto
		}
	}
}

func validateProtocolOptions(p ProtocolOptions) error {
	if p.Compression > FeatureAuto || p.OB20 > FeatureAuto || p.Checksum > FeatureAuto || p.NewMetadata > FeatureAuto || p.ProxyChecksum > FeatureAuto || p.FullLinkTrace > FeatureAuto ||
		p.Compression == FeatureRequired && (p.OB20 == FeatureRequired || p.Checksum == FeatureRequired || p.FullLinkTrace == FeatureRequired) || p.OB20 == FeatureOff && (p.Checksum == FeatureRequired || p.FullLinkTrace == FeatureRequired) {
		return &ConfigError{Field: "Protocol"}
	}
	return nil
}

func parseFeatureMode(value string) (FeatureMode, error) {
	switch value {
	case "auto":
		return FeatureAuto, nil
	case "off":
		return FeatureOff, nil
	case "required":
		return FeatureRequired, nil
	default:
		return FeatureAuto, &ConfigError{Field: "protocol policy"}
	}
}

func protocolAttributes(p ProtocolOptions) map[string]string {
	return configspec.ProtocolAttributes(p.NewMetadata != FeatureOff, p.ProxyChecksum != FeatureOff, p.OB20 != FeatureOff, p.Compression == FeatureRequired, p.FullLinkTrace != FeatureOff)
}

func connectionAttributes(cfg Config, proxyUser string) (map[string]string, error) {
	attrs, field := configspec.ConnectionAttributes(protocolAttributes(cfg.Protocol), cfg.ConnectionAttributes, proxyUser, cfg.Session.Charset, cfg.Mode == ModeMySQL, cfg.Limits.MaxMessageBytes)
	if field != "" {
		return nil, &ConfigError{Field: field}
	}
	return attrs, nil
}
