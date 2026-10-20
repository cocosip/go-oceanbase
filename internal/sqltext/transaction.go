package sqltext

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"strings"
	"unicode/utf8"
)

// TransactionCommands builds transaction-start SQL in the original command order.
func TransactionCommands(mode config.Mode, opts driver.TxOptions) ([]string, error) {
	level, err := IsolationSQL(opts.Isolation)
	if err != nil {
		return nil, err
	}
	if mode == config.ModeOracle {
		iso := sql.IsolationLevel(opts.Isolation)
		if iso != sql.LevelDefault && iso != sql.LevelReadCommitted && iso != sql.LevelSerializable || opts.ReadOnly && iso == sql.LevelReadCommitted {
			return nil, fmt.Errorf("%w: Oracle transaction options", dberrors.ErrUnsupported)
		}
		command := "SET TRANSACTION READ WRITE"
		if opts.ReadOnly {
			command = "SET TRANSACTION READ ONLY"
		} else if level != "" {
			command = "SET TRANSACTION ISOLATION LEVEL " + level
		}
		return []string{"SET autocommit=0", command}, nil
	}
	commands := []string{}
	if level != "" {
		commands = append(commands, "SET TRANSACTION ISOLATION LEVEL "+level)
	}
	access := "READ WRITE"
	if opts.ReadOnly {
		access = "READ ONLY"
	}
	return append(commands, "START TRANSACTION "+access), nil
}

// IsolationSQL maps a supported isolation level to SQL.
func IsolationSQL(level driver.IsolationLevel) (string, error) {
	switch sql.IsolationLevel(level) {
	case sql.LevelDefault:
		return "", nil
	case sql.LevelReadCommitted:
		return "READ COMMITTED", nil
	case sql.LevelRepeatableRead:
		return "REPEATABLE READ", nil
	case sql.LevelSerializable:
		return "SERIALIZABLE", nil
	default:
		return "", fmt.Errorf("%w: transaction isolation", dberrors.ErrUnsupported)
	}
}

// ValidSavepointName checks the public savepoint name grammar.
func ValidSavepointName(name string) bool {
	return name != "" && len(name) <= 256 && !strings.ContainsRune(name, 0) && utf8.ValidString(name)
}
