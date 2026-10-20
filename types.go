package oceanbase

import (
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	types "github.com/cocosip/go-oceanbase/types"
)

// ErrType indicates an invalid scalar representation or conversion.
var ErrType = dberrors.ErrType

// NString binds an Oracle national character string.
type NString = types.NString

// Raw binds Oracle RAW data; nil and empty have distinct values.
type Raw = types.Raw

// RowID binds an Oracle row identifier.
type RowID = types.RowID

// ErrComplex identifies invalid complex values, descriptions or representations.
var ErrComplex = dberrors.ErrComplex

// Attribute is an ordered object field.
type Attribute = types.Attribute

// Object is an explicitly named Oracle compatibility type.
type Object = types.Object

// Array is a named Oracle compatibility collection.
type Array = types.Array

// Number preserves decimal text without floating point conversion.
type Number = types.Number

// TimestampTZ retains the public value type.
type TimestampTZ = types.TimestampTZ

// TimestampLTZ retains the public value type.
type TimestampLTZ = types.TimestampLTZ

// IntervalYM retains the public value type.
type IntervalYM = types.IntervalYM

// IntervalDS retains the public value type.
type IntervalDS = types.IntervalDS
