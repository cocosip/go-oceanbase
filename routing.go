package oceanbase

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/internal/routing"
)

// EndpointRole labels the logical service exposed by an address, not SQL permissions.
type EndpointRole uint8

// Explicit roles never fall back to unlabelled or opposite-role endpoints.
const (
	RoleAny EndpointRole = iota
	RoleReadWrite
	RoleReadOnly
)

// BalanceStrategy orders members within ascending priority groups.
type BalanceStrategy uint8

// BalanceAffinity uses weighted random ordering without replacement, matching server affinity weights.
const (
	BalanceOrdered BalanceStrategy = iota
	BalanceRandom
	BalanceRoundRobin
	BalanceAffinity
)

// Endpoint describes a caller-configured address and its logical service role.
type Endpoint struct {
	Addr          string
	Role          EndpointRole
	Group, Weight uint16
}

// RoutingOptions selects new connections and optionally validates idle pooled sessions.
// Endpoints replaces Addr; zero MaxAttempts makes one pass over eligible endpoints.
type RoutingOptions struct {
	Endpoints          []Endpoint
	Role               EndpointRole
	Strategy           BalanceStrategy
	MaxAttempts        int
	AttemptTimeout     time.Duration
	RetryDelay         time.Duration
	BlacklistTimeout   time.Duration
	ValidationInterval time.Duration
}

func (o *RoutingOptions) applyDSNOption(name, value string) error {
	var err error
	switch name {
	case "addresses":
		if strings.Count(value, ",") >= 1024 {
			return &ConfigError{Field: name}
		}
		for _, addr := range strings.Split(value, ",") {
			o.Endpoints = append(o.Endpoints, Endpoint{Addr: addr})
		}
	case "role":
		switch value {
		case "any":
			o.Role = RoleAny
		case "readwrite":
			o.Role = RoleReadWrite
		case "readonly":
			o.Role = RoleReadOnly
		default:
			err = &ConfigError{Field: name}
		}
	case "balance":
		switch value {
		case "ordered":
			o.Strategy = BalanceOrdered
		case "random":
			o.Strategy = BalanceRandom
		case "roundrobin":
			o.Strategy = BalanceRoundRobin
		case "affinity":
			o.Strategy = BalanceAffinity
		default:
			err = &ConfigError{Field: name}
		}
	case "maxAttempts":
		o.MaxAttempts, err = strconv.Atoi(value)
	case "attemptTimeout":
		o.AttemptTimeout, err = time.ParseDuration(value)
	case "retryDelay":
		o.RetryDelay, err = time.ParseDuration(value)
	case "blacklistTimeout":
		o.BlacklistTimeout, err = time.ParseDuration(value)
	case "validationInterval":
		o.ValidationInterval, err = time.ParseDuration(value)
	}
	if err != nil {
		return &ConfigError{Field: name}
	}
	return nil
}

func (cfg *Config) normalizeRouting() error {
	o := &cfg.Routing
	if o.Role > RoleReadOnly || o.Strategy > BalanceAffinity || o.MaxAttempts < 0 || o.MaxAttempts > 4096 || len(o.Endpoints) > 1024 || o.AttemptTimeout < 0 || o.RetryDelay < 0 || o.BlacklistTimeout < 0 || o.ValidationInterval < 0 {
		return &ConfigError{Field: "Routing"}
	}
	o.Endpoints = slices.Clone(o.Endpoints)
	if len(o.Endpoints) == 0 {
		o.Endpoints = []Endpoint{{Addr: cfg.Addr, Role: o.Role}}
	}
	seen := make(map[string]bool, len(o.Endpoints))
	eligible := 0
	for i := range o.Endpoints {
		e := &o.Endpoints[i]
		if e.Role > RoleReadOnly || e.Addr == "" {
			return &ConfigError{Field: "Routing.Endpoints"}
		}
		addr, err := normalizeAddress(e.Addr)
		if err != nil || seen[addr] {
			return &ConfigError{Field: "Routing.Endpoints"}
		}
		e.Addr = addr
		seen[addr] = true
		if o.Role == RoleAny || e.Role == o.Role {
			eligible++
		}
	}
	if eligible == 0 {
		return &ConfigError{Field: "Routing.Role"}
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = eligible
	}
	if o.AttemptTimeout == 0 {
		o.AttemptTimeout = max(cfg.ConnectTimeout/time.Duration(min(eligible, o.MaxAttempts)), time.Nanosecond)
	}
	if o.RetryDelay == 0 {
		o.RetryDelay = 50 * time.Millisecond
	}
	if o.BlacklistTimeout == 0 {
		o.BlacklistTimeout = time.Second
	}
	return nil
}

func (o RoutingOptions) selector() (*routing.Selector, error) {
	var addresses []routing.Address
	for _, e := range o.Endpoints {
		if o.Role == RoleAny || e.Role == o.Role {
			addresses = append(addresses, routing.Address{Addr: e.Addr, Group: e.Group, Weight: e.Weight})
		}
	}
	return routing.NewSelector(addresses, routing.Options{Strategy: routing.Strategy(o.Strategy), BlacklistTimeout: o.BlacklistTimeout})
}
