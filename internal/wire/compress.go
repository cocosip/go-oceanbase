package wire

import (
	"bytes"
	"compress/zlib"
	"io"
	"net"
)

const maxFrame = 0xffffff

type frameIO struct {
	conn       net.Conn
	sequence   byte
	obSequence byte
	buffer     []byte
	ob         *OB20Options
	requestID  uint32
	readBytes  int64
	trace      *fullTraceState
	outExtra   []byte
}

// EnableCompression selects zlib framing after the uncompressed authentication completes.
func (p *PacketIO) EnableCompression() error { return p.enableFrames(nil) }

func (p *PacketIO) enableFrames(ob *OB20Options) error {
	if p.failed {
		return ErrDesynchronized
	}
	if p.frames != nil || p.conn == nil || p.maxMessageBytes < 0 {
		return ErrMalformed
	}
	p.frames = &frameIO{conn: p.conn, ob: ob}
	if ob != nil && ob.FullLinkTrace {
		p.frames.trace = &fullTraceState{}
	}
	return nil
}

// Framed reports whether the physical stream has an outer sequence and command identity.
func (p *PacketIO) Framed() bool { return p.frames != nil }

func (f *frameIO) reset() error {
	if len(f.buffer) != 0 {
		return ErrDesynchronized
	}
	f.sequence = 0
	f.obSequence = 0
	f.requestID = (f.requestID + 1) & maxFrame
	return nil
}

func (f *frameIO) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for len(f.buffer) == 0 {
		var err error
		if f.ob != nil {
			err = f.readOB20()
		} else {
			err = f.readCompressed()
		}
		if err != nil {
			return 0, err
		}
	}
	n := copy(b, f.buffer)
	f.buffer = f.buffer[n:]
	return n, nil
}

func (f *frameIO) readCompressed() error {
	var h [7]byte
	if err := f.readFull(h[:]); err != nil {
		return err
	}
	n, expanded := int3(h[:3]), int3(h[4:])
	if h[3] != f.sequence {
		return ErrSequence
	}
	if n < 1 {
		return ErrMessageTooLarge
	}
	f.sequence++
	data := make([]byte, n)
	if err := f.readFull(data); err != nil {
		return err
	}
	if expanded == 0 {
		f.buffer = data
		return nil
	}
	input := bytes.NewReader(data)
	z, err := zlib.NewReader(input)
	if err != nil {
		return ErrMalformed
	}
	defer func() { _ = z.Close() }()
	f.buffer, err = io.ReadAll(io.LimitReader(z, int64(expanded)+1))
	if err != nil || len(f.buffer) != expanded || input.Len() != 0 {
		return ErrMalformed
	}
	return nil
}

func (f *frameIO) encodeCompressed(data []byte, payloadSize int) ([]byte, error) {
	expanded := 0
	// Connector/J tests native payload size, excluding the four-byte packet header.
	if payloadSize > 100 {
		var out bytes.Buffer
		z := zlib.NewWriter(&out)
		if _, err := z.Write(data); err != nil {
			return nil, err
		}
		if err := z.Close(); err != nil {
			return nil, err
		}
		if out.Len() < int(float32(0.9)*float32(payloadSize)) {
			expanded = len(data)
			data = out.Bytes()
		}
	}
	n := len(data)
	h := []byte{byte(n), byte(n >> 8), byte(n >> 16), f.sequence, byte(expanded), byte(expanded >> 8), byte(expanded >> 16)}
	f.sequence++
	return append(h, data...), nil
}

func (p *PacketIO) writeFramed(payload []byte) (int64, error) {
	var transferred int64
	for {
		n := min(len(payload), maxFrame)
		raw := make([]byte, 4, n+4)
		raw[0], raw[1], raw[2], raw[3] = byte(n), byte(n>>8), byte(n>>16), p.sequence
		raw = append(raw, payload[:n]...)
		payload = payload[n:]
		last := n < maxFrame
		frameLimit := maxFrame
		if p.frames.ob != nil {
			frameLimit = min(frameLimit, maxFrame-28)
		}
		payloadSize := n
		for len(raw) > 0 {
			limit := frameLimit
			if len(p.frames.outExtra) > 0 {
				limit -= 4 + len(p.frames.outExtra)
			}
			if limit < 1 {
				return transferred, ErrMessageTooLarge
			}
			width := min(len(raw), limit)
			var frame []byte
			var err error
			if p.frames.ob != nil {
				frame, err = p.frames.encodeOB20(raw[:width], last && width == len(raw))
			} else {
				frame, err = p.frames.encodeCompressed(raw[:width], payloadSize)
			}
			if err != nil {
				return transferred, err
			}
			for len(frame) > 0 {
				written, e := p.conn.Write(frame)
				if written < 0 || written > len(frame) {
					return transferred, io.ErrShortWrite
				}
				transferred += int64(written)
				frame = frame[written:]
				if e != nil {
					return transferred, e
				}
				if written == 0 {
					return transferred, io.ErrNoProgress
				}
			}
			raw = raw[width:]
			payloadSize = 0
		}
		p.sequence++
		if last {
			return transferred, nil
		}
	}
}

func (f *frameIO) readFull(data []byte) error {
	n, err := io.ReadFull(f.conn, data)
	f.readBytes += int64(n)
	return err
}

func int3(data []byte) int       { return int(data[0]) | int(data[1])<<8 | int(data[2])<<16 }
func put3(data []byte, n uint32) { data[0], data[1], data[2] = byte(n), byte(n>>8), byte(n>>16) }
