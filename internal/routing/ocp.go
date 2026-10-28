package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// FetchOCP reads the loadBalanceInfo response used by Connector/J.
// Redirects are refused, even when the supplied HTTP client allows them.
func FetchOCP(ctx context.Context, client *http.Client, endpoint string) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Snapshot{}, ErrInvalid
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, discoveryLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		return Snapshot{}, ErrUnavailable
	}
	return ParseOCP(data)
}

// ParseOCP accepts the OCP data/NET_SERVICE_NAME/DESCRIPTION envelope or a direct DESCRIPTION.
// Duplicate keys, malformed metadata and trailing content are refused in either form.
func ParseOCP(data []byte) (Snapshot, error) {
	if len(data) == 0 || len(data) > discoveryLimit || !bytes.Equal(bytes.ToValidUTF8(data, nil), data) {
		return Snapshot{}, ErrInvalid
	}
	check := json.NewDecoder(bytes.NewReader(data))
	check.UseNumber()
	nodes := 0
	if err := validateJSONValue(check, 0, &nodes); err != nil {
		return Snapshot{}, ErrInvalid
	}
	if _, err := check.Token(); !errors.Is(err, io.EOF) {
		return Snapshot{}, ErrInvalid
	}
	var envelope struct {
		Data       json.RawMessage `json:"data"`
		Successful *bool           `json:"successful"`
		Status     *int            `json:"status"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Snapshot{}, ErrInvalid
	}
	identity := ""
	if len(envelope.Data) != 0 {
		if envelope.Successful != nil && !*envelope.Successful || envelope.Status != nil && (*envelope.Status < 200 || *envelope.Status >= 300) {
			return Snapshot{}, ErrUnavailable
		}
		var service struct {
			Name        string          `json:"NET_SERVICE_NAME"`
			Description json.RawMessage `json:"DESCRIPTION"`
		}
		if err := json.Unmarshal(envelope.Data, &service); err != nil || !validService(service.Name) || len(service.Description) == 0 {
			return Snapshot{}, ErrInvalid
		}
		data, identity = service.Description, service.Name
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	nodes = 0
	children, err := jsonDescriptor(d, 0, &nodes)
	if err != nil {
		return Snapshot{}, ErrInvalid
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return Snapshot{}, ErrInvalid
	}
	return descriptionSnapshot(children, identity)
}

// Envelope metadata is opaque but obeys the same size, depth and duplicate-key limits.
func validateJSONValue(d *json.Decoder, depth int, nodes *int) error {
	(*nodes)++
	if depth > 16 || *nodes > 8192 {
		return ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			keyToken, e := d.Token()
			if e != nil {
				return e
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalid
			}
			key = strings.ToUpper(key)
			if seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if e = validateJSONValue(d, depth+1, nodes); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := validateJSONValue(d, depth+1, nodes); e != nil {
				return e
			}
		}
	default:
		return ErrInvalid
	}
	_, err = d.Token()
	return err
}

func jsonDescriptor(d *json.Decoder, depth int, nodes *int) ([]descriptorNode, error) {
	if depth > 16 {
		return nil, ErrInvalid
	}
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	var result []descriptorNode
	seen := map[string]bool{}
	for d.More() {
		keyToken, e := d.Token()
		if e != nil {
			return nil, e
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, ErrInvalid
		}
		key = strings.ToUpper(key)
		if !validName(key) || seen[key] {
			return nil, ErrInvalid
		}
		seen[key] = true
		(*nodes)++
		if *nodes > 8192 {
			return nil, ErrInvalid
		}
		n := descriptorNode{key: key}
		switch key {
		case addressListKey, addressKey:
			tok, e := d.Token()
			if e != nil || tok != json.Delim('[') {
				return nil, ErrInvalid
			}
			count := 0
			for d.More() {
				c, e := jsonDescriptor(d, depth+1, nodes)
				if e != nil {
					return nil, e
				}
				count++
				if count > 1024 {
					return nil, ErrInvalid
				}
				result = append(result, descriptorNode{key: key, children: c})
			}
			if _, e = d.Token(); e != nil || count == 0 {
				return nil, ErrInvalid
			}
			continue
		case "CONNECT_DATA", "OBLB_BLACKLIST", "APPEND_STRATEGY", "REMOVE_STRATEGY":
			n.children, err = jsonDescriptor(d, depth+1, nodes)
			if err != nil {
				return nil, err
			}
		default:
			v, e := d.Token()
			if e != nil {
				return nil, e
			}
			switch value := v.(type) {
			case string:
				n.value = value
			case json.Number:
				n.value = string(value)
			default:
				return nil, ErrInvalid
			}
			if n.value == "" || len(n.value) > 1024 {
				return nil, ErrInvalid
			}
		}
		result = append(result, n)
	}
	_, err = d.Token()
	return result, err
}
