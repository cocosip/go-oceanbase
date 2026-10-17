// Package wire implements OceanBase protocol framing independently of database/sql.
package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Framing errors make the current stream unusable except pre-send size rejection.
var (
	ErrMessageTooLarge = errors.New("wire: logical message exceeds limit")
	ErrSequence        = errors.New("wire: invalid packet sequence")
	ErrDesynchronized  = errors.New("wire: stream is desynchronized")
)

// IOError preserves byte transfer evidence so callers can prevent unsafe replay.
type IOError struct {
	Op    string
	Bytes int64
	Err   error
}

func (e *IOError) Error() string {
	return fmt.Sprintf("wire: %s after %d bytes: %v", e.Op, e.Bytes, e.Err)
}
func (e *IOError) Unwrap() error { return e.Err }

// PacketIO is a serialized logical-message stream. It is not concurrency safe.
type PacketIO struct {
	conn            net.Conn
	maxMessageBytes int64
	sequence        byte
	failed          bool
	frames          *frameIO
	diagnostics     *diagnosticRecorder
}

// NewPacketIO uses a 64 MiB limit when maxMessageBytes is zero.
func NewPacketIO(conn net.Conn, maxMessageBytes int64) *PacketIO {
	if maxMessageBytes == 0 {
		maxMessageBytes = 64 << 20
	}
	return &PacketIO{conn: conn, maxMessageBytes: maxMessageBytes}
}

// ResetSequence begins a new command; it never revives a failed stream.
func (p *PacketIO) ResetSequence() {
	p.finishTrace()
	p.finishDiagnostic()
	p.sequence = 0
	if p.frames != nil && p.frames.reset() != nil {
		p.failed = true
	}
}

// ReplaceConn switches a transport at a completed message boundary, preserving sequence.
func (p *PacketIO) ReplaceConn(conn net.Conn) error {
	if p.failed {
		return ErrDesynchronized
	}
	if conn == nil {
		return errors.New("wire: nil replacement connection")
	}
	p.conn = conn
	if p.frames != nil {
		p.frames.conn = conn
	}
	return nil
}

// ReadMessage consumes one complete logical message, including continuation packets.
func (p *PacketIO) ReadMessage(ctx context.Context) (message []byte, err error) {
	var transferred int64
	sequence := p.sequence
	defer func() { p.notePacket(false, sequence, transferred, message, err) }()
	if err = p.check(ctx); err != nil {
		return nil, err
	}
	finish, err := p.prepareIO(ctx)
	if err != nil {
		return nil, p.fail(ctx, "read", 0, err)
	}
	defer finish()
	var reader io.Reader = p.conn
	if p.frames != nil {
		reader = p.frames
		p.frames.readBytes = 0
	}
	for {
		var header [4]byte
		n, readErr := io.ReadFull(reader, header[:])
		transferred += int64(n)
		if p.frames != nil {
			transferred = p.frames.readBytes
		}
		if readErr != nil {
			return nil, p.fail(ctx, "read", transferred, readErr)
		}
		if header[3] != p.sequence {
			return nil, p.fail(ctx, "read", transferred, ErrSequence)
		}
		length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
		total, sizeErr := messageSize(len(message), length, p.maxMessageBytes)
		if sizeErr != nil {
			return nil, p.fail(ctx, "read", transferred, sizeErr)
		}
		previous := len(message)
		message = append(message, make([]byte, total-previous)...)
		n, readErr = io.ReadFull(reader, message[previous:])
		transferred += int64(n)
		if p.frames != nil {
			transferred = p.frames.readBytes
		}
		if readErr != nil {
			return nil, p.fail(ctx, "read", transferred, readErr)
		}
		p.sequence++
		if length < 0xffffff {
			return message, nil
		}
	}
}

// WriteMessage sends one logical message without retrying failed I/O.
func (p *PacketIO) WriteMessage(ctx context.Context, payload []byte) (err error) {
	var transferred int64
	sequence := p.sequence
	defer func() { p.notePacket(true, sequence, transferred, nil, err) }()
	if err = p.check(ctx); err != nil {
		return err
	}
	if _, err = messageSize(0, len(payload), p.maxMessageBytes); err != nil {
		return err
	}
	if sequence == 0 {
		p.finishTrace()
		p.finishDiagnostic()
		if err = p.beginTrace(ctx); err != nil {
			return err
		}
		p.beginDiagnostic(ctx, payload)
	}
	finish, err := p.prepareIO(ctx)
	if err != nil {
		return p.fail(ctx, "write", 0, err)
	}
	defer finish()
	if p.frames != nil {
		transferred, err = p.writeFramed(payload)
		if err != nil {
			return p.fail(ctx, "write", transferred, err)
		}
		return nil
	}
	for {
		length := min(len(payload), 0xffffff)
		header := [4]byte{byte(length), byte(length >> 8), byte(length >> 16), p.sequence}
		for _, data := range [][]byte{header[:], payload[:length]} {
			for len(data) > 0 {
				n, writeErr := p.conn.Write(data)
				if n < 0 || n > len(data) {
					return p.fail(ctx, "write", transferred, io.ErrShortWrite)
				}
				transferred += int64(n)
				data = data[n:]
				if writeErr != nil {
					return p.fail(ctx, "write", transferred, writeErr)
				}
				if n == 0 {
					return p.fail(ctx, "write", transferred, io.ErrNoProgress)
				}
			}
		}
		p.sequence++
		payload = payload[length:]
		if length < 0xffffff {
			return nil
		}
	}
}

func messageSize(current, packet int, limit int64) (int, error) {
	if current < 0 || packet < 0 || packet > int(^uint(0)>>1)-current {
		return 0, ErrMessageTooLarge
	}
	total := current + packet
	if int64(total) > limit {
		return 0, ErrMessageTooLarge
	}
	return total, nil
}

func (p *PacketIO) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.failed {
		return ErrDesynchronized
	}
	if p.conn == nil {
		return errors.New("wire: nil connection")
	}
	if p.maxMessageBytes < 0 {
		return ErrMessageTooLarge
	}
	return nil
}

func (p *PacketIO) fail(ctx context.Context, operation string, n int64, err error) error {
	p.failed = true
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		err = context.DeadlineExceeded
	}
	return &IOError{Op: operation, Bytes: n, Err: err}
}

func (p *PacketIO) prepareIO(ctx context.Context) (func(), error) {
	deadline, _ := ctx.Deadline()
	// TLS reads may write control records, so refresh both directions at every entry.
	if err := p.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		if err := p.conn.SetDeadline(time.Now()); err != nil {
			_ = p.conn.Close()
		}
		close(done)
	})
	return func() {
		// Preserve complete responses and stop old callbacks before the next operation.
		if !stop() {
			<-done
		}
	}, nil
}
