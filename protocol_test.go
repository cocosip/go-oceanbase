package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testProxyCapability = "ob_capability_flag"
	testOB20            = "ob20"
	testCompression     = "compression"
	testStandard        = "standard"
)

func protocolGreeting(compression bool) []byte {
	b := authGreeting(authNative, false)
	start := bytes.IndexByte(b[1:], 0) + 2 + 4 + 8 + 1
	low := binary.LittleEndian.Uint16(b[start:])
	if compression {
		low |= 32
	}
	binary.LittleEndian.PutUint16(b[start:], low)
	high := binary.LittleEndian.Uint16(b[start+5:]) | uint16((wire.ClientSessionTrack|wire.ClientConnectAttrs)>>16)
	binary.LittleEndian.PutUint16(b[start+5:], high)
	return b
}

func protocolOK(status byte, values map[string]string) []byte {
	var state []byte
	for _, name := range []string{testProxyCapability, "__ob_client_capability_flag"} {
		if value, ok := values[name]; ok {
			pair := append([]byte{byte(len(name))}, name...)
			pair = append(pair, byte(len(value)))
			pair = append(pair, value...)
			state = append(state, 0, byte(len(pair)))
			state = append(state, pair...)
		}
	}
	if len(state) == 0 {
		return []byte{0, 0, 0, status, 0, 0, 0}
	}
	b := []byte{0, 0, 0, status, 0x40, 0, 0, 0, byte(len(state))}
	return append(b, state...)
}

func TestPacketNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		options     ProtocolOptions
		compression bool
		vars        map[string]string
		framed      bool
		metadata    bool
		unsupported bool
	}{
		{name: testStandard, options: ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff}},
		{name: "auto-no-offer"},
		{name: "compressed", compression: true, framed: true},
		{name: testOB20, vars: map[string]string{testProxyCapability: "33024"}, framed: true},
		{name: "ob20-required", options: ProtocolOptions{OB20: FeatureRequired}, vars: map[string]string{testProxyCapability: "256"}, framed: true},
		{name: "ob20-missing", options: ProtocolOptions{OB20: FeatureRequired}, unsupported: true},
		{name: "compress-missing", options: ProtocolOptions{Compression: FeatureRequired}, unsupported: true},
		{name: "metadata-missing", options: ProtocolOptions{NewMetadata: FeatureRequired}, unsupported: true},
		{name: "metadata-required", options: ProtocolOptions{NewMetadata: FeatureRequired}, vars: map[string]string{"__ob_client_capability_flag": "2"}, metadata: true},
		{name: "checksum-missing", options: ProtocolOptions{Checksum: FeatureRequired}, unsupported: true},
		{name: "checksum-required", options: ProtocolOptions{Checksum: FeatureRequired}, vars: map[string]string{testProxyCapability: "256"}, framed: true},
		{name: "off-unsolicited", options: ProtocolOptions{OB20: FeatureOff}, vars: map[string]string{testProxyCapability: "256"}, unsupported: true},
		{name: "trace-required", options: ProtocolOptions{FullLinkTrace: FeatureRequired}, vars: map[string]string{testProxyCapability: "16640"}, framed: true},
		{name: "trace-missing", options: ProtocolOptions{FullLinkTrace: FeatureRequired}, vars: map[string]string{testProxyCapability: "256"}, unsupported: true},
		{name: "trace-off", options: ProtocolOptions{FullLinkTrace: FeatureOff}, vars: map[string]string{testProxyCapability: "16640"}, unsupported: true},
		{name: "trace-without-ob20", vars: map[string]string{testProxyCapability: "16384"}, unsupported: true},
		{name: "invalid-ack", vars: map[string]string{testProxyCapability: "not-a-number"}, unsupported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := runAuthTest(t, Config{User: testUser, Protocol: tc.options}, func(ctx context.Context, p *wire.PacketIO) {
				if err := p.WriteMessage(ctx, protocolGreeting(tc.compression)); err != nil {
					t.Error(err)
					return
				}
				request, err := p.ReadMessage(ctx)
				if err != nil {
					return
				}
				offered := binary.LittleEndian.Uint32(request)&32 != 0
				if offered != (tc.compression && tc.options.Compression != FeatureOff) {
					t.Error("compression handshake flag")
				}
				_ = p.WriteMessage(ctx, protocolOK(2, tc.vars))
			})
			if tc.unsupported {
				if err == nil {
					t.Fatal("unsupported negotiation succeeded")
				}
				return
			}
			if err != nil || s.packets.Framed() != tc.framed || s.newMetadata != tc.metadata {
				t.Fatal(s, err)
			}
		})
	}
}

