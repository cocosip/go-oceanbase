package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const nationalChineseLiteral = "UNISTR('\\4E2D')"
const nationalEmojiLiteral = "UNISTR('\\D83D\\DE00')"
const nationalMixedLiteral = "UNISTR('\\4E2D\\D83D\\DE00')"
const nationalMixedUTF16 = "\x4e\x2d\xd8\x3d\xde\x00"

func TestNStringPrepareCharsets(t *testing.T) {
	for _, tc := range []struct {
		name, sqlCharset, national, input, encoded, literal string
	}{
		{"gbk-valid", charsetUTF8MB4, localFilenameCharset, "中", "\xd6\xd0", nationalChineseLiteral},
		{"gbk-invalid", charsetUTF8MB4, localFilenameCharset, "😀", "", ""},
		{"fallback-valid", localFilenameCharset, "", "中", "\xd6\xd0", nationalChineseLiteral},
		{"fallback-invalid", localFilenameCharset, "", "😀", "", ""},
		{"ascii-valid", charsetUTF8MB4, identityCharsetASCII, "A", "A", "UNISTR('\\0041')"},
		{"ascii-invalid", charsetUTF8MB4, identityCharsetASCII, "中", "", ""},
		{"utf8mb3-valid", charsetUTF8MB4, charsetUTF8MB3Alias, "中", "中", nationalChineseLiteral},
		{"utf8mb3-invalid", charsetUTF8MB4, charsetUTF8MB3Alias, "😀", "", ""},
		{"latin1-valid", charsetUTF8MB4, identityCharsetLatin, "€", "\x80", "UNISTR('\\20AC')"},
		{"latin1-invalid", charsetUTF8MB4, identityCharsetLatin, "中", "", ""},
		{"latin1-sql-utf16-national", identityCharsetLatin, testCharsetUTF16, "中😀", nationalMixedUTF16, nationalMixedLiteral},
		{"ascii-sql-utf16-national", identityCharsetASCII, testCharsetUTF16, "中😀", nationalMixedUTF16, nationalMixedLiteral},
		{"gbk-sql-utf8mb4-national", localFilenameCharset, charsetUTF8MB4, "😀", "😀", nationalEmojiLiteral},
		{"default-utf8mb4", "", "", "😀", "😀", nationalEmojiLiteral},
		{"empty-national-text", charsetUTF8MB4, testCharsetUTF16, "", "", "UNISTR('')"},
		{"malformed-national-text", charsetUTF8MB4, testCharsetUTF16, "\xff", "", ""},
	} {
		for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, prepare), func(t *testing.T) {
				options := SessionOptions{Charset: tc.sqlCharset, NCharset: tc.national}
				value := NString(tc.input)
				if prepare == PrepareServer {
					p, err := encodeParameter(value, ModeOracle, options, 1024)
					if tc.literal == "" {
						if !errors.Is(err, charset.ErrEncoding) {
							t.Fatal("unrepresentable national parameter accepted", p, err)
						}
					} else if err != nil || p.Type != 208 || !p.LengthEncoded || !bytes.Equal(p.Data, []byte(tc.encoded)) {
						t.Fatal("national parameter changed", p, err)
					}
					return
				}
				c := &conn{cfg: Config{Session: options}, session: &handshakeSession{mode: ModeOracle}}
				literal, err := c.clientLiteral(value, 1024)
				if tc.literal == "" {
					wantErr := charset.ErrEncoding
					if tc.name == "malformed-national-text" {
						wantErr = ErrType
					}
					if !errors.Is(err, wantErr) || literal != "" {
						t.Fatal("unrepresentable national literal accepted", literal, err)
					}
				} else if err != nil || literal != tc.literal {
					t.Fatal("national literal changed", literal, err)
				}
			})
		}
	}
}

func TestNStringSQLPrepareCharsets(t *testing.T) {
	for _, tc := range []struct {
		name, sqlCharset, national, input, encoded, literal, invalid string
		invalidErr                                                   error
	}{
		{"explicit-gbk", charsetUTF8MB4, localFilenameCharset, "中", "\xd6\xd0", nationalChineseLiteral, "😀", charset.ErrEncoding},
		{"fallback-gbk", localFilenameCharset, "", "中", "\xd6\xd0", nationalChineseLiteral, "😀", charset.ErrEncoding},
		{"explicit-utf8mb3", charsetUTF8MB4, charsetUTF8MB3Alias, "中", "中", nationalChineseLiteral, "😀", charset.ErrEncoding},
		{"explicit-ascii", charsetUTF8MB4, identityCharsetASCII, "A", "A", "UNISTR('\\0041')", "中", charset.ErrEncoding},
		{"explicit-utf16", charsetUTF8MB4, testCharsetUTF16, "中😀", nationalMixedUTF16, nationalMixedLiteral, "\xff", ErrType},
	} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
				t.Run(fmt.Sprintf("%s/%s/%d", tc.name, variant, prepare), func(t *testing.T) {
					const query = "UPDATE t SET v=?"
					db := batchRewriteDB(t, Config{Mode: ModeOracle, PrepareMode: prepare, Session: SessionOptions{Charset: tc.sqlCharset, NCharset: tc.national}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						payload := []byte("\x03UPDATE t SET v= " + tc.literal + " ")
						if prepare == PrepareServer {
							if !servePrepareStatus(ctx, t, p, query, 88, 1, nil, 6) {
								return
							}
							payload = append(executeHeader(88), 0, 1, 208, 0, byte(len(tc.encoded)))
							payload = append(payload, tc.encoded...)
						}
						for range 2 {
							if !serveOK(ctx, t, p, payload, 6) {
								return
							}
						}
						if prepare == PrepareServer && !readCommand(ctx, t, p, stmtPacket(25, 88)) {
							return
						}
						if !serveOK(ctx, t, p, []byte{14}, 6) {
							return
						}
						p.ResetSequence()
						serveQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					s, err := db.PrepareContext(context.Background(), query)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
					if _, err = s.ExecContext(context.Background(), NString(tc.input)); err != nil {
						t.Fatal("representable national input rejected", err)
					}
					if _, err = s.ExecContext(context.Background(), NString(tc.invalid)); !errors.Is(err, tc.invalidErr) {
						t.Fatal("invalid national input accepted", err)
					}
					if _, err = s.ExecContext(context.Background(), NString(tc.input)); err != nil {
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
