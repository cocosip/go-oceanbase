package routing

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

const testAddressA = "a:2881"

func TestAddressSelection(t *testing.T) {
	addresses := []Address{{Addr: testAddressA}, {Addr: "b:2881"}, {Addr: "c:2881", Group: 1}}
	s, err := NewSelector(addresses, Options{Strategy: RoundRobin, BlacklistTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{testAddressA, "b:2881", testAddressA} {
		got, wait := s.Order(time.Now())
		if len(got) != 3 || got[0].Addr != want || got[2].Addr != "c:2881" || wait != 0 {
			t.Fatal(got, wait)
		}
	}
	addresses[0].Addr = "mutated"
	for _, strategy := range []Strategy{Ordered, Random, Affinity} {
		s, err = NewSelector([]Address{{Addr: "a", Weight: 1}, {Addr: "b", Weight: 9}, {Addr: "c", Group: 1}}, Options{Strategy: strategy, BlacklistTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		for range 20 {
			got, _ := s.Order(time.Now())
			if len(got) != 3 || got[0].Addr == got[1].Addr || got[2].Addr != "c" {
				t.Fatal(got)
			}
		}
	}
}

func TestAddressHealth(t *testing.T) {
	now := time.Now()
	s, _ := NewSelector([]Address{{Addr: "a"}, {Addr: "b"}}, Options{BlacklistTimeout: time.Second})
	s.Failure("a", now)
	got, wait := s.Order(now)
	if len(got) != 1 || got[0].Addr != "b" || wait != 0 {
		t.Fatal(got, wait)
	}
	s.Failure("b", now.Add(time.Millisecond))
	got, wait = s.Order(now)
	if len(got) != 0 || wait != time.Second {
		t.Fatal(got, wait)
	}
	got, wait = s.Order(now.Add(time.Second))
	if len(got) != 1 || got[0].Addr != "a" || wait != 0 {
		t.Fatal(got, wait)
	}
	s.Success("b")
	got, _ = s.Order(now)
	if len(got) != 1 || got[0].Addr != "b" {
		t.Fatal(got)
	}
	other, _ := NewSelector([]Address{{Addr: "a"}}, Options{BlacklistTimeout: time.Second})
	got, _ = other.Order(now)
	if len(got) != 1 {
		t.Fatal("health leaked across connectors")
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 100 {
				s.Failure("a", now)
				s.Success("a")
				s.Order(now)
			}
		})
	}
	group.Wait()
}

func TestAddressParsing(t *testing.T) {
	for input, want := range map[string]string{"": "127.0.0.1:2881", "db": "db:2881", "[::1]": "[::1]:2881", "[::1]:2882": "[::1]:2882", "db:02881": "db:2881"} {
		got, err := NormalizeAddress(input)
		if err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{"db:0", "db:65536", "bad name", "db:port", "db\x00", "host/a", "a,b"} {
		if _, err := NormalizeAddress(input); err == nil {
			t.Fatal(input)
		}
	}
	for _, options := range []Options{{Strategy: 99}, {BlacklistTimeout: -1}} {
		if _, err := NewSelector([]Address{{Addr: "a"}}, options); err == nil {
			t.Fatal(options)
		}
	}
	if _, err := NewSelector(nil, Options{}); err == nil {
		t.Fatal("empty selector accepted")
	}
	s, _ := NewSelector([]Address{{Addr: "a"}, {Addr: "b"}}, Options{})
	a, _ := s.Order(time.Now())
	b, _ := s.Order(time.Now())
	if !reflect.DeepEqual(a, b) {
		t.Fatal(a, b)
	}
}

func FuzzAddressParsing(f *testing.F) {
	for _, seed := range []string{"", "db", "DB:02881", "[::1]:2881", "::1", "db:0", "a,b", "db\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		normalized, err := NormalizeAddress(input)
		if err != nil {
			return
		}
		again, err := NormalizeAddress(normalized)
		if err != nil || again != normalized {
			t.Fatalf("noncanonical address: %q -> %q: %v", normalized, again, err)
		}
	})
}
