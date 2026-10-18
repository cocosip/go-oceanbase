package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	parameterBudgetText      = "测测"
	parameterBudgetGBK       = "\xb2\xe2\xb2\xe2"
	parameterBudgetExpansion = "\x81\x30\x81\x30\x81\x30\x81\x30"
)

func TestParameterEncodedBudget(t *testing.T) {
	for _, tc := range []struct {
		name, charset, ncharset, text, encoded string
		national                               bool
	}{
		{localFilenameCharset, localFilenameCharset, "", parameterBudgetText, parameterBudgetGBK, false},
		{identityCharsetLatin, identityCharsetLatin, "", "€€", "\x80\x80", false},
		{testAttributeGB18030, testAttributeGB18030, "", "\u0080\u0080", parameterBudgetExpansion, false},
		{charsetUTF8MB4, charsetUTF8MB4, "", parameterBudgetText, parameterBudgetText, false},
		{testCharsetUTF16, charsetUTF8MB4, testCharsetUTF16, parameterBudgetText, "\x6d\x4b\x6d\x4b", true},
		{"national-gbk", charsetUTF8MB4, localFilenameCharset, parameterBudgetText, parameterBudgetGBK, true},
		{"national-fallback", localFilenameCharset, "", parameterBudgetText, parameterBudgetGBK, true},
		{"national-expansion", charsetUTF8MB4, testAttributeGB18030, "\u0080\u0080", parameterBudgetExpansion, true},
	} {
		for _, mode := range []Mode{ModeMySQL, ModeOracle} {
			if tc.national && mode != ModeOracle {
				continue
			}
			for _, limit := range []int64{int64(len(tc.encoded)), int64(len(tc.encoded)) - 1, 0, -1} {
				t.Run(fmt.Sprintf("%s/%d/%d", tc.name, mode, limit), func(t *testing.T) {
					var value any = tc.text
					typ := uint16(253)
					if tc.national {
						value, typ = NString(tc.text), 208
					}
					p, err := encodeParameter(value, mode, SessionOptions{Charset: tc.charset, NCharset: tc.ncharset}, limit)
					if limit < int64(len(tc.encoded)) {
						if !errors.Is(err, wire.ErrMessageTooLarge) {
							t.Fatal("encoded payload exceeded its budget", err)
						}
						return
					}
					if err != nil || p.Type != typ || !p.LengthEncoded || !bytes.Equal(p.Data, []byte(tc.encoded)) {
						t.Fatal("exact encoded budget rejected or data changed", err)
					}
				})
			}
		}
	}
}

func TestComplexTextEncodedBudget(t *testing.T) {
	for _, typ := range []string{typeNameVarchar2, typeNameNVarchar2} {
		for _, charset := range []string{localFilenameCharset, testCharsetUTF16} {
			if typ == typeNameVarchar2 && charset == testCharsetUTF16 {
				continue
			}
			for _, limit := range []int64{6, 5} {
				t.Run(fmt.Sprintf("%s/%s/%d", typ, charset, limit), func(t *testing.T) {
					options := SessionOptions{Charset: localFilenameCharset, NCharset: charset}
					e := complexEncoder{conn: &conn{cfg: Config{Session: options}}, nodes: maxComplexNodes, limit: limit, data: []byte{42}}
					err := e.scalar(typ, parameterBudgetText, 0)
					if limit == 5 {
						if !errors.Is(err, wire.ErrMessageTooLarge) || !bytes.Equal(e.data, []byte{42}) {
							t.Fatal("complex field escaped its cumulative budget", err)
						}
						return
					}
					want := []byte{42, 4, 0xb2, 0xe2, 0xb2, 0xe2}
					if charset == testCharsetUTF16 {
						want = []byte{42, 4, 0x6d, 0x4b, 0x6d, 0x4b}
					}
					if err != nil || !bytes.Equal(e.data, want) {
						t.Fatal("complex field rejected a fitting encoded value", err)
					}
				})
			}
		}
	}
}

func TestPreparedParameterEncodedBudget(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, national := range []bool{false, true} {
			if national && mode != ModeOracle {
				continue
			}
			for _, variant := range []string{testStandard, testCompression, testOB20} {
				t.Run(fmt.Sprintf("%d/%t/%s", mode, national, variant), func(t *testing.T) {
					status, typ := byte(2), uint16(253)
					if mode == ModeOracle {
						status = 6
					}
					if national {
						typ = 208
					}
					encoded := []byte(strings.Repeat("\xb2\xe2", 150))
					payload, err := wire.EncodeStmtExecute(84, []wire.Parameter{{Type: typ, Data: encoded, LengthEncoded: true}}, 4096)
					if err != nil {
						t.Fatal(err)
					}
					db := batchRewriteDB(t, Config{Mode: mode, Session: SessionOptions{Charset: localFilenameCharset}, Limits: Limits{MaxMessageBytes: int64(len(payload))}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						if !servePrepareStatus(ctx, t, p, testBatchSQL, 84, 1, nil, status) || !serveOK(ctx, t, p, payload, status) || !readCommand(ctx, t, p, stmtPacket(25, 84)) || !serveOK(ctx, t, p, []byte{14}, status) {
							return
						}
						p.ResetSequence()
						serveQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					s, err := db.PrepareContext(context.Background(), testBatchSQL)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
					text := strings.Repeat("测", 150)
					value, oversized := any(text), any(text+"x")
					if national {
						value, oversized = NString(text), NString(text+"x")
					}
					if _, err = s.ExecContext(context.Background(), value); err != nil {
						t.Fatal("fitting encoded parameter was rejected", err)
					}
					if _, err = s.ExecContext(context.Background(), oversized); !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("length prefix or execution header exceeded message budget", err)
					}
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					if err = db.PingContext(context.Background()); err != nil {
						t.Fatal("pre-send rejection damaged the physical session", err)
					}
				})
			}
		}
	}
}
