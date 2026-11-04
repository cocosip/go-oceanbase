//go:build integration

package integrationtest

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
)

// ReceiptLossConn discards one complete successful response receipt before the
// driver sees it. The fixture requires a plaintext endpoint; it does not
// simulate a server crash.
type ReceiptLossConn struct {
	net.Conn
	armed, acknowledged atomic.Bool
	combined, text      atomic.Bool
	pending             []byte
	terminated          bool
}

// Arm selects the next read, after all transaction work has completed, and
// expects a standard-protocol command OK.
func (c *ReceiptLossConn) Arm() { c.armed.Store(true) }

// ArmCombined selects the next read and expects an OceanBase combined
// prepare/execute response whose trailing native OK is the receipt to drop.
func (c *ReceiptLossConn) ArmCombined() {
	c.combined.Store(true)
	c.armed.Store(true)
}

// ArmTextResult selects the next read and expects a text-protocol SELECT
// response whose final rowset EOF is the receipt to drop.
func (c *ReceiptLossConn) ArmTextResult() {
	c.text.Store(true)
	c.armed.Store(true)
}

// Acknowledged reports that a complete successful receipt was consumed, not
// just a header.
func (c *ReceiptLossConn) Acknowledged() bool { return c.acknowledged.Load() }

func (c *ReceiptLossConn) Read(p []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	if c.terminated {
		return 0, io.ErrUnexpectedEOF
	}
	if !c.armed.Swap(false) {
		return c.Conn.Read(p)
	}
	var err error
	if c.text.Load() {
		err = c.captureTextResult()
	} else if c.combined.Load() {
		err = c.captureCombined()
	} else {
		err = c.captureCommandOK()
	}
	c.terminated = true
	if err != nil {
		return 0, err
	}
	if len(c.pending) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// captureCommandOK consumes one complete standard-protocol OK packet.
func (c *ReceiptLossConn) captureCommandOK() error {
	defer func() { _ = c.Close() }()
	raw, body, err := readFixturePacket(c.Conn)
	if err != nil {
		return err
	}
	// Transaction OKs have zero affected rows/insert ID, then two-byte status/warnings.
	if raw[3] != 1 || len(body) < 7 || body[0] != 0 || body[1] != 0 || body[2] != 0 || body[3]&8 != 0 {
		return errors.New("receipt-loss fixture expected a bounded command OK")
	}
	c.acknowledged.Store(true)
	return nil
}

// captureCombined consumes a combined prepare/execute response up to and
// including the trailing native OK, which it drops. Metadata EOF packets are
// optional; a one-packet lookahead keeps both layouts aligned.
func (c *ReceiptLossConn) captureCombined() error {
	defer func() { _ = c.Close() }()
	var trace []string
	note := func(body []byte) {
		if len(trace) < 12 {
			trace = append(trace, fmt.Sprintf("len=%d first=% x", len(body), body[:min(len(body), 8)]))
		}
	}
	type packet struct{ raw, body []byte }
	var held *packet
	read := func() (packet, error) {
		if held != nil {
			p := *held
			held = nil
			return p, nil
		}
		raw, body, err := readFixturePacket(c.Conn)
		if err != nil {
			return packet{}, err
		}
		note(body)
		return packet{raw: raw, body: body}, nil
	}
	appendPacket := func(p packet) { c.pending = append(c.pending, p.raw...) }
	isEOF := func(p packet) bool { return len(p.body) > 0 && p.body[0] == 0xfe }

	first, err := read()
	if err != nil {
		return err
	}
	body := first.body
	if len(body) != 17 || body[0] != 0 || body[9] != 0 || body[16] != 1 {
		return fmt.Errorf("receipt-loss fixture expected a combined rowset response: %v", trace)
	}
	parameters := binary.LittleEndian.Uint16(body[7:])
	columns := binary.LittleEndian.Uint16(body[5:])
	if columns == 0 {
		return fmt.Errorf("receipt-loss fixture expected combined output columns: %v", trace)
	}
	appendPacket(first)
	for i := 0; i < int(parameters); i++ {
		p, err := read()
		if err != nil {
			return err
		}
		appendPacket(p)
	}
	if p, err := read(); err != nil {
		return err
	} else if isEOF(p) {
		appendPacket(p)
	} else {
		held = &p
	}
	for i := 0; i < int(columns); i++ {
		p, err := read()
		if err != nil {
			return err
		}
		appendPacket(p)
	}
	if p, err := read(); err != nil {
		return err
	} else if isEOF(p) {
		appendPacket(p)
	} else {
		held = &p
	}
	for {
		p, err := read()
		if err != nil {
			return err
		}
		appendPacket(p)
		if isEOF(p) {
			break
		}
	}
	tail, err := read()
	if err != nil {
		return err
	}
	// The trailing native OK may carry session-state information, so only the
	// final-status bit and the OK header are stable.
	if len(tail.body) < 6 || tail.body[0] != 0 || tail.body[3]&8 != 0 {
		return fmt.Errorf("receipt-loss fixture expected the combined tail OK: %v", trace)
	}
	c.acknowledged.Store(true)
	return nil
}

// captureTextResult consumes a text-protocol SELECT response and drops only the
// final rowset EOF. The column-count, definition and optional metadata EOF are
// forwarded so the driver can decode the rows.
func (c *ReceiptLossConn) captureTextResult() error {
	defer func() { _ = c.Close() }()
	type packet struct{ raw, body []byte }
	var held *packet
	read := func() (packet, error) {
		if held != nil {
			p := *held
			held = nil
			return p, nil
		}
		raw, body, err := readFixturePacket(c.Conn)
		if err != nil {
			return packet{}, err
		}
		return packet{raw: raw, body: body}, nil
	}
	appendPacket := func(p packet) { c.pending = append(c.pending, p.raw...) }
	isEOF := func(p packet) bool { return len(p.body) > 0 && p.body[0] == 0xfe }
	count, err := read()
	if err != nil {
		return err
	}
	if len(count.body) != 1 || count.body[0] == 0 || count.body[0] == 0xfe {
		return errors.New("receipt-loss fixture expected a text column count")
	}
	appendPacket(count)
	for i := 0; i < int(count.body[0]); i++ {
		p, err := read()
		if err != nil {
			return err
		}
		appendPacket(p)
	}
	if p, err := read(); err != nil {
		return err
	} else if isEOF(p) {
		appendPacket(p)
	} else {
		held = &p
	}
	for {
		p, err := read()
		if err != nil {
			return err
		}
		if isEOF(p) {
			c.acknowledged.Store(true)
			return nil
		}
		appendPacket(p)
	}
}

func readFixturePacket(conn net.Conn) (raw, body []byte, err error) {
	var header [4]byte
	if _, err = io.ReadFull(conn, header[:]); err != nil {
		return nil, nil, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if n < 1 || n > 64*1024 {
		return nil, nil, errors.New("receipt-loss fixture packet bound")
	}
	body = make([]byte, n)
	if _, err = io.ReadFull(conn, body); err != nil {
		return nil, nil, err
	}
	raw = append(append(make([]byte, 0, n+4), header[:]...), body...)
	return raw, body, nil
}

// OpenReceiptLoss pins a single physical connection and forbids automatic redial.
func OpenReceiptLoss(t *testing.T, p Profile) (*sql.Conn, *ReceiptLossConn) {
	t.Helper()
	cfg := ProfileConfig(t, p)
	if cfg.TLSConfig != nil {
		t.Fatal("receipt-loss fixture requires a plaintext endpoint")
	}
	cfg.Protocol = config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff,
		Checksum: config.FeatureOff, NewMetadata: config.FeatureOff, ProxyChecksum: config.FeatureOff,
		FullLinkTrace: config.FeatureOff}
	var fault *ReceiptLossConn
	var dialed atomic.Bool
	cfg.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if dialed.Swap(true) {
			return nil, errors.New("receipt-loss fixture forbids redial")
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		fault = &ReceiptLossConn{Conn: conn}
		return fault, nil
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		FailOperation(t, "receipt-loss connector", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	pinned, err := db.Conn(TestContext(t))
	if err != nil {
		FailOperation(t, "pin receipt-loss connection", err)
	}
	t.Cleanup(func() { _ = pinned.Close() })
	return pinned, fault
}

