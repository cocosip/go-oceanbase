// Package publictest provides high-level public driver fixtures shared by owner tests.
package publictest

import (
	"context"
	"database/sql"
	"encoding/binary"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// BatchRewriteDB creates a local fake peer with batch capabilities and optional framing.
func BatchRewriteDB(t *testing.T, cfg oceanbase.Config, variant string, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	status := byte(2)
	if cfg.Mode == oceanbase.ModeOracle {
		status = 6
	}
	cfg.User = testpeer.User
	cfg.Protocol = oceanbase.ProtocolOptions{Compression: oceanbase.FeatureOff, OB20: oceanbase.FeatureOff}
	switch variant {
	case testpeer.Compression:
		cfg.Protocol.Compression = oceanbase.FeatureRequired
	case testpeer.OB20:
		cfg.Protocol.OB20 = oceanbase.FeatureRequired
	}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		greeting := testpeer.ProtocolGreeting(variant == testpeer.Compression)
		// The high capability word starts after the low word, charset and status.
		start := 1
		for greeting[start] != 0 {
			start++
		}
		start += 1 + 4 + 8 + 1 + 2 + 1 + 2
		if variant != "no-offer" {
			binary.LittleEndian.PutUint16(greeting[start:], binary.LittleEndian.Uint16(greeting[start:])|uint16(wire.ClientMultiStatements>>16))
		}
		if !testpeer.SendPackets(ctx, t, p, greeting) {
			return
		}
		if _, err := p.ReadMessage(ctx); err != nil {
			t.Error(err)
			return
		}
		vars := map[string]string{}
		if variant == testpeer.OB20 {
			vars["ob_capability_flag"] = "33024"
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(status, vars)) {
			return
		}
		switch variant {
		case testpeer.Compression:
			if err := p.EnableCompression(); err != nil {
				t.Error(err)
				return
			}
		case testpeer.OB20:
			if err := p.EnableOB20(wire.OB20Options{ConnectionID: 42, Checksum: true, NewExtra: true}); err != nil {
				t.Error(err)
				return
			}
		}
		ready := false
		if cfg.Session.Charset == "gbk" {
			ready = testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES gbk"), status) && testpeer.ServeOK(ctx, t, p, []byte("\x03"+"SET autocommit=1"), status) && testpeer.ServeVersion(ctx, t, p, status, false) && testpeer.ServeOK(ctx, t, p, []byte("\x03"+"SET SESSION time_zone = '+00:00'"), status)
		} else {
			ready = testpeer.ServeBaseline(ctx, t, p, status)
		}
		if ready && cfg.Session.NCharset != "" {
			ready = testpeer.ServeOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = '"+cfg.Session.NCharset+"'"), status)
		}
		if ready {
			serve(context.Background(), p)
		}
	})
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}
