package wire

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"time"
)

// TraceRequest is a value snapshot; only this package owns its context key.
type TraceRequest struct {
	App                   FullTraceInfo
	TraceID, ParentSpanID [16]byte
}

type traceContextKey struct{}

// WithTraceRequest carries an immutable request without wrapping ordinary driver contexts.
func WithTraceRequest(ctx context.Context, info TraceRequest) context.Context {
	return context.WithValue(ctx, traceContextKey{}, info)
}

// TraceRequestFromContext returns zero information for an unannotated request.
func TraceRequestFromContext(ctx context.Context) TraceRequest {
	info, _ := ctx.Value(traceContextKey{}).(TraceRequest)
	return info
}

type traceSpan struct {
	info    FullTraceInfo
	parent  [16]byte
	started time.Time
	control TraceControl
}

type fullTraceState struct {
	feedback TraceFeedback
	active   *traceSpan
	pending  []byte // one completed command; sent once with the next command
}

func tracePercentage() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read terminates the process on entropy failure.
	return float64(binary.LittleEndian.Uint64(b[:])>>11) / (1 << 53)
}

func (p *PacketIO) beginTrace(ctx context.Context) error {
	if p.frames == nil || p.frames.trace == nil {
		return nil
	}
	t := p.frames.trace
	request := TraceRequestFromContext(ctx)
	info := request.App
	control := t.feedback.Control
	info.Enabled = control.ShowTrace || control.Level > 0 && control.RecordPolicy >= 1 && control.RecordPolicy < 4 && tracePercentage() < control.Sample
	info.ForcePrint = info.Enabled && (control.ShowTrace || control.RecordPolicy == 1 || control.RecordPolicy == 3 && tracePercentage() < control.PrintSample)
	if info.Enabled {
		info.TraceID = request.TraceID
		if info.TraceID == [16]byte{} {
			_, _ = rand.Read(info.TraceID[:])
		}
		_, _ = rand.Read(info.SpanID[:])
	}
	data, err := EncodeFullTrace(info)
	if err != nil {
		return err
	}
	data = append(data, t.pending...)
	t.pending = nil
	p.frames.outExtra = encodeTraceExtra(data, p.frames.ob.NewExtra)
	t.active = &traceSpan{info: info, parent: request.ParentSpanID, started: time.Now(), control: control}
	t.feedback.QueryStartMicros, t.feedback.QueryEndMicros = 0, 0
	return nil
}

func (p *PacketIO) finishTrace() {
	if p.frames == nil || p.frames.trace == nil || p.frames.trace.active == nil {
		return
	}
	t := p.frames.trace
	s := t.active
	t.active = nil
	if p.diagnostics != nil && p.diagnostics.current != nil {
		r := p.diagnostics.current
		r.TraceID, r.SpanID = s.info.TraceID, s.info.SpanID
		r.QueryStartMicros, r.QueryEndMicros = t.feedback.QueryStartMicros, t.feedback.QueryEndMicros
	}
	ended := time.Now()
	if !s.info.Enabled || !s.info.ForcePrint && (s.control.SlowThreshold <= 0 || ended.Sub(s.started) < s.control.SlowThreshold) {
		return
	}
	data, _ := json.Marshal(struct {
		TraceID  string `json:"trace_id"`
		Name     string `json:"name"`
		ID       string `json:"id"`
		ParentID string `json:"parent_id"`
		Start    int64  `json:"start_ts"`
		End      int64  `json:"end_ts"`
		Follow   bool   `json:"is_follow"`
	}{traceIDString(s.info.TraceID), "oceanbase_go", traceIDString(s.info.SpanID), traceIDString(s.parent), s.started.UnixMicro(), ended.UnixMicro(), false})
	t.pending = appendTraceField(nil, 1, appendTraceField(nil, 1, append(data, 0)))
	if s.control.ShowTrace {
		show := append([]byte{'['}, data...)
		show = append(show, ']', 0)
		t.pending = appendTraceField(t.pending, 2005, appendTraceField(nil, 2050, show))
	}
}

func traceIDString(id [16]byte) string {
	s := hex.EncodeToString(id[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func encodeTraceExtra(data []byte, modern bool) []byte {
	if modern {
		return appendTraceField(nil, 2003, data)
	}
	b := appendLegacyVarchar(nil, []byte("full_trc"))
	return appendLegacyVarchar(b, data)
}

func appendLegacyVarchar(b, data []byte) []byte {
	b = append(b, 22, 2, 0, 255)
	b = binary.AppendUvarint(b, uint64(len(data)))
	b = append(b, data...)
	return append(b, 0)
}

func legacyVarchar(data []byte) ([]byte, []byte, error) {
	if len(data) < 4 {
		return nil, nil, ErrMalformed
	}
	if data[0] == 0 {
		return nil, data[4:], nil
	}
	if data[0] != 22 {
		return nil, nil, ErrMalformed
	}
	n, width := binary.Uvarint(data[4:])
	if width <= 0 || n >= uint64(len(data)-4-width) {
		return nil, nil, ErrMalformed
	}
	start := 4 + width
	end := start + int(n)
	if data[end] != 0 {
		return nil, nil, ErrMalformed
	}
	return data[start:end], data[end+1:], nil
}

func (f *frameIO) decodeTraceExtra(extra []byte, modern bool) error {
	feedback := TraceFeedback{}
	if f.trace != nil {
		feedback = f.trace.feedback
	}
	for len(extra) > 0 {
		var value, rest []byte
		var err error
		var fullTrace bool
		if modern {
			var id uint16
			id, value, rest, err = nextTraceField(extra)
			fullTrace = id == 2003
		} else {
			if f.trace == nil {
				return nil
			} // no legacy object decoding unless FLT was negotiated
			var key []byte
			key, rest, err = legacyVarchar(extra)
			if err == nil {
				value, rest, err = legacyVarchar(rest)
			}
			fullTrace = string(key) == "full_trc"
		}
		if err != nil {
			return err
		}
		if fullTrace && f.trace != nil {
			feedback, err = DecodeFullTrace(value, feedback)
			if err != nil {
				return err
			}
		}
		extra = rest
	}
	if f.trace != nil {
		f.trace.feedback = feedback
	}
	return nil
}
