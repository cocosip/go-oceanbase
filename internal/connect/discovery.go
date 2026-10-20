package connect

import (
	"context"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/routing"
)

func newDiscovery(o config.DiscoveryOptions, options config.RoutingOptions) (*routing.Discovery, error) {
	if o.Source == nil {
		return nil, nil
	}
	d, err := routing.NewDiscovery(func(ctx context.Context) (routing.Snapshot, error) {
		t, err := o.Source(ctx)
		if err != nil {
			return routing.Snapshot{}, err
		}
		if len(t.Endpoints) > 1024 {
			return routing.Snapshot{}, routing.ErrInvalid
		}
		s := routing.Snapshot{Service: t.Service, Endpoints: make([]routing.DiscoveredEndpoint, len(t.Endpoints))}
		for i, e := range t.Endpoints {
			s.Endpoints[i] = routing.DiscoveredEndpoint{Address: routing.Address{Addr: e.Addr, Group: e.Group, Weight: e.Weight}, Role: uint8(e.Role)}
		}
		return s, nil
	}, routing.DiscoveryOptions{Service: o.Service, Role: uint8(options.Role), RefreshInterval: o.RefreshInterval, RequestTimeout: o.RequestTimeout, MaxAge: o.MaxAge, Selection: routing.Options{Strategy: routing.Strategy(options.Strategy), BlacklistTimeout: options.BlacklistTimeout}})
	if err != nil {
		return nil, &config.ConfigError{Field: "Discovery"}
	}
	return d, nil
}

func routingSelector(o config.RoutingOptions) (*routing.Selector, error) {
	var addresses []routing.Address
	for _, e := range o.Endpoints {
		if o.Role == config.RoleAny || e.Role == o.Role {
			addresses = append(addresses, routing.Address{Addr: e.Addr, Group: e.Group, Weight: e.Weight})
		}
	}
	return routing.NewSelector(addresses, routing.Options{Strategy: routing.Strategy(o.Strategy), BlacklistTimeout: o.BlacklistTimeout})
}
