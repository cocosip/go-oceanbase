package wire

import (
	"context"
	"time"
)

// CommandWrite records send evidence for one command; Attempted includes partial writes.
type CommandWrite struct {
	Attempted bool
	Err       error
}

// Pipeline sends bounded, independent commands while receive consumes each response in order.
// receive must consume a complete response. Any error closes the transport and joins the writer.
func (p *PacketIO) Pipeline(ctx context.Context, commands [][]byte, receive func(int) error) ([]CommandWrite, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	if p.Framed() {
		return nil, ErrMalformed
	}
	if receive == nil {
		return nil, ErrMalformed
	}
	for _, command := range commands {
		if _, err := messageSize(0, len(command), p.maxMessageBytes); err != nil {
			return nil, err
		}
	}
	writes := make([]CommandWrite, len(commands))
	if len(commands) == 0 {
		return writes, nil
	}
	done := make(chan error, 1)
	p.finishDiagnostic()
	var writeRecords []RequestDiagnostic
	base := 0
	if p.diagnostics != nil {
		base = len(p.diagnostics.completed)
		writeRecords = make([]RequestDiagnostic, min(len(commands), p.diagnostics.maxRequests-base))
	}
	// Each direction has its own sequence and failure state; PacketIO itself stays serialized.
	writer := NewPacketIO(p.conn, p.maxMessageBytes)
	go func() {
		for i, command := range commands {
			if err := ctx.Err(); err != nil {
				writes[i].Err = err
				_ = p.conn.Close()
				done <- err
				return
			}
			writer.ResetSequence()
			if i < len(writeRecords) {
				writer.EnableDiagnostics(1, p.diagnostics.maxPackets)
			} else {
				writer.diagnostics = nil
			}
			writes[i].Attempted = true
			writes[i].Err = writer.WriteMessage(ctx, command)
			if i < len(writeRecords) {
				records, _ := writer.TakeDiagnostics()
				if len(records) != 0 {
					writeRecords[i] = records[0]
				}
			}
			if writes[i].Err != nil {
				_ = p.conn.Close()
				done <- writes[i].Err
				return
			}
		}
		done <- nil
	}()
	var readErr error
	readCount := 0
	for i, command := range commands {
		p.beginDiagnostic(ctx, command)
		p.sequence = byte(len(command)/0xffffff + 1)
		readErr = receive(i)
		if p.diagnostics != nil && p.diagnostics.current != nil {
			p.diagnostics.current.Failed = p.diagnostics.current.Failed || readErr != nil
		}
		p.finishDiagnostic()
		readCount++
		if readErr != nil {
			_ = p.conn.Close()
			break
		}
	}
	writeErr := <-done
	p.mergePipelineDiagnostics(ctx, commands, writes, writeRecords, base, readCount)
	if readErr != nil || writeErr != nil {
		p.failed = true
	}
	if readErr != nil {
		return writes, readErr
	}
	return writes, writeErr
}

func (p *PacketIO) mergePipelineDiagnostics(ctx context.Context, commands [][]byte, writes []CommandWrite, writeRecords []RequestDiagnostic, base, readCount int) {
	if p.diagnostics != nil {
		for i, w := range writes {
			if i >= readCount && w.Attempted {
				p.beginDiagnostic(ctx, commands[i])
				if p.diagnostics.current != nil {
					p.diagnostics.current.Failed = true
				} // sent without a complete response
				p.finishDiagnostic()
			}
			if i < len(writeRecords) && base+i < len(p.diagnostics.completed) && !writeRecords[i].Started.IsZero() {
				r := &p.diagnostics.completed[base+i]
				sent := writeRecords[i]
				ended := r.Started.Add(r.Duration)
				r.Started = sent.Started
				r.Duration = max(ended.Sub(sent.Started), time.Duration(0))
				r.WriteBytes = sent.WriteBytes
				r.Failed = r.Failed || sent.Failed
				packets := append(sent.Packets, r.Packets...)
				r.DroppedPackets += sent.DroppedPackets + max(len(packets)-p.diagnostics.maxPackets, 0)
				r.Packets = packets[:min(len(packets), p.diagnostics.maxPackets)]
			}
		}
	}
}
