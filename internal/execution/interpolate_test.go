package execution

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/types"
)

func interpolateConstructorTest(t *testing.T, mode config.Mode, query string, args []driver.NamedValue) error {
	t.Helper()
	cfg, err := config.Normalize(config.Config{User: "constructor-test", Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(cfg, &session.Negotiated{Mode: mode})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	plan, err := sqltext.BuildBindPlan(query, mode)
	if err != nil {
		return err
	}
	values, err := parameters.Bind(plan, args)
	if err != nil {
		return err
	}
	_, err = interpolateClient(context.Background(), op, plan, values)
	return err
}

func TestClientConstructorsRejectBeforeIO(t *testing.T) {
	value := types.Object{Schema: "S", Type: "T", Null: true}
	for _, query := range []string{
		"CREATE TYPE x AS OBJECT (n NUMBER); SELECT ? FROM dual",
		"SELECT ? FROM dual; /*x*/ ALTER TYPE T ADD ATTRIBUTE x NUMBER",
		"BEGIN EXECUTE IMMEDIATE 'DROP TYPE T'; p(?); END;",
	} {
		if err := interpolateConstructorTest(t, config.ModeOracle, query, []driver.NamedValue{{Value: value}}); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	if err := interpolateConstructorTest(t, config.ModeOracle, "SELECT ? FROM dual", []driver.NamedValue{{Value: types.Array{ElementType: valueformat.TypeNameNumber}}}); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := interpolateConstructorTest(t, config.ModeMySQL, "SELECT ?", []driver.NamedValue{{Value: value}}); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
}
