package catalog

import (
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// StatementParameters projects authoritative preparation metadata without inferring directions.
func StatementParameters(mode config.Mode, plan sqltext.Plan, parameters []wire.Column) [][]driver.Value {
	values := make([][]driver.Value, len(parameters))
	for i, column := range parameters {
		slot := plan.Slots[i]
		row := []driver.Value{int64(i + 1), int64(slot.Index + 1), nil, nil, nil, nil, nil, nil, nil, int64(column.Type), int64(column.InOut)}
		if slot.Name != "" {
			row[2] = slot.Name
		}
		if column.Type != 6 {
			if name := valuecodec.TextTypeName(column, mode); name != "" {
				row[4] = name
			}
		}
		if mode == config.ModeOracle {
			row[5] = int64(0)
			if column.Flags&1 == 0 {
				row[5] = int64(1)
			}
		}
		if n, ok := valuecodec.ColumnLength(column, mode); ok {
			row[6] = n
		}
		if precision, scale, ok := valuecodec.ColumnPrecisionScale(column, mode); ok {
			row[7], row[8] = precision, scale
		}
		values[i] = row
	}
	return values
}
