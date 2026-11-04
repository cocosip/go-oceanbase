//go:build integration

package integrationtest

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
)

// ReceiptLossConn discards one complete standard-protocol OK before the driver sees it.
// The fixture requires a plaintext endpoint; it does not simulate a server crash.
type ReceiptLossConn struct {
	net.Conn
	armed, acknowledged atomic.Bool
}

// Arm selects the next read, after all transaction work has completed.
func (c *ReceiptLossConn) Arm() { c.armed.Store(true) }

// Acknowledged reports that a complete successful OK was consumed, not just a header.
func (c *ReceiptLossConn) Acknowledged() bool { return c.acknowledged.Load() }

func (c *ReceiptLossConn) Read(p []byte) (int, error) {
	if !c.armed.Swap(false) {
		return c.Conn.Read(p)
	}
	defer func() { _ = c.Close() }()
	var header [4]byte
	if _, err := io.ReadFull(c.Conn, header[:]); err != nil {
		return 0, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if header[3] != 1 || n < 7 || n > 64*1024 {
		return 0, errors.New("receipt-loss fixture expected a bounded command OK")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c.Conn, body); err != nil {
		return 0, err
	}
	// Transaction OKs have zero affected rows/insert ID, then two-byte status/warnings.
	if body[0] != 0 || body[1] != 0 || body[2] != 0 || body[3]&8 != 0 {
		return 0, errors.New("receipt-loss fixture expected a final OK")
	}
	c.acknowledged.Store(true)
	return 0, io.ErrUnexpectedEOF
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
