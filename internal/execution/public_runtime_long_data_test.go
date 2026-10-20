package execution_test

import (
	context "context"
	errors "errors"
	fmt "fmt"
	io "io"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLongDataNilReaderSession(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				readers := []io.Reader{longReaderFunc(func([]byte) (int, error) { return 0, io.EOF }), longMapReader{}, longSliceReader{}, make(longChanReader)}
				status := byte(2)
				typ := byte(252)
				if mode == oceanbase.ModeOracle {
					status, typ = 6, 254
				}
				db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: mode}, framing, func(ctx context.Context, p *wire.PacketIO) {
					for range readers {
						if !publictest.ServePrepareStatus(ctx, t, p, selectBindSQL, 77, 1, nil, status) ||
							!testpeer.ReadCommand(ctx, t, p, longDataPacket(mode, 77, 0, 3, nil)) {
							return
						}
						if mode == oceanbase.ModeOracle && !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, status, 0, 0, 0}) {
							return
						}
						if !testpeer.ServeOK(ctx, t, p, append(publictest.ExecuteHeader(77), 0, 1, typ, 0), status) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 77)) {
							return
						}
					}
					if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				input := &oceanbase.LongData{Reader: longReaderFunc(nil), Length: -1}
				if _, err := db.ExecContext(context.Background(), selectBindSQL, input); !errors.Is(err, oceanbase.ErrLongData) {
					t.Fatal("nil Reader was not rejected before prepare/upload", err)
				}
				for i, reader := range readers {
					if i != 0 {
						input = &oceanbase.LongData{}
					}
					input.Reader = reader
					if _, err := db.ExecContext(context.Background(), selectBindSQL, input); err != nil {
						t.Fatalf("non-nil empty %T rejected: %v", reader, err)
					}
					if _, err := db.ExecContext(context.Background(), selectBindSQL, input); !errors.Is(err, oceanbase.ErrLongData) {
						t.Fatal("valid Reader was not consumed")
					}
				}
				if err := db.Ping(); err != nil {
					t.Fatal("Reader validation disturbed session reuse", err)
				}
			})
		}
	}
}
