package config

import (
	"testing"
	"time"
)

func TestRoutingDSN(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://app@baseline/?addresses=first.example,second.example:2882&role=readonly&balance=roundrobin&maxAttempts=3&attemptTimeout=200ms&retryDelay=1ms&blacklistTimeout=10ms&validationInterval=1m")
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.Routing
	if len(o.Endpoints) != 2 || o.Endpoints[0].Addr != routeFirst || o.Endpoints[1].Addr != "second.example:2882" || o.Endpoints[0].Role != RoleReadOnly || o.Role != RoleReadOnly || o.Strategy != BalanceRoundRobin || o.MaxAttempts != 3 || o.AttemptTimeout != 200*time.Millisecond || o.RetryDelay != time.Millisecond || o.BlacklistTimeout != 10*time.Millisecond || o.ValidationInterval != time.Minute {
		t.Fatal(o)
	}
	for _, option := range []string{"addresses=a,,b", "addresses=a,A:2881", "addresses=bad:0", "role=standby", "balance=unknown", "maxAttempts=4097", "maxAttempts=-1", "attemptTimeout=-1s", "retryDelay=bad", "blacklistTimeout=-1s", "validationInterval=-1s"} {
		if _, err = ParseDSN("oceanbase://app@baseline/?" + option); err == nil {
			t.Fatal(option)
		}
	}
}