func TestPacketOptions(t *testing.T) {
	for _, opts := range []ProtocolOptions{{Compression: 255}, {OB20: 255}, {Checksum: 255}, {NewMetadata: 255}, {Compression: FeatureRequired, OB20: FeatureRequired}, {OB20: FeatureOff, Checksum: FeatureRequired}} {
		if _, err := NewConnector(Config{User: testUser, Protocol: opts}); err == nil {
			t.Fatal(opts)
		}
	}
	cfg, err := ParseDSN("oceanbase://u@localhost/?compression=off&ob20=required&checksum=auto&newMetadata=required")
	if err != nil || cfg.Protocol.Compression != FeatureOff || cfg.Protocol.OB20 != FeatureRequired || cfg.Protocol.NewMetadata != FeatureRequired {
		t.Fatal(cfg.Protocol, err)
	}
	if _, err = ParseDSN("oceanbase://u@localhost/?ob20=true"); err == nil {
		t.Fatal("non-policy accepted")
	}
}

func TestTraceOptions(t *testing.T) {
	for _, options := range []ProtocolOptions{{FullLinkTrace: 255}, {FullLinkTrace: FeatureRequired, OB20: FeatureOff}, {FullLinkTrace: FeatureRequired, Compression: FeatureRequired}} {
		if _, err := NewConnector(Config{User: testUser, Protocol: options}); err == nil {
			t.Fatal("invalid trace policy accepted", options)
		}
	}
	for _, options := range []DiagnosticOptions{{SlowThreshold: -1}, {MaxRequests: -1}, {MaxRequests: 1025}, {MaxPackets: -1}, {MaxPackets: 1025}} {
		if _, err := NewConnector(Config{User: testUser, Diagnostics: options}); err == nil {
			t.Fatal("invalid diagnostic limits accepted")
		}
	}
	cfg, err := ParseDSN("oceanbase://u@localhost/?fullLinkTrace=required")
	if err != nil || cfg.Protocol.FullLinkTrace != FeatureRequired {
		t.Fatal(cfg, err)
	}
	if _, err = ParseDSN("oceanbase://u@localhost/?fullLinkTrace=true"); err == nil {
		t.Fatal("non-policy accepted")
	}
}

func TestPacketModes(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprint(mode, "/", variant), func(t *testing.T) {
				status := byte(2)
				if mode == ModeOracle {
					status = 6
				}
				options := ProtocolOptions{OB20: FeatureOff, Compression: FeatureOff}
				if variant == testCompression {
					options.Compression = FeatureRequired
				}
				if variant == testOB20 {
					options.OB20 = FeatureRequired
				}
				dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !sendPackets(ctx, t, p, protocolGreeting(variant == testCompression)) {
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					vars := map[string]string{}
					if variant == testOB20 {
						vars[testProxyCapability] = "33024"
						vars["__ob_client_capability_flag"] = "2"
					}
					if !sendPackets(ctx, t, p, protocolOK(status, vars)) {
						return
					}
					if variant == testCompression {
						if err := p.EnableCompression(); err != nil {
							t.Error(err)
							return
						}
					}
					if variant == testOB20 {
						if err := p.EnableOB20(wire.OB20Options{ConnectionID: 42, Checksum: true, NewExtra: true}); err != nil {
							t.Error(err)
							return
						}
					}
					if !serveBaseline(ctx, t, p, status) {
						return
					}
					ctx = context.Background()
					if !readQuery(ctx, t, p, "SELECT v") {
						return
					}
					if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 253, 45, 0), []byte{254, 0, 0, status, 0}, []byte{3, 'a', 'b', 'c'}, []byte{254, 0, 0, status, 0}) {
						return
					}
					for i := int64(1); i <= 2; i++ {
						if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) || !readCommand(ctx, t, p, batchExecutePacket(t, i)) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
							return
						}
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				connector, err := NewConnector(Config{User: testUser, Mode: mode, Protocol: options, DialContext: dial})
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				defer func() { _ = db.Close() }()
				var got string
				if err = db.QueryRowContext(context.Background(), "SELECT v").Scan(&got); err != nil || got != "abc" {
					t.Fatal(got, err)
				}
				batch, err := ExecBatch(context.Background(), db, testBatchSQL, [][]any{{1}, {2}}, BatchOptions{Strategy: BatchMultiSend, ContinueOnError: variant != testStandard})
				if err != nil || batch.Strategy != BatchSequential || len(batch.Items) != 2 || batch.Items[1].Status != BatchSucceeded {
					t.Fatal(batch, err)
				}
			})
		}
	}
}

