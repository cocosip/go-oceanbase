//go:build integration

package integration

import (
	"os"
	"testing"
)

func TestSmoke(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	require := os.Getenv("OB_TEST_REQUIRE")
	if require != "" && require != "0" && require != "1" {
		t.Fatal("OB_TEST_REQUIRE must be 0 or 1")
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			db := openProfile(t, p)
			if err := db.PingContext(testContext(t)); err != nil {
				failOperation(t, "Ping", err)
			}
			// D05 adds SQL-based version capture; Ping already validates actual mode through authentication.
			t.Log("authenticated connection, session initialization and Ping passed; server version capture pending D05")
		})
	}
}
