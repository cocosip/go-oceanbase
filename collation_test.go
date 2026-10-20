package oceanbase

import (
	"fmt"
)

func ExampleSessionOptions_collation() {
	connector, err := NewConnector(Config{User: "app", Session: SessionOptions{Charset: "utf8mb4", Collation: "utf8mb4_bin"}})
	fmt.Println(connector != nil, err == nil)
	// Output: true true
}
