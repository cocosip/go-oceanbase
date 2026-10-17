package wire

// DecodeSessionVariables bounds and parses the length-delimited system-variable state records.
func DecodeSessionVariables(data []byte) (map[string]string, error) {
	vars := make(map[string]string)
	r := decoder{data: data}
	for r.remaining() > 0 && r.err == nil {
		kind := r.u8()
		body := r.lenBytes()
		if kind != 0 {
			continue
		}
		entry := decoder{data: body}
		for entry.remaining() > 0 && entry.err == nil {
			name, value := entry.lenBytes(), entry.lenBytes()
			if entry.err != nil {
				return nil, ErrMalformed
			}
			if _, exists := vars[string(name)]; exists {
				return nil, ErrMalformed
			}
			vars[string(name)] = string(value)
		}
		if entry.err != nil {
			return nil, ErrMalformed
		}
	}
	return vars, r.err
}
