package oceanbase

import "github.com/cocosip/go-oceanbase/internal/sqltext"

// RewriteJDBCEscapes rewrites supported JDBC escapes without executing SQL.
func RewriteJDBCEscapes(query string, mode Mode) (string, error) {
	return sqltext.RewriteJDBCEscapes(query, mode)
}
