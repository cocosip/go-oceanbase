package routing

import (
	"bytes"
	"net"
	"strconv"
	"strings"
	"unicode"
)

const discoveryLimit = 1 << 20
const addressListKey = "ADDRESS_LIST"
const addressKey = "ADDRESS"

type descriptorNode struct {
	key, value string
	children   []descriptorNode
}

type tnsParser struct {
	input      string
	pos, nodes int
}

func (p *tnsParser) space() {
	for p.pos < len(p.input) {
		c := p.input[p.pos]
		if c == '#' {
			for p.pos < len(p.input) && p.input[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		if !strings.ContainsRune(" \t\r\n", rune(c)) {
			break
		}
		p.pos++
	}
}

func (p *tnsParser) node(depth int, wrapped bool) (descriptorNode, error) {
	n := descriptorNode{}
	p.nodes++
	if depth > 16 || p.nodes > 8192 {
		return n, ErrInvalid
	}
	p.space()
	if wrapped {
		if p.pos >= len(p.input) || p.input[p.pos] != '(' {
			return n, ErrInvalid
		}
		p.pos++
		p.space()
	}
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != '=' {
		if strings.ContainsRune("()#\"'", rune(p.input[p.pos])) {
			return n, ErrInvalid
		}
		p.pos++
	}
	if p.pos == len(p.input) {
		return n, ErrInvalid
	}
	n.key = strings.ToUpper(strings.TrimSpace(p.input[start:p.pos]))
	if !validName(n.key) {
		return n, ErrInvalid
	}
	p.pos++
	p.space()
	if p.pos < len(p.input) && p.input[p.pos] == '(' {
		for p.pos < len(p.input) && p.input[p.pos] == '(' {
			child, err := p.node(depth+1, true)
			if err != nil {
				return n, err
			}
			n.children = append(n.children, child)
			p.space()
		}
	} else {
		start = p.pos
		for p.pos < len(p.input) && p.input[p.pos] != ')' {
			if strings.ContainsRune("(=#\"'", rune(p.input[p.pos])) {
				return n, ErrInvalid
			}
			p.pos++
		}
		n.value = strings.TrimSpace(p.input[start:p.pos])
		if n.value == "" || len(n.value) > 1024 || strings.IndexFunc(n.value, unicode.IsControl) >= 0 {
			return n, ErrInvalid
		}
	}
	if wrapped {
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return n, ErrInvalid
		}
		p.pos++
	}
	return n, nil
}

func validName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-", c) {
			return false
		}
	}
	return true
}

// ParseTNS accepts an inline DESCRIPTION, alias=DESCRIPTION, or Java's wrapped alias.
// Routing policy hints are validated but selection policy belongs to the Connector.
func ParseTNS(data []byte, alias string) (Snapshot, error) {
	if len(data) == 0 || len(data) > discoveryLimit || !bytes.Equal(bytes.ToValidUTF8(data, nil), data) {
		return Snapshot{}, ErrInvalid
	}
	p := tnsParser{input: string(data)}
	var found *descriptorNode
	aliases := map[string]bool{}
	for {
		p.space()
		if p.pos == len(p.input) {
			break
		}
		n, err := p.node(0, p.input[p.pos] == '(')
		if err != nil {
			return Snapshot{}, err
		}
		if n.key == "DESCRIPTION" {
			if alias != "" || found != nil {
				return Snapshot{}, ErrInvalid
			}
			found = &n
		} else {
			if aliases[n.key] || len(n.children) != 1 || n.children[0].key != "DESCRIPTION" {
				return Snapshot{}, ErrInvalid
			}
			aliases[n.key] = true
			if strings.EqualFold(n.key, alias) {
				if found != nil {
					return Snapshot{}, ErrInvalid
				}
				found = &n.children[0]
			}
		}
	}
	if found == nil {
		return Snapshot{}, ErrInvalid
	}
	return descriptionSnapshot(found.children, "")
}

