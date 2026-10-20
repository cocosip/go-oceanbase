package config

import (
	"testing"
)

func TestPoolJavaDSNRejected(t *testing.T) {
	for _, key := range []string{"pool", "poolName", "maxPoolSize", "minPoolSize", "maxIdleTime", "poolValidMinDelay", "staticGlobal", "registerJmxPool"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + key + "=1"); err == nil {
			t.Fatal("Java pool option silently accepted:", key)
		}
	}
}
