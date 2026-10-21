package oceanbase

import (
	context "context"
	sql "database/sql"
	errors "errors"
	testing "testing"
)

func TestDriverRegistration(t *testing.T) {
	db, err := sql.Open("oceanbase", "oceanbase://app:secret@localhost/demo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = db.PingContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Ping opened a connection: %v", err)
	}
	if _, err = sql.Open("oceanbase", "oceanbase://app:secret@localhost/demo?unknown=secret"); err == nil {
		t.Fatal("DriverContext must validate DSN before dialing")
	}
}
