package wire

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
	"time"
)

func traceField(id uint16, value []byte) []byte {
	b := binary.LittleEndian.AppendUint16(nil, id)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(value)))
	return append(b, value...)
}

func TestFullTraceEncoding(t *testing.T) {
	info := FullTraceInfo{Module: "中", Action: "read", Enabled: true, ForcePrint: true, TraceID: [16]byte{1}, SpanID: [16]byte{2}}
	got, err := EncodeFullTrace(info)
	app := traceField(2001, nil)
	app = append(app, traceField(2002, []byte("中"))...)
	app = append(app, traceField(2003, []byte("read"))...)
	app = append(app, traceField(2004, nil)...)
	want := traceField(2001, app)
	span := traceField(2030, []byte{1})
	span = append(span, traceField(2031, []byte{1})...)
	span = append(span, traceField(2032, info.TraceID[:])...)
	span = append(span, traceField(2033, []byte{0})...)
	span = append(span, traceField(2034, info.SpanID[:])...)
	want = append(want, traceField(2004, span)...)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %x want %x: %v", got, want, err)
	}
	for _, invalid := range []FullTraceInfo{{Module: strings.Repeat("x", 65)}, {Action: "a\x00b"}, {ClientInfo: "\xff"}, {Enabled: true}} {
		if _, err := EncodeFullTrace(invalid); err == nil {
			t.Fatal("invalid trace accepted", invalid)
		}
	}
}

func TestFullTraceControl(t *testing.T) {
	fields := traceField(2020, []byte{2})
	fields = append(fields, traceField(2021, binary.LittleEndian.AppendUint64(nil, math.Float64bits(.5)))...)
	fields = append(fields, traceField(2022, []byte{3})...)
	fields = append(fields, traceField(2023, binary.LittleEndian.AppendUint64(nil, math.Float64bits(.25)))...)
	fields = append(fields, traceField(2024, binary.LittleEndian.AppendUint64(nil, 1234))...)
	fields = append(fields, traceField(2025, []byte{1})...)
	data := append(traceField(2003, fields), traceField(9999, []byte("unknown"))...)
	feedback, err := DecodeFullTrace(data, TraceFeedback{})
	want := TraceControl{Level: 2, Sample: .5, RecordPolicy: 3, PrintSample: .25, SlowThreshold: 1234 * time.Microsecond, ShowTrace: true}
	if err != nil || feedback.Control != want {
		t.Fatal(feedback, err)
	}
	for n := 1; n < len(traceField(2003, fields)); n++ {
		if _, err := DecodeFullTrace(data[:n], TraceFeedback{}); err == nil {
			t.Fatal("truncated control accepted", n)
		}
	}
	for _, field := range [][]byte{
		traceField(2020, nil), traceField(2021, binary.LittleEndian.AppendUint64(nil, math.Float64bits(math.NaN()))),
		traceField(2021, binary.LittleEndian.AppendUint64(nil, math.Float64bits(1.1))), traceField(2022, []byte{5}),
		traceField(2024, binary.LittleEndian.AppendUint64(nil, math.MaxUint64)), traceField(2025, []byte{2}),
	} {
		if _, err := DecodeFullTrace(traceField(2003, field), TraceFeedback{}); err == nil {
			t.Fatal("bad control accepted", field)
		}
	}
	query := traceField(2010, binary.LittleEndian.AppendUint64(nil, 100))
	query = append(query, traceField(2011, binary.LittleEndian.AppendUint64(nil, 200))...)
	feedback, err = DecodeFullTrace(traceField(2002, query), feedback)
	if err != nil || feedback.QueryStartMicros != 100 || feedback.QueryEndMicros != 200 || feedback.Control != want {
		t.Fatal(feedback, err)
	}
}
