package oceanbase

const (
	transactionReadOnlyWrite  = "UPDATE readonly_target SET value=1"
	transactionPendingWrite   = "UPDATE pending SET value=1"
	transactionMySQLReadOnly  = "START TRANSACTION READ ONLY"
	transactionOracleReadOnly = "SET TRANSACTION READ ONLY"
	transactionUTF8Names      = "SET NAMES utf8mb4"
	transactionUTCZone        = "SET SESSION time_zone = '+00:00'"
	transactionMySQLLabel     = "mysql"
	transactionOracleLabel    = "oracle"
)
