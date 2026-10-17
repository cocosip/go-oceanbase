package wire

import "context"

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
			writes[i].Attempted = true
			writes[i].Err = writer.WriteMessage(ctx, command)
			if writes[i].Err != nil {
				_ = p.conn.Close()
				done <- writes[i].Err
				return
			}
		}
		done <- nil
	}()
	var readErr error
	for i, command := range commands {
		p.sequence = byte(len(command)/0xffffff + 1)
		if readErr = receive(i); readErr != nil {
			_ = p.conn.Close()
			break
		}
	}
	writeErr := <-done
	if readErr != nil || writeErr != nil {
		p.failed = true
	}
	if readErr != nil {
		return writes, readErr
	}
	return writes, writeErr
}
