package cursor

import (
	"context"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/session"
)

func TestCursorInsertRejectsNullKey(t *testing.T) {
	id := &catalog.CursorIdentity{Table: "`s`.`t`", Keys: []string{"id"}, Columns: []string{"id", "b"}, Locking: true}
	s := session.New(config.Config{}, &session.Negotiated{Mode: config.ModeMySQL})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { op.Release() }()
	o := &operation{op: op, cur: &record{identity: id}}
	if _, _, err := o.mutationSQL(Request{Operation: Insert, Changes: []ColumnChange{{"id", []byte(nil)}, {"b", []byte("v")}}}, true); err == nil {
		t.Fatal("NULL primary key accepted before insert")
	}
}
