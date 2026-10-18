package wire

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// PacketDiagnostic contains metadata only; authentication and payload bytes are never retained.
type PacketDiagnostic struct {
	Write    bool
	Bytes    int64
	Sequence byte
	Failed   bool
}

// RequestDiagnostic is a bounded command snapshot, not a decoded business result.
type RequestDiagnostic struct {
	Context                          context.Context
	Command                          byte
	RequestID                        uint32
	Started                          time.Time
	Duration                         time.Duration
	ReadBytes, WriteBytes            int64
	SQLFingerprint                   string
	SQLBytes                         int
	ErrorCode                        uint16
	SQLState                         string
	Failed                           bool
	Packets                          []PacketDiagnostic
	DroppedPackets                   int
	TraceID, SpanID                  [16]byte
	QueryStartMicros, QueryEndMicros int64
}

type diagnosticRecorder struct {
	maxRequests, maxPackets int
	current                 *RequestDiagnostic
	completed               []RequestDiagnostic
	dropped                 int
	nextID                  uint32
}

// EnableDiagnostics starts observing only commands sent after authentication.
func (p *PacketIO) EnableDiagnostics(maxRequests, maxPackets int) {
	if maxRequests <= 0 {
		maxRequests = 64
	}
	if maxPackets <= 0 {
		maxPackets = 64
	}
	if p.diagnostics == nil {
		p.diagnostics = &diagnosticRecorder{}
	}
	p.diagnostics.maxRequests, p.diagnostics.maxPackets = maxRequests, maxPackets
}

func (p *PacketIO) beginDiagnostic(ctx context.Context, payload []byte) {
	if p.diagnostics == nil || len(payload) == 0 {
		return
	}
	p.finishDiagnostic()
	d := p.diagnostics
	d.nextID++
	id := d.nextID
	if p.frames != nil && p.frames.ob != nil {
		id = p.frames.requestID
	}
	r := &RequestDiagnostic{Context: ctx, Command: payload[0], RequestID: id, Started: time.Now()}
	query := diagnosticSQL(payload)
	if query != nil {
		r.SQLFingerprint, r.SQLBytes = sqlFingerprint(query), len(query)
	}
	d.current = r
}

// sqlFingerprint is a non-secret correlation value: it lets a caller group
// diagnostics by statement shape without retaining the statement text.
//
// SHA-256 is the documented contract for this field (architecture §18.3.1), so
// the algorithm must stay stable across processes and cannot become a keyed or
// memory-hard hash. The input is a SQL statement, never authentication material,
// which is why CodeQL's go/weak-sensitive-data-hashing does not apply here.
func sqlFingerprint(query []byte) string {
	sum := sha256.Sum256(query)
	return hex.EncodeToString(sum[:])
}

func diagnosticSQL(payload []byte) []byte {
	switch payload[0] {
	case 3, 22:
		return payload[1:]
	case 161:
		if len(payload) > 10 {
			d := decoder{data: payload, offset: 10}
			query := d.lenBytes()
			if d.err == nil {
				return query
			}
		}
	}
	return nil
}

func (p *PacketIO) notePacket(write bool, sequence byte, n int64, payload []byte, err error) {
	d := p.diagnostics
	if d == nil || d.current == nil {
		return
	}
	r := d.current
	if write {
		r.WriteBytes += n
	} else {
		r.ReadBytes += n
	}
	r.Failed = r.Failed || err != nil
	if !write && len(payload) >= 3 && payload[0] == 255 {
		r.ErrorCode = binary.LittleEndian.Uint16(payload[1:])
		r.Failed = true
		if len(payload) >= 9 && payload[3] == '#' {
			valid := true
			for _, b := range payload[4:9] {
				valid = valid && (b >= '0' && b <= '9' || b >= 'A' && b <= 'Z')
			}
			if valid {
				r.SQLState = string(payload[4:9])
			}
		}
	}
	if len(r.Packets) < d.maxPackets {
		r.Packets = append(r.Packets, PacketDiagnostic{Write: write, Bytes: n, Sequence: sequence, Failed: err != nil})
	} else {
		r.DroppedPackets++
	}
}

func (p *PacketIO) finishDiagnostic() {
	d := p.diagnostics
	if d == nil || d.current == nil {
		return
	}
	r := d.current
	r.Duration = time.Since(r.Started)
	if len(d.completed) < d.maxRequests {
		d.completed = append(d.completed, *r)
	} else {
		d.dropped++
	}
	d.current = nil
}

// MarkDiagnosticFailure records driver-side decoding failure before releasing the gate.
func (p *PacketIO) MarkDiagnosticFailure() {
	if d := p.diagnostics; d != nil && d.current != nil && d.current.Command != 1 {
		d.current.Failed = true
	}
}

// TakeDiagnostics transfers snapshots so the root driver can invoke callbacks after unlocking.
func (p *PacketIO) TakeDiagnostics() ([]RequestDiagnostic, int) {
	p.finishTrace()
	p.finishDiagnostic()
	if p.diagnostics == nil {
		return nil, 0
	}
	d := p.diagnostics
	completed, dropped := d.completed, d.dropped
	d.completed, d.dropped = nil, 0
	return completed, dropped
}
