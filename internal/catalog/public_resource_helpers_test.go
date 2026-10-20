package catalog_test

import (
	driver "database/sql/driver"
	atomic "sync/atomic"
	_ "time/tzdata"
)

type tableKeyValuer struct {
	calls atomic.Int32
	value []byte
}

func (v *tableKeyValuer) Value() (driver.Value, error) { v.calls.Add(1); return v.value, nil }
