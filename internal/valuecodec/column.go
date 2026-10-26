package valuecodec

import (
	"database/sql"
	"reflect"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// DecodeColumn decodes column names using the explicitly negotiated session charset.
func DecodeColumn(data []byte, options config.SessionOptions, newMetadata bool) (wire.Column, error) {
	column, err := wire.DecodeColumn(data)
	if err != nil {
		return column, err
	}
	column.NewMetadata = newMetadata
	name := options.Charset
	if name == "" {
		name = charsetUTF8MB4
	}
	for _, field := range []*string{&column.Catalog, &column.Schema, &column.Table, &column.OriginalTable, &column.Name, &column.OriginalName, &column.ComplexSchema, &column.ComplexName, &column.ElementSchema, &column.ElementName} {
		*field, err = charset.Decode(name, []byte(*field))
		if err != nil {
			return wire.Column{}, err
		}
	}
	return column, nil
}

// ColumnLength reports the protocol-defined column length in bytes or characters.
func ColumnLength(c wire.Column, mode config.Mode) (int64, bool) {
	if c.NewMetadata && mode == config.ModeOracle {
		switch c.Type {
		case 203, 209:
			return int64(c.Precision), c.Precision != 0
		case 210, 211:
			return 0, false
		}
	}
	if c.Length == 0 {
		return 0, false
	}
	if c.NewMetadata && mode == config.ModeOracle && (c.Type == 207 || c.Type == 208) {
		return int64(c.Length), true
	}
	if c.Type == 203 || c.Type == 16 || c.Type >= 249 && c.Type <= 254 && c.Charset == 63 {
		return int64(c.Length), true
	}
	switch c.Type {
	case 15, 253, 254, 207, 208, 249, 250, 251, 252:
		if mode == config.ModeOracle && (c.Type == 15 || c.Type == 253 || c.Type == 254) {
			return int64(c.Length), true
		}
		name, ok := charset.Collation(c.Charset)
		// HKSCS metadata uses the server's mbmaxlen=2 despite its UTF-8 wire payload.
		width := map[string]uint32{"binary": 1, charsetASCII: 1, charsetLatin1: 1, charsetGBK: 2, "hkscs": 2, "hkscs31": 2, charsetUTF8MB3: 3, charsetUTF8MB4: 4, charsetUTF16: 4, charsetGB18030: 4, "gb18030_2022": 4}[name]
		if !ok || width == 0 || c.Length%width != 0 {
			return 0, false
		}
		return int64(c.Length / width), true
	}
	return 0, false
}

// ColumnPrecisionScale reports decimal precision and scale for the compatibility mode.
func ColumnPrecisionScale(c wire.Column, mode config.Mode) (int64, int64, bool) {
	if c.Type != 0 && c.Type != 246 && (mode != config.ModeOracle || c.Type != 3) {
		return 0, 0, false
	}
	if mode == config.ModeOracle {
		if c.Precision == 0 || c.Precision > 38 || int8(c.Decimals) < -84 {
			return 0, 0, false
		}
		return int64(c.Precision), int64(int8(c.Decimals)), true
	}
	if c.Decimals > 30 {
		return 0, 0, false
	}
	p := int64(c.Length)
	if c.Flags&32 == 0 {
		p--
	}
	if c.Decimals > 0 {
		p--
	}
	if p <= 0 || p > 65 || p < int64(c.Decimals) {
		return 0, 0, false
	}
	return p, int64(c.Decimals), true
}

// ColumnScanType reports the standard scan destination type for a column.
func ColumnScanType(c wire.Column, mode config.Mode, options config.SessionOptions) reflect.Type {
	if c.Type == 163 && mode == config.ModeOracle {
		return reflect.TypeFor[sql.Rows]()
	}
	if c.Type == 210 && mode == config.ModeOracle {
		return reflect.TypeFor[[]byte]()
	}
	if options.ParseTime && (c.Type == 7 || c.Type == 10 || c.Type == 12 || c.Type == 14 || c.Type >= 200 && c.Type <= 202 && mode == config.ModeOracle) {
		return reflect.TypeFor[time.Time]()
	}
	if c.Charset == 63 && (c.Type == 15 || c.Type >= 249) || c.Type == 16 || c.Type == 203 {
		return reflect.TypeFor[[]byte]()
	}
	switch c.Type {
	case 8:
		if c.Flags&32 != 0 {
			return reflect.TypeFor[uint64]()
		}
		return reflect.TypeFor[int64]()
	case 1, 2, 9, 13:
		return reflect.TypeFor[int64]()
	case 3:
		if mode != config.ModeOracle {
			return reflect.TypeFor[int64]()
		}
	case 4, 5:
		return reflect.TypeFor[float64]()
	}
	return reflect.TypeFor[string]()
}
