package oceanbase

import (
	"fmt"
	"strconv"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// FeatureMode selects a negotiated capability. Auto uses it only when confirmed.
type FeatureMode uint8

// Feature policies never trigger a change of protocol after business commands have started.
const (
	FeatureAuto FeatureMode = iota
	FeatureOff
	FeatureRequired
)

// ProtocolOptions controls framing and result metadata at connection establishment.
// OB20 takes precedence over optional compression; requiring both is invalid.
type ProtocolOptions struct {
	Compression FeatureMode
	OB20        FeatureMode
	Checksum    FeatureMode
	NewMetadata FeatureMode
	// ProxyChecksum requests the legacy proxy checksum capabilities, independently of OB20 CRC.
	ProxyChecksum FeatureMode
	// FullLinkTrace negotiates OB20 trace extras independently of logging.
	FullLinkTrace FeatureMode
}

func validateProtocolOptions(p ProtocolOptions) error {
	if p.Compression > FeatureRequired || p.OB20 > FeatureRequired || p.Checksum > FeatureRequired || p.NewMetadata > FeatureRequired || p.ProxyChecksum > FeatureRequired || p.FullLinkTrace > FeatureRequired ||
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

func (c *connector) protocolAttributes() map[string]string {
	client := uint64(1) // existing locator-v2 declaration
	if c.cfg.Protocol.NewMetadata != FeatureOff {
		client |= 2
	}
	attrs := map[string]string{"__ob_client_attribute_capability_flag": strconv.FormatUint(client, 10)}
	var proxy uint64
	if c.cfg.Protocol.ProxyChecksum != FeatureOff {
		proxy |= 1<<3 | 1<<6
	}
	if c.cfg.Protocol.OB20 != FeatureOff && c.cfg.Protocol.Compression != FeatureRequired {
		// Do not request extra-OK, session synchronization or routing capabilities not yet implemented.
		proxy |= 1<<8 | 1<<15
		if c.cfg.Protocol.FullLinkTrace != FeatureOff {
			proxy |= 1<<14 | 1<<18
		}
	}
	if proxy != 0 {
		attrs["__proxy_capability_flag"] = strconv.FormatUint(proxy, 10)
	}
	return attrs
}

func (c *connector) selectProtocol(s *handshakeSession) error {
	vars, err := wire.DecodeSessionVariables(s.ok.State)
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
			return 0, fmt.Errorf("%w: invalid protocol capability acknowledgment", ErrUnsupported)
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
	if c.cfg.Protocol.ProxyChecksum == FeatureRequired && (proxy&proxyChecksum != proxyChecksum || s.capabilities&wire.ClientConnectAttrs == 0) || c.cfg.Protocol.ProxyChecksum == FeatureOff && proxy&proxyChecksum != 0 {
		return fmt.Errorf("%w: negotiated proxy checksum policy", ErrUnsupported)
	}
	ob := proxy&(1<<8) != 0
	metadata := client&2 != 0
	requestedOB := c.cfg.Protocol.OB20 != FeatureOff && c.cfg.Protocol.Compression != FeatureRequired && s.capabilities&wire.ClientConnectAttrs != 0
	requestedMeta := c.cfg.Protocol.NewMetadata != FeatureOff && s.capabilities&wire.ClientConnectAttrs != 0
	trace, err := c.validateTraceNegotiation(proxy, requestedOB)
	if err != nil {
		return err
	}
	if ob && !requestedOB || metadata && !requestedMeta || !ob && (c.cfg.Protocol.OB20 == FeatureRequired || c.cfg.Protocol.Checksum == FeatureRequired) || !metadata && c.cfg.Protocol.NewMetadata == FeatureRequired {
		return fmt.Errorf("%w: negotiated packet or metadata policy", ErrUnsupported)
	}
	s.newMetadata = metadata
	if ob {
		return s.packets.EnableOB20(wire.OB20Options{ConnectionID: s.hello.ConnectionID, Checksum: c.cfg.Protocol.Checksum != FeatureOff, RequireChecksum: c.cfg.Protocol.Checksum == FeatureRequired, NewExtra: proxy&(1<<15) != 0, FullLinkTrace: trace})
	}
	if s.capabilities&wire.ClientCompress != 0 {
		return s.packets.EnableCompression()
	}
	return nil
}

func (c *connector) validateTraceNegotiation(proxy uint64, requestedOB bool) (bool, error) {
	trace := proxy&(1<<14) != 0
	requested := requestedOB && c.cfg.Protocol.FullLinkTrace != FeatureOff
	if trace && (proxy&(1<<8) == 0 || !requested) || !trace && c.cfg.Protocol.FullLinkTrace == FeatureRequired || proxy&(1<<18) != 0 && !trace {
		return false, fmt.Errorf("%w: negotiated full link trace policy", ErrUnsupported)
	}
	return trace, nil
}
