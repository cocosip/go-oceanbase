package wire

import (
	"encoding/binary"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const maxFullTraceBytes = 64 << 10

// FullTraceInfo contains one request's app and sampled span information, never SQL or values.
type FullTraceInfo struct {
	ClientIdentifier, Module, Action, ClientInfo string
	TraceID, SpanID                              [16]byte
	Enabled, ForcePrint                          bool
}

// TraceControl is the server's sampling and recording policy. RecordPolicy 1..4 matches the wire.
type TraceControl struct {
	Level               byte
	Sample, PrintSample float64
	RecordPolicy        byte
	SlowThreshold       time.Duration
	ShowTrace           bool
}

// TraceFeedback separates connection sampling policy from the current request's server times.
type TraceFeedback struct {
	Control                          TraceControl
	QueryStartMicros, QueryEndMicros int64
}

// ValidateTraceApp checks UTF-8 byte limits without truncating user data.
func ValidateTraceApp(info FullTraceInfo) error {
	for i, value := range []string{info.ClientIdentifier, info.Module, info.Action, info.ClientInfo} {
		limit := 64
		if i == 3 {
			limit = 256
		}
		if len(value) > limit || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return ErrMalformed
		}
	}
	return nil
}

// EncodeFullTrace emits all app fields, including empty ones, so pool reuse cannot retain app state.
func EncodeFullTrace(info FullTraceInfo) ([]byte, error) {
	if err := ValidateTraceApp(info); err != nil {
		return nil, err
	}
	if info.Enabled && (info.TraceID == [16]byte{} || info.SpanID == [16]byte{}) {
		return nil, ErrMalformed
	}
	var app []byte
	for i, value := range []string{info.ClientIdentifier, info.Module, info.Action, info.ClientInfo} {
		app = appendTraceField(app, uint16(2001+i), []byte(value))
	}
	data := appendTraceField(nil, 2001, app)
	if info.Enabled {
		span := appendTraceField(nil, 2030, []byte{1})
		force := byte(0)
		if info.ForcePrint {
			force = 1
		}
		span = appendTraceField(span, 2031, []byte{force})
		span = appendTraceField(span, 2032, info.TraceID[:])
		span = appendTraceField(span, 2033, []byte{0})
		span = appendTraceField(span, 2034, info.SpanID[:])
		data = appendTraceField(data, 2004, span)
	}
	return data, nil
}

func appendTraceField(dst []byte, id uint16, value []byte) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, id)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(value)))
	return append(dst, value...)
}

func nextTraceField(data []byte) (uint16, []byte, []byte, error) {
	if len(data) < 6 {
		return 0, nil, nil, ErrMalformed
	}
	n := binary.LittleEndian.Uint32(data[2:])
	if uint64(n) > uint64(len(data)-6) {
		return 0, nil, nil, ErrMalformed
	}
	return binary.LittleEndian.Uint16(data), data[6 : 6+int(n)], data[6+int(n):], nil
}

// DecodeFullTrace validates nested TLVs and merges partial policy updates atomically.
func DecodeFullTrace(data []byte, previous TraceFeedback) (TraceFeedback, error) {
	if len(data) > maxFullTraceBytes {
		return previous, ErrMessageTooLarge
	}
	result := previous
	for len(data) > 0 {
		id, fields, rest, err := nextTraceField(data)
		if err != nil {
			return previous, err
		}
		data = rest
		if id < 2001 || id > 2005 {
			continue
		}
		for len(fields) > 0 {
			field, value, tail, e := nextTraceField(fields)
			if e != nil {
				return previous, e
			}
			fields = tail
			if id == 2003 {
				e = decodeTraceControl(&result.Control, field, value)
			}
			if id == 2002 && (field == 2010 || field == 2011) {
				if len(value) != 8 || binary.LittleEndian.Uint64(value) > math.MaxInt64 {
					return previous, ErrMalformed
				}
				n := int64(binary.LittleEndian.Uint64(value))
				if field == 2010 {
					result.QueryStartMicros = n
				} else {
					result.QueryEndMicros = n
				}
			}
			if e != nil {
				return previous, e
			}
		}
	}
	return result, nil
}

func decodeTraceControl(control *TraceControl, id uint16, value []byte) error {
	switch id {
	case 2020, 2022, 2025:
		if len(value) != 1 {
			return ErrMalformed
		}
		switch id {
		case 2020:
			control.Level = value[0]
		case 2022:
			if value[0] < 1 || value[0] > 4 {
				return ErrMalformed
			}
			control.RecordPolicy = value[0]
		case 2025:
			if value[0] > 1 {
				return ErrMalformed
			}
			control.ShowTrace = value[0] == 1
		}
	case 2021, 2023:
		if len(value) != 8 {
			return ErrMalformed
		}
		n := math.Float64frombits(binary.LittleEndian.Uint64(value))
		if math.IsNaN(n) || n < 0 || n > 1 {
			return ErrMalformed
		}
		if id == 2021 {
			control.Sample = n
		} else {
			control.PrintSample = n
		}
	case 2024:
		if len(value) != 8 {
			return ErrMalformed
		}
		n := binary.LittleEndian.Uint64(value)
		if n > math.MaxInt64/uint64(time.Microsecond) {
			return ErrMalformed
		}
		control.SlowThreshold = time.Duration(n) * time.Microsecond
	}
	return nil
}
