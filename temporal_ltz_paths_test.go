package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const ltzSQLPrefix = "CAST(TIMESTAMP '"
const ltzSQLSuffix = "' AS TIMESTAMP(9) WITH LOCAL TIME ZONE)"
const ltzSummerNewYork = "2024-07-01 12:34:56.123456789 America/New_York"
const ltzSpringGap = "2024-03-10 02:30:00 America/New_York"
const ltzAutumnOverlap = "2024-11-03 01:30:00 America/New_York"

func TestLTZPrepareEquivalence(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, input, literal string
		location             *time.Location
		wall                 []byte
	}{
		{"cross-region-utc", ltzSummerNewYork, "2024-07-01 16:34:56.123456789", time.UTC, []byte{20, 24, 7, 1, 16, 34, 56, 21, 205, 91, 7, 9}},
		{"cross-region-new-york", "2024-01-02 12:34:56 UTC", "2024-01-02 07:34:56", ny, []byte{20, 24, 1, 2, 7, 34, 56, 0, 0, 0, 0, 0}},
		{"same-region-overlap", ltzAutumnOverlap, "2024-11-03 01:30:00", ny, []byte{20, 24, 11, 3, 1, 30, 0, 0, 0, 0, 0, 0}},
		{"same-region-gap", ltzSpringGap, "2024-03-10 02:30:00", ny, []byte{20, 24, 3, 10, 2, 30, 0, 0, 0, 0, 0, 0}},
		{"source-overlap", ltzAutumnOverlap, "", time.UTC, nil},
		{"source-gap", ltzSpringGap, "", time.UTC, nil},
		{"target-overlap-early", "2024-11-03 01:30:00 -04:00", "", ny, nil},
		{"target-overlap-late", "2024-11-03 01:30:00 -05:00", "", ny, nil},
		{"target-overlap-region", "2024-11-03 05:30:00 UTC", "", ny, nil},
		{"offset-year-underflow", "0001-01-01 00:00:00 +14:00", "", time.UTC, nil},
		{"offset-year-overflow", "9999-12-31 23:59:59 -12:00", "", time.UTC, nil},
		{"region-year-underflow", "0001-01-01 00:00:00 Asia/Tokyo", "", time.UTC, nil},
		{"region-year-overflow", "9999-12-31 23:59:59 America/New_York", "", time.UTC, nil},
		{"explicit-offset", "2026-01-02 03:04:05.123 +08:00", "2026-01-02 03:04:05.123 +08:00", time.UTC, []byte{20, 26, 1, 1, 19, 4, 5, 192, 212, 84, 7, 3}},
		{"default-utc", ltzSummerNewYork, "2024-07-01 16:34:56.123456789", nil, []byte{20, 24, 7, 1, 16, 34, 56, 21, 205, 91, 7, 9}},
		{"fixed-utc-east", "2024-01-02 12:34:56 UTC", "2024-01-02 20:34:56", time.FixedZone("UTC", 8*3600), []byte{20, 24, 1, 2, 20, 34, 56, 0, 0, 0, 0, 0}},
		{"fixed-new-york-east", ltzSummerNewYork, "2024-07-02 00:34:56.123456789", time.FixedZone(ny.String(), 8*3600), []byte{20, 24, 7, 2, 0, 34, 56, 21, 205, 91, 7, 9}},
		{"fixed-new-york-west", ltzSummerNewYork, "2024-07-01 11:34:56.123456789", time.FixedZone(ny.String(), -5*3600), []byte{20, 24, 7, 1, 11, 34, 56, 21, 205, 91, 7, 9}},
		{"fixed-new-york-overlap", ltzAutumnOverlap, "", time.FixedZone(ny.String(), -4*3600), nil},
		{"fixed-new-york-gap", ltzSpringGap, "", time.FixedZone(ny.String(), -5*3600), nil},
		{"fixed-unknown-name", "2024-01-02 12:34:56 business", "", time.FixedZone("business", 8*3600), nil},
		{"fixed-year-overflow", "9999-12-31 23:59:59 UTC", "", time.FixedZone("UTC", 8*3600), nil},
	} {
		for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, prepare), func(t *testing.T) {
				options := SessionOptions{Location: tc.location}
				value := TimestampLTZ(tc.input)
				if prepare == PrepareServer {
					p, e := encodeParameter(value, ModeOracle, options, 1024)
					if tc.wall == nil {
						if !errors.Is(e, ErrType) {
							t.Fatal("invalid session time accepted", e)
						}
					} else if e != nil || p.Type != 201 || !p.LengthEncoded || !bytes.Equal(p.Data, tc.wall) {
						t.Fatal("session wall changed", p, e)
					}
					return
				}
				c := &conn{cfg: Config{Session: options}, session: &handshakeSession{mode: ModeOracle}}
				text, e := c.clientLiteral(value, 1024)
				if tc.wall == nil {
					if !errors.Is(e, ErrType) {
						t.Fatal("invalid session time accepted", text, e)
					}
				} else if e != nil || text != ltzSQLPrefix+tc.literal+ltzSQLSuffix {
					t.Fatal("session literal changed", text, e)
				}
			})
		}
	}
}

func TestLTZSQLPrepareEquivalence(t *testing.T) {
	for _, location := range []*time.Location{time.UTC, time.FixedZone("America/New_York", 0)} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
				t.Run(fmt.Sprintf("%s/%s/%d", location, variant, prepare), func(t *testing.T) {
					const query = "UPDATE t SET v=?"
					db := batchRewriteDB(t, Config{Mode: ModeOracle, PrepareMode: prepare, Session: SessionOptions{Location: location}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						payload := []byte("\x03UPDATE t SET v= " + ltzSQLPrefix + "2024-07-01 16:34:56.123456789" + ltzSQLSuffix + " ")
						if prepare == PrepareServer {
							if !servePrepareStatus(ctx, t, p, query, 88, 1, nil, 6) {
								return
							}
							payload = append(executeHeader(88), 0, 1, 201, 0, 12, 20, 24, 7, 1, 16, 34, 56, 21, 205, 91, 7, 9)
						}
						if !serveOK(ctx, t, p, payload, 6) {
							return
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
					if _, err = s.ExecContext(context.Background(), TimestampLTZ(ltzSummerNewYork)); err != nil {
						t.Fatal("resolvable region rejected", err)
					}
					if _, err = s.ExecContext(context.Background(), TimestampLTZ(ltzSpringGap)); !errors.Is(err, ErrType) {
						t.Fatal("source gap accepted", err)
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
