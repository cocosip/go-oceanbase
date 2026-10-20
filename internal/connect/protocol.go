package connect

import (
	"fmt"
	"strconv"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/configspec"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// AuthenticationAttributes encodes configured and identity attributes for login.
func AuthenticationAttributes(cfg config.Config, credentials config.Credentials, capabilities uint64) (map[string]string, error) {
	if credentials.ProxyUser != "" && capabilities&wire.ClientConnectAttrs == 0 {
		return nil, fmt.Errorf("%w: proxy login requires connection attributes", dberrors.ErrUnsupported)
	}
	return ConnectionAttributes(cfg, credentials.ProxyUser)
}

// SelectProtocol validates login acknowledgements before enabling packet framing.
func SelectProtocol(cfg config.Config, s *session.Negotiated) error {
	vars, err := wire.DecodeSessionVariables(s.OK.State)
	if err != nil {
		return err
	}
	parse := func(name string) (uint64, error) {
		text, exists := vars[name]
		if !exists {
			return 0, nil
		}
		value, e := strconv.ParseUint(text, 10, 64)
		if e != nil {
			return 0, fmt.Errorf("%w: invalid protocol capability acknowledgment", dberrors.ErrUnsupported)
		}
		return value, nil
	}
	proxy, err := parse("ob_capability_flag")
	if err != nil {
		return err
	}
	client, err := parse("__ob_client_capability_flag")
	if err != nil {
		return err
	}
	const proxyChecksum = 1<<3 | 1<<6
	if cfg.Protocol.ProxyChecksum == config.FeatureRequired && (proxy&proxyChecksum != proxyChecksum || s.Capabilities&wire.ClientConnectAttrs == 0) || cfg.Protocol.ProxyChecksum == config.FeatureOff && proxy&proxyChecksum != 0 {
		return fmt.Errorf("%w: negotiated proxy checksum policy", dberrors.ErrUnsupported)
	}
	ob := proxy&(1<<8) != 0
	metadata := client&2 != 0
	requestedOB := cfg.Protocol.OB20 != config.FeatureOff && cfg.Protocol.Compression != config.FeatureRequired && s.Capabilities&wire.ClientConnectAttrs != 0
	requestedMeta := cfg.Protocol.NewMetadata != config.FeatureOff && s.Capabilities&wire.ClientConnectAttrs != 0
	trace, err := validateTraceNegotiation(cfg, proxy, requestedOB)
	if err != nil {
		return err
	}
	if ob && !requestedOB || metadata && !requestedMeta || !ob && (cfg.Protocol.OB20 == config.FeatureRequired || cfg.Protocol.Checksum == config.FeatureRequired) || !metadata && cfg.Protocol.NewMetadata == config.FeatureRequired {
		return fmt.Errorf("%w: negotiated packet or metadata policy", dberrors.ErrUnsupported)
	}
	s.NewMetadata = metadata
	if ob {
		return s.Packets.EnableOB20(wire.OB20Options{ConnectionID: s.Hello.ConnectionID, Checksum: cfg.Protocol.Checksum != config.FeatureOff, RequireChecksum: cfg.Protocol.Checksum == config.FeatureRequired, NewExtra: proxy&(1<<15) != 0, FullLinkTrace: trace})
	}
	if s.Capabilities&wire.ClientCompress != 0 {
		return s.Packets.EnableCompression()
	}
	return nil
}

func validateTraceNegotiation(cfg config.Config, proxy uint64, requestedOB bool) (bool, error) {
	trace := proxy&(1<<14) != 0
	requested := requestedOB && cfg.Protocol.FullLinkTrace != config.FeatureOff
	if trace && (proxy&(1<<8) == 0 || !requested) || !trace && cfg.Protocol.FullLinkTrace == config.FeatureRequired || proxy&(1<<18) != 0 && !trace {
		return false, fmt.Errorf("%w: negotiated full link trace policy", dberrors.ErrUnsupported)
	}
	return trace, nil
}

// ProtocolAttributes describes the requested OceanBase protocol features.
func ProtocolAttributes(cfg config.Config) map[string]string {
	p := cfg.Protocol
	return configspec.ProtocolAttributes(p.NewMetadata != config.FeatureOff, p.ProxyChecksum != config.FeatureOff, p.OB20 != config.FeatureOff, p.Compression == config.FeatureRequired, p.FullLinkTrace != config.FeatureOff)
}

// ConnectionAttributes combines and encodes attributes within the message budget.
func ConnectionAttributes(cfg config.Config, proxyUser string) (map[string]string, error) {
	attrs, field := configspec.ConnectionAttributes(ProtocolAttributes(cfg), cfg.ConnectionAttributes, proxyUser, cfg.Session.Charset, cfg.Mode == config.ModeMySQL, cfg.Limits.MaxMessageBytes)
	if field != "" {
		return nil, &config.ConfigError{Field: field}
	}
	return attrs, nil
}
