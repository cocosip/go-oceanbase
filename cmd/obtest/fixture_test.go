package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const fixtureDroppedState = "dropped"
const fixtureDropTableSQL = "DROP TABLE OBT_OFFLINE_T"

type fixtureStep struct {
	query    string
	values   [][]driver.Value
	affected int64
	err      error
	inspect  func([]driver.NamedValue)
	optional bool
}

type fixtureDB struct {
	t     *testing.T
	steps []fixtureStep
}

func (f *fixtureDB) Connect(context.Context) (driver.Conn, error) { return &fixtureConn{f}, nil }
func (*fixtureDB) Driver() driver.Driver                          { return fixtureDriver{} }

type fixtureDriver struct{}

func (fixtureDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fixture requires connector")
}

type fixtureConn struct{ fixture *fixtureDB }

func (*fixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (f *fixtureConn) Begin() (driver.Tx, error) {
	return f.BeginTx(context.Background(), driver.TxOptions{})
}

func (f *fixtureConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	_, err := f.next(ctx, "BEGIN")
	return fixtureTx{f}, err
}

type fixtureTx struct{ conn *fixtureConn }

func (tx fixtureTx) Commit() error {
	_, err := tx.conn.next(context.Background(), "COMMIT")
	return err
}

func (tx fixtureTx) Rollback() error {
	_, err := tx.conn.next(context.Background(), "ROLLBACK")
	return err
}

func (*fixtureConn) Close() error                             { return nil }
func (*fixtureConn) CheckNamedValue(*driver.NamedValue) error { return nil }
func (f *fixtureConn) next(ctx context.Context, query string) (fixtureStep, error) {
	if err := ctx.Err(); err != nil {
		return fixtureStep{}, err
	}
	for len(f.fixture.steps) != 0 && f.fixture.steps[0].optional && f.fixture.steps[0].query != query {
		f.fixture.steps = f.fixture.steps[1:]
	}
	if len(f.fixture.steps) == 0 {
		f.fixture.t.Errorf("unexpected SQL: %s", query)
		return fixtureStep{}, errors.New("unexpected operation")
	}
	step := f.fixture.steps[0]
	f.fixture.steps = f.fixture.steps[1:]
	if query != step.query {
		f.fixture.t.Errorf("SQL=%q want %q", query, step.query)
		return step, errors.New("operation order mismatch")
	}
	return step, step.err
}
func (f *fixtureConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	step, err := f.next(ctx, query)
	if step.inspect != nil {
		step.inspect(args)
	}
	return driver.RowsAffected(step.affected), err
}

func TestOwnedXACleanup(t *testing.T) {
	for _, state := range []string{xaActiveState, "suspended", xaEndedState, "prepared"} {
		t.Run(state, func(t *testing.T) {
			var flags []int64
			recordFlag := func(args []driver.NamedValue) {
				if len(args) != 1 {
					t.Error("missing XA request")
					return
				}
				flags = append(flags, reflect.ValueOf(args[0].Value).Elem().FieldByName("flags").Int())
			}
			want := []int64{0}
			if state == xaActiveState {
				want = []int64{int64(oceanbase.XAFail), 0}
			}
			if state == "suspended" {
				want = []int64{int64(oceanbase.XAResume), int64(oceanbase.XAFail), 0}
			}
			steps := make([]fixtureStep, len(want))
			for i := range steps {
				steps[i].inspect = recordFlag
			}
			s := fixtureScenario(t, steps)
			c, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			record := &objectRecord{State: state}
			xid := oceanbase.XID{FormatID: 42, GlobalID: []byte("owned")}
			if err = cleanupOwnedXA(context.Background(), s.db, &c, xid, record); err != nil {
				t.Fatal(err)
			}
			if c != nil || record.State != xaCleanedState || !reflect.DeepEqual(flags, want) {
				t.Fatalf("cleanup=%s flags=%v", record.State, flags)
			}
		})
	}
}

func TestCancellationRejectsUnrelatedErrors(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		code := uint32(1317)
		if mode == oceanbase.ModeOracle {
			code = 1013
		}
		if err := checkCancellation(&oceanbase.Error{Code: code}, mode, 0); err != nil {
			t.Fatal(err)
		}
		for _, outcome := range []error{nil, context.DeadlineExceeded, &oceanbase.Error{Code: 1205}, &oceanbase.Error{Code: 1064}} {
			if err := checkCancellation(outcome, mode, 0); err == nil {
				t.Fatalf("unrelated outcome accepted: %T", outcome)
			}
		}
		if err := checkCancellation(&oceanbase.Error{Code: 777}, mode, 777); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *fixtureConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := f.next(ctx, query)
	return &fixtureRows{values: step.values}, err
}

type fixtureRows struct{ values [][]driver.Value }

func (r *fixtureRows) Columns() []string {
	n := 1
	if len(r.values) != 0 {
		n = len(r.values[0])
	}
	columns := make([]string, n)
	for i := range columns {
		columns[i] = "V"
	}
	return columns
}
func (*fixtureRows) Close() error { return nil }
func (r *fixtureRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

func fixtureScenario(t *testing.T, steps []fixtureStep) *scenario {
	t.Helper()
	f := &fixtureDB{t: t, steps: steps}
	db := sql.OpenDB(f)
	db.SetMaxOpenConns(1)
	s := &scenario{db: db, prefix: "OBT_OFFLINE", mode: oceanbase.ModeOracle, cleanupTimeout: time.Second}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if len(f.steps) != 0 {
			t.Errorf("%d operations not executed", len(f.steps))
		}
	})
	return s
}

