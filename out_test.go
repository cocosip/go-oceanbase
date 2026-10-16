package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type failingOutScanner struct{ calls int }

func (s *failingOutScanner) Scan(any) error { s.calls++; return errors.New("scanner rejected output") }

func TestOutValidation(t *testing.T) {
	var text string
	var number int64 = 42
	var nilInt *int
	for _, arg := range []any{sql.Out{Dest: &text}, sql.Out{Dest: &number, In: true}} {
		if _, err := convertArgument(arg); err != nil {
			t.Fatalf("valid output rejected: %v", err)
		}
	}
	for _, arg := range []any{sql.Out{}, sql.Out{Dest: nilInt}, sql.Out{Dest: number}, sql.Out{Dest: new(map[string]int)}, sql.Out{Dest: new(*sql.Rows)}, sql.Out{Dest: &failingOutScanner{}, In: true}} {
		if _, err := convertArgument(arg); err == nil {
			t.Fatal("invalid output accepted")
		}
	}
}

func outOracleDB(t *testing.T, prepare PrepareMode, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	c, err := NewConnector(Config{User: testUser, Mode: ModeOracle, PrepareMode: prepare, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if serveConnect(ctx, t, p, 6) && serveBaseline(ctx, t, p, 6) {
			serve(context.Background(), p)
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(c)
}

func combinedHeader(id uint32, columns, params uint16, flags byte) []byte {
	b := binary.LittleEndian.AppendUint32([]byte{0}, id)
	b = binary.LittleEndian.AppendUint16(b, columns)
	b = binary.LittleEndian.AppendUint16(b, params)
	return append(b, 0, 0, 0, flags, 0, 0, 0, 1)
}

func TestOutReturningProtocol(t *testing.T) {
	for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
		for _, tc := range []struct {
			tailError bool
			flags     byte
		}{{false, 1}, {true, 1}, {false, 3}, {true, 3}} {
			tailError := tc.tailError
			t.Run(fmt.Sprint(prepare)+"/flags="+fmt.Sprint(tc.flags)+"/tailError="+strconv.FormatBool(tailError), func(t *testing.T) {
				db := outOracleDB(t, prepare, func(ctx context.Context, p *wire.PacketIO) {
					want, err := wire.EncodePrepareExecute(0, []byte("INSERT INTO t VALUES(?) RETURNING id INTO ?"), []wire.Parameter{{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 42)}, {Type: 6}}, 4096)
					if err != nil || !readCommand(ctx, t, p, want) {
						return
					}
					if !sendPackets(ctx, t, p, combinedHeader(9, 1, 2, tc.flags), textColumn("p", 8, 63, 0), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 43), []byte{254, 0, 0, 6, 0}) {
						return
					}
					tail := []byte{0, 1, 0, 6, 0, 0, 0}
					if tailError {
						tail = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
					}
					if !sendPackets(ctx, t, p, tail) || !readCommand(ctx, t, p, stmtPacket(25, 9)) || !serveOK(ctx, t, p, []byte{14}, 6) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				value := int64(7)
				result, err := db.ExecContext(context.Background(), "INSERT INTO t VALUES(:v) RETURNING id INTO :id", sql.Named("v", 42), sql.Named("id", sql.Out{Dest: &value}))
				if tailError {
					var server *Error
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

func TestOutMySQLSessionFinalization(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
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
		if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !readQuery(ctx, t, p, "CALL p( "+vars[0]+" , "+vars[1]+" )") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("ordinary", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '9'}, []byte{254, 0, 0, 10, 0}, []byte{0, 2, 0, 2, 0, 0, 0}) || !readQuery(ctx, t, p, "SELECT "+strings.Join(vars, ",")) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{2}, textColumn("a", 8, 63, 0), textColumn("b", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2', 2, 'o', 'k'}, []byte{254, 0, 0, 2, 0}) || !readQuery(ctx, t, p, "SET "+vars[0]+"=NULL,"+vars[1]+"=NULL") || !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	a, b := int64(41), testOutBefore
	e, err := Execute(context.Background(), pinned, "CALL p(?,?)", sql.Out{Dest: &a, In: true}, sql.Out{Dest: &b})
	if err != nil || a != 41 || b != testOutBefore {
		t.Fatal(a, b, err)
	}
	if err = e.Close(); err != nil || a != 42 || b != "ok" {
		t.Fatal(a, b, err)
	}
	snapshot := e.Snapshot()
	if len(snapshot.Results) != 2 || snapshot.Results[0].Kind != ResultRows || snapshot.Results[1].RowsAffected != 2 || !snapshot.Done {
		t.Fatal("helper results leaked", snapshot)
	}
	if err = pinned.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOutFinalization(t *testing.T) {
	c := &conn{cfg: Config{Session: SessionOptions{Location: time.UTC}, Limits: Limits{MaxMessageBytes: 1024, MaxLOBBytes: 1024}}}
	first := int64(7)
	second := int8(8)
	a, err := makeOutput(Out{Dest: &first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := makeOutput(Out{Dest: &second})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.assignOutputs([]*outBinding{a, b}, []driver.Value{int64(1), int64(128)}); err == nil || first != 7 || second != 8 {
		t.Fatalf("overflow partly updated outputs: %d %d %v", first, second, err)
	}
	if err = c.assignOutputs([]*outBinding{a}, []driver.Value{nil}); err == nil || first != 7 {
		t.Fatal("NULL became a zero value", first, err)
	}
	var nullable sql.Null[int64]
	n, err := makeOutput(Out{Dest: &nullable})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.assignOutputs([]*outBinding{a, n}, []driver.Value{int64(9), nil}); err != nil || first != 9 || nullable.Valid {
		t.Fatal(first, nullable, err)
	}
	scanner := &failingOutScanner{}
	s, err := makeOutput(Out{Dest: scanner, Type: typeNameVarchar})
	if err != nil {
		t.Fatal(err)
	}
	err = c.assignOutputs([]*outBinding{a, s}, []driver.Value{int64(10), "rejected"})
	var failure *OutError
	if !errors.As(err, &failure) || !failure.Partial || failure.Parameter != 2 || first != 10 || scanner.calls != 1 {
		t.Fatal(first, scanner.calls, err)
	}
}

func TestOutDeclaredLength(t *testing.T) {
	c := &conn{cfg: Config{Limits: Limits{MaxMessageBytes: 1024}}}
	var binaryValue []byte
	binaryOut, err := makeOutput(Out{Dest: &binaryValue, Type: typeNameVarbinary, Length: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.assignOutputs([]*outBinding{binaryOut}, []driver.Value{"中"}); err == nil || binaryValue != nil {
		t.Fatal("binary bound counted characters", binaryValue, err)
	}
	var textValue string
	textOut, err := makeOutput(Out{Dest: &textValue, Type: typeNameVarchar, Length: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.assignOutputs([]*outBinding{textOut}, []driver.Value{[]byte("中")}); err != nil || textValue != "中" {
		t.Fatal("text bound counted bytes", textValue, err)
	}
}
