package diagnostic_test

import (
	bytes "bytes"
	context "context"
	binary "encoding/binary"
	io "io"
	math "math"
	net "net"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	execution "github.com/cocosip/go-oceanbase/internal/execution"
	session "github.com/cocosip/go-oceanbase/internal/session"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func fltTestField(id uint16, value []byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, id)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(value)))
	return append(b, value...)
}
func TestTraceNilLogger(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	var extras [][]byte
	go func() {
		for i := range 3 {
			var header [31]byte
			if _, err := io.ReadFull(server, header[:]); err != nil {
				done <- err
				return
			}
			n := binary.LittleEndian.Uint32(header[19:])
			data := make([]byte, int(n)+4)
			if _, err := io.ReadFull(server, data); err != nil {
				done <- err
				return
			}
			width := binary.LittleEndian.Uint32(data)
			extras = append(extras, bytes.Clone(data[4:4+int(width)]))
			fields := fltTestField(2020, []byte{1})
			fields = append(fields, fltTestField(2021, binary.LittleEndian.AppendUint64(nil, math.Float64bits(1)))...)
			fields = append(fields, fltTestField(2022, []byte{1})...)
			trace := fltTestField(2003, fltTestField(2003, fields))
			body := binary.LittleEndian.AppendUint32(nil, uint32(len(trace)))
			body = append(body, trace...)
			body = append(body, 7, 0, 0, 1, 0, 0, 0, 6, 0, 0, 0)
			size := len(body) + 28
			response := make([]byte, 31)
			response[0], response[1], response[2], response[3] = byte(size), byte(size>>8), byte(size>>16), 1
			binary.LittleEndian.PutUint16(response[7:], 0x20ab)
			binary.LittleEndian.PutUint16(response[9:], 20)
			response[15], response[18] = byte(i+1), 1
			binary.LittleEndian.PutUint32(response[19:], uint32(len(body)))
			binary.LittleEndian.PutUint32(response[23:], 11)
			response = append(response, body...)
			response = append(response, 0, 0, 0, 0)
			if _, err := server.Write(response); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	p := wire.NewPacketIO(client, 4096)
	if err := p.EnableOB20(wire.OB20Options{NewExtra: true, FullLinkTrace: true}); err != nil {
		t.Fatal(err)
	}
	cfg := diagnosticConfig()
	var records []config.RequestDiagnostic
	cfg.Diagnostics.Request = func(_ context.Context, r config.RequestDiagnostic) { records = append(records, r) }
	c := execution.New(nil, session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: p, Mode: config.ModeOracle, Capabilities: wire.ClientProtocol41}))

	if err := c.Ping(oceanbase.WithTraceInfo(ctx, oceanbase.TraceInfo{Module: "only-first"})); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(oceanbase.WithTraceInfo(ctx, oceanbase.TraceInfo{TraceID: [16]byte{9}, ParentSpanID: [16]byte{8}})); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].TraceID != [16]byte{} || records[1].TraceID != [16]byte{9} || records[2].TraceID == [16]byte{} || records[1].SpanID == records[2].SpanID {
		t.Fatal(records)
	}
	if !bytes.Contains(extras[0], []byte("only-first")) || bytes.Contains(extras[1], []byte("only-first")) || bytes.Contains(extras[2], []byte("only-first")) {
		t.Fatal("app leaked")
	}
}
