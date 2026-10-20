package call_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	fmt "fmt"
	regexp "regexp"
	strconv "strconv"
	strings "strings"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOutMySQLSessionFinalization(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		p.ResetSequence()
		init, err := p.ReadMessage(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		pattern := regexp.MustCompile(`^\x03SET (@_obgo_[0-9a-f]{32}_0)=41,(@_obgo_[0-9a-f]{32}_1)=NULL$`)
		matches := pattern.FindStringSubmatch(string(init))
		if len(matches) != 3 {
			t.Errorf("unsafe or wrong variables: %q", init)
			return
		}
		vars := matches[1:]
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !testpeer.ReadQuery(ctx, t, p, "CALL p( "+vars[0]+" , "+vars[1]+" )") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("ordinary", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '9'}, []byte{254, 0, 0, 10, 0}, []byte{0, 2, 0, 2, 0, 0, 0}) || !testpeer.ReadQuery(ctx, t, p, "SELECT "+strings.Join(vars, ",")) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{2}, testpeer.TextColumn("a", 8, 63, 0), testpeer.TextColumn("b", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2', 2, 'o', 'k'}, []byte{254, 0, 0, 2, 0}) || !testpeer.ReadQuery(ctx, t, p, "SET "+vars[0]+"=NULL,"+vars[1]+"=NULL") || !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	a, b := int64(41), testOutBefore
	e, err := oceanbase.Execute(context.Background(), pinned, "CALL p(?,?)", sql.Out{Dest: &a, In: true}, sql.Out{Dest: &b})
	if err != nil || a != 41 || b != testOutBefore {
		t.Fatal(a, b, err)
	}
	if err = e.Close(); err != nil || a != 42 || b != "ok" {
		t.Fatal(a, b, err)
	}
	snapshot := e.Snapshot()
	if len(snapshot.Results) != 2 || snapshot.Results[0].Kind != oceanbase.ResultRows || snapshot.Results[1].RowsAffected != 2 || !snapshot.Done {
		t.Fatal("helper results leaked", snapshot)
	}
	if len(snapshot.Results[0].Columns) != 1 || snapshot.Results[0].Columns[0].Name != "ordinary" || snapshot.Results[1].Columns != nil {
		t.Fatal("OUT helper columns leaked", snapshot)
	}
	if err = pinned.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOutReturningProtocol(t *testing.T) {
	for _, prepare := range []config.PrepareMode{config.PrepareServer, config.PrepareClient} {
		for _, tc := range []struct {
			tailError bool
			flags     byte
		}{{false, 1}, {true, 1}, {false, 3}, {true, 3}} {
			tailError := tc.tailError
			t.Run(fmt.Sprint(prepare)+"/flags="+fmt.Sprint(tc.flags)+"/tailError="+strconv.FormatBool(tailError), func(t *testing.T) {
				db := publictest.OutOracleDB(t, prepare, func(ctx context.Context, p *wire.PacketIO) {
					want, err := wire.EncodePrepareExecute(0, []byte("INSERT INTO t VALUES(?) RETURNING id INTO ?"), []wire.Parameter{{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 42)}, {Type: 6}}, 4096)
					if err != nil || !testpeer.ReadCommand(ctx, t, p, want) {
						return
					}
					if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(9, 1, 2, tc.flags), testpeer.TextColumn("p", 8, 63, 0), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 43), []byte{254, 0, 0, 6, 0}) {
						return
					}
					tail := []byte{0, 1, 0, 6, 0, 0, 0}
					if tailError {
						tail = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
					}
					if !testpeer.SendPackets(ctx, t, p, tail) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				value := int64(7)
				result, err := db.ExecContext(context.Background(), "INSERT INTO t VALUES(:v) RETURNING id INTO :id", sql.Named("v", 42), sql.Named("id", sql.Out{Dest: &value}))
				if tailError {
					var server *oceanbase.Error
					if !errors.As(err, &server) || value != 7 {
						t.Fatal("tail error published output", value, err)
					}
				} else {
					if err != nil || value != 43 {
						t.Fatal(value, err)
					}
					if n, e := result.RowsAffected(); e != nil || n != 1 {
						t.Fatal(n, e)
					}
				}
				if err = db.Ping(); err != nil {
					t.Fatal("unread tail", err)
				}
			})
		}
	}
}

func TestOutValidation(t *testing.T) {
	var text string
	var number int64 = 42
	var nilInt *int
	for _, arg := range []any{sql.Out{Dest: &text}, sql.Out{Dest: &number, In: true}} {
		if _, err := normalizeArgument(t, arg); err != nil {
			t.Fatalf("valid output rejected: %v", err)
		}
	}
	for _, arg := range []any{sql.Out{}, sql.Out{Dest: nilInt}, sql.Out{Dest: number}, sql.Out{Dest: new(map[string]int)}, sql.Out{Dest: new(*sql.Rows)}, sql.Out{Dest: &failingOutScanner{}, In: true}} {
		if _, err := normalizeArgument(t, arg); err == nil {
			t.Fatal("invalid output accepted")
		}
	}
}
