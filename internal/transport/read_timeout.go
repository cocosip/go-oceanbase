package transport

import (
	"net"
	"sync"
	"time"
)

// WithReadTimeout bounds each underlying read independently of idle application time.
// Keep this below TLS so its buffered records and control writes retain their normal behavior.
func WithReadTimeout(conn net.Conn, timeout time.Duration) net.Conn {
	return &readTimeoutConn{Conn: conn, timeout: timeout}
}

type readTimeoutConn struct {
	net.Conn
	timeout   time.Duration
	readMu    sync.Mutex
	mu        sync.Mutex
	deadline  time.Time
	readLimit time.Time
}

func (c *readTimeoutConn) Read(data []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	c.mu.Lock()
	c.readLimit = time.Now().Add(c.timeout)
	err := c.Conn.SetReadDeadline(c.limitReadDeadline(c.deadline))
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.readLimit = time.Time{}
		c.mu.Unlock()
	}()
	if err != nil {
		return 0, err
	}
	return c.Conn.Read(data)
}

func (c *readTimeoutConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = deadline
	return c.Conn.SetReadDeadline(c.limitReadDeadline(deadline))
}

func (c *readTimeoutConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = deadline
	// A pipeline writer must not extend an already blocked reader's timeout.
	if err := c.Conn.SetReadDeadline(c.limitReadDeadline(deadline)); err != nil {
		return err
	}
	return c.SetWriteDeadline(deadline)
}

func (c *readTimeoutConn) limitReadDeadline(deadline time.Time) time.Time {
	if !c.readLimit.IsZero() && (deadline.IsZero() || c.readLimit.Before(deadline)) {
		return c.readLimit
	}
	return deadline
}