func TestCRUDOfflineWorkflow(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong-content", true: "complete"}[valid], func(t *testing.T) {
			steps := []fixtureStep{
				{query: "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V VARCHAR(100))"},
				{query: "INSERT INTO OBT_OFFLINE_T VALUES (1,'seed')", affected: 1},
				{query: "UPDATE OBT_OFFLINE_T SET V='updated' WHERE ID=1", affected: 1},
				{query: "SELECT V FROM OBT_OFFLINE_T WHERE ID=1", values: [][]driver.Value{{"wrong"}}},
			}
			if valid {
				steps[3].values[0][0] = "updated"
				steps = append(steps,
					fixtureStep{query: "INSERT INTO OBT_OFFLINE_T VALUES (1,'duplicate')", err: &oceanbase.Error{Code: 1062, Message: "private"}},
					fixtureStep{query: "SELECT COUNT(*) FROM OBT_OFFLINE_T", values: [][]driver.Value{{int64(1)}}},
					fixtureStep{query: "DELETE FROM OBT_OFFLINE_T WHERE ID=1", affected: 1},
					fixtureStep{query: "SELECT V FROM OBT_OFFLINE_T WHERE ID=1"})
			}
			steps = append(steps, fixtureStep{query: fixtureDropTableSQL})
			s := fixtureScenario(t, steps)
			r := runScenario(context.Background(), s, runCRUD)
			want := statusFail
			if valid {
				want = statusPass
			}
			if r.Status != want || len(r.Objects) != 1 || r.Objects[0].State != fixtureDroppedState {
				t.Fatalf("workflow result: %+v", r)
			}
		})
	}
}

func TestOnlyConfirmedObjectsAreDropped(t *testing.T) {
	s := fixtureScenario(t, []fixtureStep{
		{query: "CREATE TYPE OBT_OFFLINE_O AS OBJECT(N NUMBER)"},
		{query: "CREATE TABLE OBT_OFFLINE_T (V OBT_OFFLINE_O)", err: errors.New("connection failed after send")},
		{query: "DROP TYPE OBT_OFFLINE_O"},
	})
	r := runScenario(context.Background(), s, func(ctx context.Context, s *scenario) error {
		if _, err := s.create(ctx, "TYPE", "O", "AS OBJECT(N NUMBER)"); err != nil {
			return err
		}
		_, err := s.table(ctx, "(V OBT_OFFLINE_O)")
		return err
	})
	if r.Status != statusFail || len(r.Objects) != 2 || r.Objects[0].State != fixtureDroppedState || r.Objects[1].State != "create_unconfirmed" {
		t.Fatalf("incorrect ownership/cleanup: %+v", r)
	}
}

func TestCLIConfigurationOnly(t *testing.T) {
	const secret = "do-not-print-this-password"
	t.Setenv("OB_DSN", "oceanbase://fixture:"+secret+"@127.0.0.1:1/test?mode=mysql")
	for _, args := range [][]string{{"-list"}, {"-check"}} {
		var out, stderr strings.Builder
		if code := command(args, &out, &stderr); code != 0 || strings.Contains(out.String()+stderr.String(), secret) {
			t.Fatalf("offline command: %d %s %s", code, &out, &stderr)
		}
	}
	defs := caseDefinitions()
	names := map[string]bool{}
	for _, d := range defs {
		if d.name == "" || d.run == nil || names[d.name] {
			t.Fatal("invalid case registry")
		}
		names[d.name] = true
	}
	cfg, err := readConfig(strings.NewReader(`{"targets":[{"name":"test","dsn_env":"OB_DSN"}],"cases":["crud","transactions"]}`))
	if err != nil || !reflect.DeepEqual(cfg.Cases, []string{"crud", "transactions"}) {
		t.Fatal("valid selection was not preserved", err)
	}
}
