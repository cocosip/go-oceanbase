package valuecodec_test

import (
	"context"
	"errors"
	"fmt"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"testing"
)

const nationalChineseLiteral = "UNISTR('\\4E2D')"
const nationalMixedLiteral = "UNISTR('\\4E2D\\D83D\\DE00')"
const nationalMixedUTF16 = "\x4e\x2d\xd8\x3d\xde\x00"

func TestNStringSQLPrepareCharsets(t *testing.T) {
	for _, tc := range []struct {
		name, sqlCharset, national, input, encoded, literal, invalid string
		invalidErr                                                   error
	}{
		{"explicit-gbk", charsetUTF8MB4, localFilenameCharset, "中", "\xd6\xd0", nationalChineseLiteral, "😀", charset.ErrEncoding},
		{"fallback-gbk", localFilenameCharset, "", "中", "\xd6\xd0", nationalChineseLiteral, "😀", charset.ErrEncoding},
		{"explicit-utf8mb3", charsetUTF8MB4, charsetUTF8MB3Alias, "中", "中", nationalChineseLiteral, "😀", charset.ErrEncoding},
		{"explicit-ascii", charsetUTF8MB4, identityCharsetASCII, "A", "A", "UNISTR('\\0041')", "中", charset.ErrEncoding},
		{"explicit-utf16", charsetUTF8MB4, testCharsetUTF16, "中😀", nationalMixedUTF16, nationalMixedLiteral, "\xff", oceanbase.ErrType},
	} {
		for _, variant := range []string{publictest.TestStandard, publictest.TestCompression, publictest.TestOB20} {
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprintf("%s/%s/%d", tc.name, variant, prepare), func(t *testing.T) {
					const query = "UPDATE t SET v=?"
					db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: oceanbase.ModeOracle, PrepareMode: prepare, Session: oceanbase.SessionOptions{Charset: tc.sqlCharset, NCharset: tc.national}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						payload := []byte("\x03UPDATE t SET v= " + tc.literal + " ")
						if prepare == oceanbase.PrepareServer {
							if !publictest.ServePrepareStatus(ctx, t, p, query, 88, 1, nil, 6) {
								return
							}
							payload = append(publictest.ExecuteHeader(88), 0, 1, 208, 0, byte(len(tc.encoded)))
							payload = append(payload, tc.encoded...)
						}
						for range 2 {
							if !testpeer.ServeOK(ctx, t, p, payload, 6) {
								return
							}
						}
						if prepare == oceanbase.PrepareServer && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 88)) {
							return
						}
						if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					s, err := db.PrepareContext(context.Background(), query)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
					if _, err = s.ExecContext(context.Background(), oceanbase.NString(tc.input)); err != nil {
						t.Fatal("representable national input rejected", err)
					}
					if _, err = s.ExecContext(context.Background(), oceanbase.NString(tc.invalid)); !errors.Is(err, tc.invalidErr) {
						t.Fatal("invalid national input accepted", err)
					}
					if _, err = s.ExecContext(context.Background(), oceanbase.NString(tc.input)); err != nil {
						t.Fatal("rejection disturbed prepared statement", err)
					}
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					if err = db.PingContext(context.Background()); err != nil {
						t.Fatal("rejection disturbed physical session", err)
					}
				})
			}
		}
	}
}
