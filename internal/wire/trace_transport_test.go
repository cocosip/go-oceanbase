package wire

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func TestFullTraceTransport(t *testing.T) {
	for _, modern := range []bool{false, true} {
		c := newBufferConn(nil)
		p := NewPacketIO(c, 64)
		if err := p.EnableOB20(OB20Options{ConnectionID: 42, NewExtra: modern, FullLinkTrace: true}); err != nil {
			t.Fatal(err)
		}
		p.EnableDiagnostics(8, 8)
		p.frames.trace.feedback.Control = TraceControl{Level: 1, Sample: 1, RecordPolicy: 1}
		ctx := WithTraceRequest(context.Background(), TraceRequest{App: FullTraceInfo{Module: "first"}, TraceID: [16]byte{1}, ParentSpanID: [16]byte{2}})
		p.ResetSequence()
		if err := p.WriteMessage(ctx, []byte{14}); err != nil {
			t.Fatal(err)
		}
		first := bytes.Clone(c.out.Bytes())
		if binary.LittleEndian.Uint32(first[23:])&1 == 0 || !bytes.Contains(first, []byte("first")) {
			t.Fatal("missing trace app")
		}
		control := traceField(2021, binary.LittleEndian.AppendUint64(nil, math.Float64bits(0)))
		extra := encodeTraceExtra(traceField(2003, control), modern)
		data := binary.LittleEndian.AppendUint32(nil, uint32(len(extra)))
		data = append(data, extra...)
		data = append(data, 1, 0, 0, 1, 0)
		flags := uint32(3)
		if modern {
			flags |= 8
		}
		c.in = bytes.NewReader(ob20Fixture(data, 1, flags))
		if _, err := p.ReadMessage(ctx); err != nil {
			t.Fatal(err)
		}
		records, _ := p.TakeDiagnostics()
		if len(records) != 1 || records[0].TraceID != [16]byte{1} || records[0].SpanID == [16]byte{} {
			t.Fatal(records)
		}
		c.out.Reset()
		p.ResetSequence()
		if err := p.WriteMessage(context.Background(), []byte{14}); err != nil {
			t.Fatal(err)
		}
		second := c.out.Bytes()
		if bytes.Contains(second, []byte("first")) || !bytes.Contains(second, []byte("trace_id")) {
			t.Fatal("app leaked or completed span not flushed")
		}
		if p.frames.trace.active.info.Enabled {
			t.Fatal("sampling feedback ignored")
		}
		if p.frames.trace.active.info.Module != "" {
			t.Fatal("pooled app leaked")
		}
	}
}

func TestFullTracePolicies(t *testing.T) {
	for _, tc := range []struct {
		control          TraceControl
		sampled, printed bool
	}{
		{TraceControl{}, false, false},
		{TraceControl{Level: 1, Sample: 1, RecordPolicy: 4}, false, false},
		{TraceControl{Level: 1, Sample: 1, RecordPolicy: 1}, true, true},
		{TraceControl{Level: 1, Sample: 1, RecordPolicy: 2}, true, false},
		{TraceControl{Level: 1, Sample: 1, RecordPolicy: 2, SlowThreshold: time.Nanosecond}, true, true},
		{TraceControl{Level: 1, Sample: 1, RecordPolicy: 3, PrintSample: 1}, true, true},
		{TraceControl{ShowTrace: true}, true, true},
	} {
		p := NewPacketIO(newBufferConn(nil), 64)
		_ = p.EnableOB20(OB20Options{FullLinkTrace: true, NewExtra: true})
		p.frames.trace.feedback.Control = tc.control
		if err := p.WriteMessage(context.Background(), []byte{14}); err != nil {
			t.Fatal(err)
		}
		if p.frames.trace.active.info.Enabled != tc.sampled {
			t.Fatal(tc)
		}
		p.frames.trace.active.started = time.Now().Add(-time.Second)
		p.TakeDiagnostics()
		if (len(p.frames.trace.pending) > 0) != tc.printed {
			t.Fatal(tc)
		}
		if tc.control.ShowTrace && !bytes.Contains(p.frames.trace.pending, []byte{'[', '{'}) {
			t.Fatal("missing show trace span")
		}
	}
}

func TestFullTraceFragmentation(t *testing.T) {
	c := newBufferConn(nil)
	p := NewPacketIO(c, 32<<20)
	_ = p.EnableOB20(OB20Options{ConnectionID: 42, NewExtra: true, FullLinkTrace: true, Checksum: true})
	p.ResetSequence()
	payload := bytes.Repeat([]byte{7}, maxFrame+17)
	if err := p.WriteMessage(WithTraceRequest(context.Background(), TraceRequest{App: FullTraceInfo{Module: "fragment"}}), payload); err != nil {
		t.Fatal(err)
	}
	data := c.out.Bytes()
	count := 0
	for len(data) > 0 {
		n := int3(data[:3]) + 7
		flags := binary.LittleEndian.Uint32(data[23:])
		if (flags&1 != 0) != (count == 0) {
			t.Fatal("extra repeated on continuation", count)
		}
		count++
		data = data[n:]
	}
	peer := NewPacketIO(newBufferConn(c.out.Bytes()), 32<<20)
	_ = peer.EnableOB20(OB20Options{ConnectionID: 42, NewExtra: true, FullLinkTrace: true, RequireChecksum: true})
	peer.ResetSequence()
	got, err := peer.ReadMessage(context.Background())
	if err != nil || !bytes.Equal(got, payload) || count < 2 {
		t.Fatal("fragmented trace frame changed payload", count, err)
	}
}

func TestFullTraceMalformedExtra(t *testing.T) {
	for _, modern := range []bool{false, true} {
		extra := encodeTraceExtra(traceField(2003, traceField(2021, []byte{1})), modern)
		for n := 1; n <= len(extra); n++ {
			c := newBufferConn(nil)
			p := NewPacketIO(c, 64)
			_ = p.EnableOB20(OB20Options{ConnectionID: 42, FullLinkTrace: true})
			p.ResetSequence()
			data := binary.LittleEndian.AppendUint32(nil, uint32(n))
			data = append(data, extra[:n]...)
			data = append(data, 1, 0, 0, 0, 0)
			flags := uint32(3)
			if modern {
				flags |= 8
			}
			c.in = bytes.NewReader(ob20Fixture(data, 0, flags))
			if _, err := p.ReadMessage(context.Background()); err == nil {
				t.Fatal("malformed trace accepted", modern, n)
			}
		}
	}
}