func descriptionSnapshot(nodes []descriptorNode, identity string) (Snapshot, error) {
	s := Snapshot{}
	seen := map[string]bool{}
	group := uint16(0)
	for _, n := range nodes {
		if n.key != addressListKey && n.key != addressKey && seen[n.key] {
			return s, ErrInvalid
		}
		seen[n.key] = true
		switch n.key {
		case addressListKey:
			count := 0
			policies := map[string]bool{}
			for _, a := range n.children {
				if a.key != addressKey {
					if policies[a.key] || !validPolicy(a) {
						return s, ErrInvalid
					}
					policies[a.key] = true
					continue
				}
				e, err := descriptorEndpoint(a.children)
				if err != nil {
					return s, err
				}
				e.Group = group
				s.Endpoints = append(s.Endpoints, e)
				count++
			}
			if count == 0 || len(s.Endpoints) > 1024 {
				return s, ErrInvalid
			}
			group++
		case addressKey:
			// Bare ADDRESS entries are alternates within one priority group.
			e, err := descriptorEndpoint(n.children)
			if err != nil {
				return s, err
			}
			e.Group = group
			s.Endpoints = append(s.Endpoints, e)
			if len(s.Endpoints) > 1024 {
				return s, ErrInvalid
			}
		case "CONNECT_DATA":
			if len(n.children) != 1 || n.children[0].key != "SERVICE_NAME" || !validService(n.children[0].value) {
				return s, ErrInvalid
			}
			s.Service = n.children[0].value
		default:
			if !validPolicy(n) {
				return s, ErrInvalid
			}
		}
	}
	if identity != "" {
		s.Service = identity
	}
	return validateSnapshot(s, s.Service, 0)
}

func descriptorEndpoint(nodes []descriptorNode) (DiscoveredEndpoint, error) {
	e := DiscoveredEndpoint{}
	values := map[string]string{}
	for _, n := range nodes {
		if len(n.children) != 0 || values[n.key] != "" || n.value == "" {
			return e, ErrInvalid
		}
		values[n.key] = n.value
	}
	port := "2881"
	for key, v := range values {
		switch key {
		case "HOST":
		case "PORT":
			port = v
		case "PROTOCOL":
			if !strings.EqualFold(v, "tcp") {
				return e, ErrInvalid
			}
		case "WEIGHT":
			n, err := strconv.ParseUint(v, 10, 16)
			if err != nil {
				return e, ErrInvalid
			}
			e.Weight = uint16(n)
		case "TYPE":
			switch strings.ToLower(v) {
			case "master", "readwrite":
				e.Role = 1
			case "slave", "readonly":
				e.Role = 2
			default:
				return e, ErrInvalid
			}
		default:
			return e, ErrInvalid
		}
	}
	if values["HOST"] == "" {
		return e, ErrInvalid
	}
	addr, err := NormalizeAddress(net.JoinHostPort(values["HOST"], port))
	e.Addr = addr
	return e, err
}

func validPolicy(n descriptorNode) bool {
	if len(n.children) != 0 {
		if n.key != "OBLB_BLACKLIST" || len(n.children) > 2 {
			return false
		}
		seen := map[string]bool{}
		for _, strategy := range n.children {
			if seen[strategy.key] || (strategy.key != "REMOVE_STRATEGY" && strategy.key != "APPEND_STRATEGY") || len(strategy.children) == 0 {
				return false
			}
			seen[strategy.key] = true
			fields := map[string]bool{}
			for _, f := range strategy.children {
				if fields[f.key] || len(f.children) > 0 {
					return false
				}
				fields[f.key] = true
				switch f.key {
				case "NAME":
					if f.value != "TIMEOUT" && f.value != "RETRYDURATION" && f.value != "NORMAL" {
						return false
					}
				case "TIMEOUT", "RETRYTIMES", "DURATION":
					if _, err := strconv.ParseUint(f.value, 10, 32); err != nil {
						return false
					}
				default:
					return false
				}
			}
		}
		return true
	}
	switch n.key {
	case "OBLB", "LOAD_BALANCE":
		return strings.EqualFold(n.value, "ON") || strings.EqualFold(n.value, "OFF")
	case "OBLB_STRATEGY", "LOAD_BALANCE_STRATEGY", "OBLB_GROUP_STRATEGY":
		switch strings.ToUpper(n.value) {
		case "RANDOM", "ROTATION", "SERVERAFFINITY", "BALANCE", "ORDERED":
			return true
		}
	case "OBLB_RETRY_ALL_DOWNS":
		_, err := strconv.ParseUint(n.value, 10, 32)
		return err == nil
	}
	return false
}
