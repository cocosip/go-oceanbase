package config

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/cocosip/go-oceanbase/internal/routing"
)

const (
	discoveryField           = "Discovery"
	addressDescriptionOption = "addressDescription"
)

// Topology is an owned discovery snapshot for one logical service.
// Explicit endpoint roles are required when Routing.Role is constrained.
type Topology struct {
	Service   string
	Endpoints []Endpoint
}

// TopologySource returns an owned snapshot and must honor ctx cancellation.
// Calls are serialized per Connector; sources shared across Connectors must be concurrency safe.
type TopologySource func(context.Context) (Topology, error)

// DiscoveryOptions refreshes candidates for new connections, never active sessions.
// Service is required; zero durations use 30s refresh, 5s request and 5m maximum age.
// Service is a caller-provided logical-service label, not proof of server-side
// tenant identity; the source must return only endpoints for the same tenant.
type DiscoveryOptions struct {
	Source                                  TopologySource
	Service                                 string
	RefreshInterval, RequestTimeout, MaxAge time.Duration
}

func (o *DiscoveryOptions) normalize() error {
	if o.Source == nil {
		if o.Service != "" || o.RefreshInterval != 0 || o.RequestTimeout != 0 || o.MaxAge != 0 {
			return &ConfigError{Field: discoveryField}
		}
		return nil
	}
	if o.Service == "" || len(o.Service) > 1024 || strings.IndexFunc(o.Service, unicode.IsControl) >= 0 || strings.TrimSpace(o.Service) != o.Service || o.RefreshInterval < 0 || o.RequestTimeout < 0 || o.MaxAge < 0 {
		return &ConfigError{Field: discoveryField}
	}
	if o.RefreshInterval == 0 {
		o.RefreshInterval = 30 * time.Second
	}
	if o.RequestTimeout == 0 {
		o.RequestTimeout = 5 * time.Second
	}
	if o.MaxAge == 0 {
		o.MaxAge = 5 * time.Minute
	}
	if o.MaxAge < o.RefreshInterval {
		return &ConfigError{Field: "Discovery.MaxAge"}
	}
	return nil
}

func publicTopology(s routing.Snapshot) Topology {
	t := Topology{Service: s.Service, Endpoints: make([]Endpoint, len(s.Endpoints))}
	for i, e := range s.Endpoints {
		t.Endpoints[i] = Endpoint{Addr: e.Addr, Role: EndpointRole(e.Role), Group: e.Group, Weight: e.Weight}
	}
	return t
}

// ParseAddressDescription extracts an inline TNS DESCRIPTION without file or network I/O.
// Connector routing options take precedence over Java load-balancing policy hints.
func ParseAddressDescription(description string) (Topology, error) {
	s, err := routing.ParseTNS([]byte(description), "")
	if err != nil {
		return Topology{}, &ConfigError{Field: addressDescriptionOption}
	}
	return publicTopology(s), nil
}

// NewTNSFileSource reads only the explicitly supplied regular file and alias on each lookup.
// No environment search, disk cache or global watcher is installed.
func NewTNSFileSource(path, alias string) (TopologySource, error) {
	if path == "" || strings.ContainsRune(path, 0) || alias == "" || len(alias) > 256 {
		return nil, &ConfigError{Field: "TNS source"}
	}
	return func(ctx context.Context) (Topology, error) {
		if err := ctx.Err(); err != nil {
			return Topology{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return Topology{}, routing.ErrUnavailable
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return Topology{}, routing.ErrInvalid
		}
		file, err := os.Open(path)
		if err != nil {
			return Topology{}, routing.ErrUnavailable
		}
		defer func() { _ = file.Close() }()
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return Topology{}, routing.ErrInvalid
		}
		data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil {
			return Topology{}, routing.ErrUnavailable
		}
		if err = ctx.Err(); err != nil {
			return Topology{}, err
		}
		s, err := routing.ParseTNS(data, alias)
		return publicTopology(s), err
	}, nil
}

// NewOCPSource reads the Connector/J loadBalanceInfo JSON response, including its data envelope.
// The URL is explicit; redirects and embedded credentials are refused.
// The client is copied; its transport and callbacks remain caller-owned and concurrency safe.
func NewOCPSource(endpoint string, client *http.Client) (TopologySource, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, &ConfigError{Field: "OCP source"}
	}
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	return func(ctx context.Context) (Topology, error) {
		s, err := routing.FetchOCP(ctx, &copyClient, endpoint)
		return publicTopology(s), err
	}, nil
}

// parseDiscoveryDSN consumes source options together so query-map order cannot select a source.
func parseDiscoveryDSN(cfg *Config, values url.Values) error {
	get := func(name string) (string, error) {
		v := values[name]
		delete(values, name)
		if len(v) == 0 {
			return "", nil
		}
		if len(v) != 1 || v[0] == "" {
			return "", &ConfigError{Field: name}
		}
		return v[0], nil
	}
	var file, alias, ocp, description string
	for _, v := range []struct {
		name   string
		target *string
	}{{"tnsFile", &file}, {"tnsAlias", &alias}, {"ocpURL", &ocp}, {addressDescriptionOption, &description}, {"discoveryService", &cfg.Discovery.Service}} {
		s, err := get(v.name)
		if err != nil {
			return err
		}
		*v.target = s
	}
	for _, v := range []struct {
		name   string
		target *time.Duration
	}{{"discoveryRefreshInterval", &cfg.Discovery.RefreshInterval}, {"discoveryTimeout", &cfg.Discovery.RequestTimeout}, {"discoveryMaxAge", &cfg.Discovery.MaxAge}} {
		s, err := get(v.name)
		if err != nil {
			return err
		}
		if s != "" {
			*v.target, err = time.ParseDuration(s)
			if err != nil {
				return &ConfigError{Field: v.name}
			}
		}
	}
	if (file != "") != (alias != "") || (file != "" && ocp != "") || (description != "" && (file != "" || ocp != "" || values.Has("addresses"))) {
		return &ConfigError{Field: "Discovery source"}
	}
	var err error
	switch {
	case file != "":
		cfg.Discovery.Source, err = NewTNSFileSource(file, alias)
	case ocp != "":
		cfg.Discovery.Source, err = NewOCPSource(ocp, nil)
	case description != "":
		var t Topology
		t, err = ParseAddressDescription(description)
		cfg.Routing.Endpoints = t.Endpoints
	}
	return err
}
