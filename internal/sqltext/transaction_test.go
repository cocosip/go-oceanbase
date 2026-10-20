package sqltext

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestTransactionGrammar(t *testing.T) {
	for _, tc := range []struct {
		mode    config.Mode
		options driver.TxOptions
		want    []string
	}{
		{config.ModeMySQL, driver.TxOptions{}, []string{"START TRANSACTION READ WRITE"}},
		{config.ModeMySQL, driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelRepeatableRead), ReadOnly: true}, []string{"SET TRANSACTION ISOLATION LEVEL REPEATABLE READ", "START TRANSACTION READ ONLY"}},
		{config.ModeOracle, driver.TxOptions{}, []string{testDisableAutocommit, "SET TRANSACTION READ WRITE"}},
		{config.ModeOracle, driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSerializable)}, []string{testDisableAutocommit, "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"}},
		{config.ModeOracle, driver.TxOptions{ReadOnly: true}, []string{testDisableAutocommit, "SET TRANSACTION READ ONLY"}},
	} {
		got, err := TransactionCommands(tc.mode, tc.options)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatal(got, err)
		}
	}
	for _, level := range []sql.IsolationLevel{sql.LevelReadUncommitted, sql.LevelWriteCommitted, sql.LevelSnapshot, sql.LevelLinearizable} {
		if _, err := IsolationSQL(driver.IsolationLevel(level)); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal(err)
		}
	}
	if _, err := TransactionCommands(config.ModeOracle, driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelReadCommitted), ReadOnly: true}); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
}

const testDisableAutocommit = "SET autocommit=0"
