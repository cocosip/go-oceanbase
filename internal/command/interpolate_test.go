package command

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestInterpolateSharedBudget(t *testing.T) {
	cfg := config.Config{Limits: config.Limits{MaxMessageBytes: 1000, MaxLOBBytes: 1000}, Session: config.SessionOptions{Charset: "utf8mb4"}}
	s := session.New(cfg, &session.Negotiated{Mode: config.ModeOracle})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { op.Release() }()
	query, err := Interpolate(op, "SELECT :a,:A FROM dual", []driver.NamedValue{{Name: "a", Value: "a'b"}})
	if err != nil || string(query) != "SELECT  'a''b' , 'a''b'  FROM dual" {
		t.Fatal(string(query), err)
	}
	if _, err = Interpolate(op, "SELECT :a FROM dual", []driver.NamedValue{{Value: 1}}); !errors.Is(err, dberrors.ErrBind) {
		t.Fatal(err)
	}
	query, err = Interpolate(op, "SELECT ?-? FROM dual", []driver.NamedValue{{Value: int64(-1)}, {Value: valuecodec.OutputVariable("@out")}})
	if err != nil || string(query) != "SELECT  -1 - @out  FROM dual" {
		t.Fatal(string(query), err)
	}
	cfg.Limits.MaxMessageBytes = 10
	s = session.New(cfg, &session.Negotiated{Mode: config.ModeOracle})
	small, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { small.Release() }()
	if _, err = Interpolate(small, "SELECT ?", []driver.NamedValue{{Value: "abc"}}); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
}
