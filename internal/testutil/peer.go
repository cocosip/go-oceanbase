// Package testutil provides deterministic protocol peers without driver imports.
package testutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"
)

// ErrInjected is the deterministic I/O failure selected by Faults.
var ErrInjected = errors.New("test peer: injected I/O failure")

// Faults counts calls from one and permits short operations and delayed failures.
type Faults struct {
	ReadFailAt, WriteFailAt int
	MaxRead, MaxWrite       int
	ReadDelay, WriteDelay   time.Duration
}

// FaultConn wraps a connection with a deterministic fault schedule.
type FaultConn struct {
	net.Conn
	faults        Faults
	reads, writes atomic.Int64
}

// NewFaultConn retains the supplied connection and fault schedule.
func NewFaultConn(conn net.Conn, faults Faults) *FaultConn {
	return &FaultConn{Conn: conn, faults: faults}
}
func (c *FaultConn) Read(b []byte) (int, error) {
	call := c.reads.Add(1)
	if c.faults.ReadDelay > 0 {
		time.Sleep(c.faults.ReadDelay)
	}
	if call == int64(c.faults.ReadFailAt) {
		return 0, ErrInjected
	}
	if c.faults.MaxRead > 0 && len(b) > c.faults.MaxRead {
		b = b[:c.faults.MaxRead]
	}
	return c.Conn.Read(b)
}
func (c *FaultConn) Write(b []byte) (int, error) {
	call := c.writes.Add(1)
	if c.faults.WriteDelay > 0 {
		time.Sleep(c.faults.WriteDelay)
	}
	if call == int64(c.faults.WriteFailAt) {
		return 0, ErrInjected
	}
	if c.faults.MaxWrite > 0 && len(b) > c.faults.MaxWrite {
		b = b[:c.faults.MaxWrite]
	}
	return c.Conn.Write(b)
}

// Counts returns attempted reads and writes, including injected failures.
func (c *FaultConn) Counts() (int64, int64) { return c.reads.Load(), c.writes.Load() }

// Step optionally consumes exact command bytes before sending a response.
type Step struct {
	Want, Reply []byte
	Delay       time.Duration
}

// RunScript runs a peer script and counts fully received commands.
// The caller owns the connection and sets its deadlines.
func RunScript(conn net.Conn, steps []Step) (int, error) {
	count := 0
	for i, step := range steps {
		if len(step.Want) > 0 {
			got := make([]byte, len(step.Want))
			if _, err := io.ReadFull(conn, got); err != nil {
				return count, fmt.Errorf("peer step %d: %w", i, err)
			}
			if !bytes.Equal(got, step.Want) {
				return count, fmt.Errorf("peer step %d: unexpected command", i)
			}
			count++
		}
		if step.Delay > 0 {
			time.Sleep(step.Delay)
		}
		for data := step.Reply; len(data) > 0; {
			n, err := conn.Write(data)
			if n < 0 || n > len(data) {
				return count, io.ErrShortWrite
			}
			data = data[n:]
			if err != nil {
				return count, err
			}
			if n == 0 {
				return count, io.ErrNoProgress
			}
		}
	}
	return count, nil
}