func TestNewMetadataCharacterUnits(t *testing.T) {
	c := &conn{session: &handshakeSession{mode: ModeOracle, newMetadata: true}}
	column, err := c.decodeColumn(textColumn("n", 207, 54, 0))
	if err != nil {
		t.Fatal(err)
	}
	column.Length = 9
	n, known := columnLength(column, ModeOracle)
	if !known || n != 9 {
		t.Fatal(n, known)
	}
}

func TestNewMetadataLengths(t *testing.T) {
	for _, tc := range []struct {
		typ       byte
		length    uint32
		precision byte
		want      int64
		known     bool
	}{
		{203, 99, 17, 17, true}, // RAW length comes from precision in new metadata.
		{209, 0, 18, 18, true},
		{210, 100, 0, 0, false},
		{211, 100, 0, 0, false},
		{207, 12, 0, 12, true},
		{208, 12, 0, 12, true},
	} {
		got, known := columnLength(wire.Column{Type: tc.typ, Length: tc.length, Precision: tc.precision, NewMetadata: true, Charset: 54}, ModeOracle)
		if got != tc.want || known != tc.known {
			t.Fatalf("type %d: length=%d known=%v", tc.typ, got, known)
		}
	}
}

func TestProxyChecksumPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy  FeatureMode
		ack     uint64
		wantErr bool
	}{
		{FeatureAuto, 0, false}, {FeatureOff, 0, false}, {FeatureRequired, 72, false},
		{FeatureRequired, 8, true}, {FeatureRequired, 0, true}, {FeatureOff, 72, true},
	} {
		cfg := Config{Protocol: ProtocolOptions{OB20: FeatureOff, Compression: FeatureOff, ProxyChecksum: tc.policy}}
		c := &connector{cfg: cfg}
		attrs := c.protocolAttributes()
		proxy, _ := strconv.ParseUint(attrs["__proxy_capability_flag"], 10, 64)
		if (proxy&72 == 72) != (tc.policy != FeatureOff) {
			t.Fatal("proxy checksum declaration", attrs)
		}
		s := &handshakeSession{packets: wire.NewPacketIO(nil, 64), capabilities: wire.ClientConnectAttrs}
		ok, err := wire.DecodeOK(protocolOK(2, map[string]string{testProxyCapability: strconv.FormatUint(tc.ack, 10)}), wire.ClientSessionTrack|wire.ClientProtocol41)
		if err != nil {
			t.Fatal(err)
		}
		s.ok = ok
		err = c.selectProtocol(s)
		if (err != nil) != tc.wantErr {
			t.Fatal(tc, err)
		}
		if err != nil && !errors.Is(err, ErrUnsupported) {
			t.Fatal(err)
		}
	}
	if _, err := NewConnector(Config{User: testUser, Protocol: ProtocolOptions{ProxyChecksum: 255}}); err == nil {
		t.Fatal("invalid proxy policy")
	}
	cfg, err := ParseDSN("oceanbase://u@localhost/?proxyChecksum=required&ob20=off")
	if err != nil || cfg.Protocol.ProxyChecksum != FeatureRequired {
		t.Fatal(cfg.Protocol, err)
	}
}
