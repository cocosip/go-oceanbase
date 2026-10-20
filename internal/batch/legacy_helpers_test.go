package batch_test

import (
	"database/sql/driver"
	"encoding/binary"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/publictest"
)

const (
	testMaxUint64Text    = "18446744073709551615"
	testClientStrategy   = "client"
	localFilenameCharset = "gbk"
)

type countingValuer struct{ calls *int }

func (v countingValuer) Value() (driver.Value, error) { *v.calls++; return int64(42), nil }

func normalizeArgument(value any) (any, error) {
	for {
		next, again, err := parameters.NormalizeStep(value)
		if err != nil || !again {
			return next, err
		}
		value = next
	}
}

func batchSucceeded(result *oceanbase.BatchResult) int {
	n := 0
	for _, item := range result.Items {
		if item.Status == oceanbase.BatchSucceeded {
			n++
		}
	}
	return n
}

func longDataPacket(mode oceanbase.Mode, id uint32, index uint16, piece byte, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(publictest.StmtPacket(24, id), index)
	if mode == oceanbase.ModeOracle {
		b[0] = 162
		b = append(b, piece, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(data)))
	}
	return append(b, data...)
}
