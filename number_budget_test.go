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

func TestNumberNormalizedBudget(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"+1", "1"}, {"1.", "1"}, {"+.5e+0", "0.5"}, {"-1e0", "-1"},
		{"1.2300e2", "123.00"}, {"1e+2", "100"}, {"1e-2", "0.01"},
		{"1e" + strings.Repeat("0", 400), "1"},
	} {
		for _, limit := range []int64{int64(len(tc.want)), int64(len(tc.want)) - 1, 0, -1} {
			t.Run(fmt.Sprintf("%s/%d", tc.input, limit), func(t *testing.T) {
				got, err := plainNumber(tc.input, limit)
				if limit < int64(len(tc.want)) {
					if !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("normalized number exceeded budget", err)
					}
				} else if err != nil || got != tc.want {
					t.Fatal("fitting exact number rejected or changed", got, err)
				}
			})
		}
	}
	for _, text := range []string{"1e2147483647", "-1e-2147483648"} {
		if _, err := plainNumber(text, 1024); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("exponent expansion escaped budget", err)
		}
	}
}

func TestComplexNumberNormalizedBudget(t *testing.T) {
	for _, limit := range []int64{3, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			e := complexEncoder{nodes: maxComplexNodes, limit: limit, data: []byte{42}}
			err := e.scalar(typeNameNumber, Number("1e0000000"), 0)
			if limit == 2 {
				if !errors.Is(err, wire.ErrMessageTooLarge) || !bytes.Equal(e.data, []byte{42}) {
					t.Fatal("number field exceeded cumulative prefix budget", err)
				}
			} else if err != nil || !bytes.Equal(e.data, []byte{42, 1, '1'}) {
				t.Fatal("fitting number field rejected or changed", e.data, err)
			}
		})
	}
}

func TestNumberSQLNormalizedBudget(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
				t.Run(fmt.Sprintf("%d/%s/%d", mode, variant, prepare), func(t *testing.T) {
					status := byte(2)
					if mode == ModeOracle {
						status = 6
					}
					const query = "UPDATE t SET v=?"
					db := batchRewriteDB(t, Config{Mode: mode, PrepareMode: prepare, Limits: Limits{MaxMessageBytes: 256}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						payload := []byte("\x03UPDATE t SET v= 1 ")
						if prepare == PrepareServer {
							if !servePrepareStatus(ctx, t, p, query, 87, 1, nil, status) {
								return
							}
							payload = append(executeHeader(87), 0, 1, 246, 0, 1, '1')
						}
						if !serveOK(ctx, t, p, payload, status) {
							return
						}
						if prepare == PrepareServer && !readCommand(ctx, t, p, stmtPacket(25, 87)) {
							return
						}
						if !serveOK(ctx, t, p, []byte{14}, status) {
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
					if _, err = s.ExecContext(context.Background(), Number("1e"+strings.Repeat("0", 400))); err != nil {
						t.Fatal("fitting normalized number rejected", err)
					}
					if _, err = s.ExecContext(context.Background(), Number("1e256")); !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("oversize decimal expansion accepted", err)
					}
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					if err = db.PingContext(context.Background()); err != nil {
						t.Fatal("number rejection lost original session", err)
					}
				})
			}
		}
	}
}
