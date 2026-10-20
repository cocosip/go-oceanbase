package valueformat

import "encoding/json"

// ComplexJSONField retains one ordered format-1 object field.
type ComplexJSONField struct {
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// ComplexJSON is the lossless format-1 representation shared with materialization.
type ComplexJSON struct {
	Format         int                `json:"format"`
	Kind           string             `json:"kind"`
	Schema         string             `json:"schema"`
	Type           string             `json:"type"`
	Version        uint64             `json:"version"`
	Null           bool               `json:"null"`
	Fields         []ComplexJSONField `json:"fields,omitempty"`
	ElementType    string             `json:"elementType,omitempty"`
	ElementVersion uint64             `json:"elementVersion,omitempty"`
	Elements       []json.RawMessage  `json:"elements,omitempty"`
}
