package oceanbase

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
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

const proxyUserAttribute = "__ob_client_proxy_user_name"

func (c *connector) authenticationAttributes(credentials Credentials, capabilities uint64) (map[string]string, error) {
	if credentials.ProxyUser != "" && capabilities&wire.ClientConnectAttrs == 0 {
		return nil, fmt.Errorf("%w: proxy login requires connection attributes", ErrUnsupported)
	}
	return c.connectionAttributes(credentials.ProxyUser)
}

func (c *connector) connectionAttributes(proxyUser string) (map[string]string, error) {
	attrs := c.protocolAttributes()
	if proxyUser != "" {
		if c.cfg.Mode == ModeMySQL || strings.ContainsRune(proxyUser, 0) || int64(utf8.RuneCountInString(proxyUser)) > c.cfg.Limits.MaxMessageBytes {
			return nil, &ConfigError{Field: "ProxyUser"}
		}
		encoded, err := charset.Encode(c.cfg.Session.Charset, proxyUser)
		if err != nil {
			return nil, &ConfigError{Field: "ProxyUser"}
		}
		attrs[proxyUserAttribute] = string(encoded)
	}
	if len(c.cfg.ConnectionAttributes) == 0 && proxyUser == "" {
		return attrs, nil
	}
	invalid := &ConfigError{Field: "ConnectionAttributes"}
	budget := c.cfg.Limits.MaxMessageBytes
	// Every nonempty key requires at least three bytes including length prefixes.
	if int64(len(c.cfg.ConnectionAttributes)) > budget/3 {
		return nil, invalid
	}
	remaining := budget
	consume := func(key, value string) bool {
		for _, text := range []string{key, value} {
			n := int64(len(text))
			prefix := attributeLengthBytes(n)
			if prefix > remaining || n > remaining-prefix {
				return false
			}
			remaining -= n + prefix
		}
		return true
	}
	for key, value := range attrs {
		if !consume(key, value) {
			return nil, invalid
		}
	}
	for key, value := range c.cfg.ConnectionAttributes {
		if key == "" || strings.HasPrefix(key, "_") || key == "ob_capability_flag" || strings.ContainsRune(key, 0) || strings.ContainsRune(value, 0) || int64(utf8.RuneCountInString(key)) > remaining || int64(utf8.RuneCountInString(value)) > remaining {
			return nil, invalid
		}
		encodedKey, err := charset.Encode(c.cfg.Session.Charset, key)
		if err != nil {
			return nil, invalid
		}
		encodedValue, err := charset.Encode(c.cfg.Session.Charset, value)
		if err != nil {
			return nil, invalid
		}
		if _, exists := attrs[string(encodedKey)]; exists {
			return nil, invalid
		}
		if !consume(string(encodedKey), string(encodedValue)) {
			return nil, invalid
		}
		attrs[string(encodedKey)] = string(encodedValue)
	}
	if attributeLengthBytes(budget-remaining) > remaining {
		return nil, invalid
	}
	return attrs, nil
}

func attributeLengthBytes(n int64) int64 {
	switch {
	case n < 251:
		return 1
	case n < 1<<16:
		return 3
	case n < 1<<24:
		return 4
	default:
		return 9
	}
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
