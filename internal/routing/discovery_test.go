package routing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const discoveryTestService = "orders"
const discoveryHealthA = "health-a:2881"
const discoveryHealthB = "health-b:2881"

const discoveryTestAddress = "host:2881"

const testDescription = `(DESCRIPTION=(ADDRESS_LIST=(ADDRESS=(PROTOCOL=tcp)(HOST=HOST1)(PORT=2881)(TYPE=master))(ADDRESS=(HOST=host2)(PORT=2882)(TYPE=slave)(WEIGHT=7)))(CONNECT_DATA=(SERVICE_NAME=orders)))`
const testOCP = `{"ADDRESS_LIST":[{"ADDRESS":[{"PROTOCOL":"tcp","HOST":"host1","PORT":2881,"TYPE":"master"}]}],"CONNECT_DATA":{"SERVICE_NAME":"orders"}}`

func TestTNSParser(t *testing.T) {
	for _, input := range []string{testDescription, "orders=" + testDescription, "(orders=" + testDescription + ")", "# comment\nother=" + strings.ReplaceAll(testDescription, discoveryTestService, "other") + "\norders=" + testDescription} {
		alias := discoveryTestService
		if input == testDescription {
			alias = ""
		}
		s, err := ParseTNS([]byte(input), alias)
		if err != nil || s.Service != discoveryTestService || len(s.Endpoints) != 2 {
			t.Fatalf("parse: %+v %v", s, err)
		}
		if s.Endpoints[0].Addr != "host1:2881" || s.Endpoints[0].Role != 1 || s.Endpoints[1].Role != 2 || s.Endpoints[1].Weight != 7 {
			t.Fatalf("endpoint semantics: %+v", s.Endpoints)
		}
	}
	for _, input := range []string{"", testDescription + "garbage", strings.Replace(testDescription, "tcp", "udp", 1), strings.Replace(testDescription, "PORT=2881", "PORT=0", 1), strings.Replace(testDescription, "HOST=HOST1", "HOST=HOST1)(HOST=other", 1), strings.Replace(testDescription, "TYPE=master", "TYPE=unknown", 1), "orders=" + testDescription + " orders=" + testDescription, strings.Repeat("(x=", 40) + "a" + strings.Repeat(")", 40), strings.Repeat(" ", (1<<20)+1), strings.Replace(testDescription, "ADDRESS_LIST=", "UNKNOWN=", 1)} {
		alias := ""
		if strings.HasPrefix(input, "orders=") {
			alias = discoveryTestService
		}
		if _, err := ParseTNS([]byte(input), alias); err == nil {
			t.Fatal("accepted invalid descriptor")
		}
	}
}

func TestOCPRefresh(t *testing.T) {
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			_, _ = w.Write([]byte(testOCP + "{}"))
		case 3:
			<-r.Context().Done()
		case 4:
			_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
		default:
			_, _ = w.Write([]byte(testOCP))
		}
	}))
	defer server.Close()
	for i := range int32(5) {
		mode.Store(i)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		s, err := FetchOCP(ctx, server.Client(), server.URL)
		cancel()
		if i == 0 {
			if err != nil || s.Service != discoveryTestService || len(s.Endpoints) != 1 || s.Endpoints[0].Role != 1 {
				t.Fatalf("OCP: %+v %v", s, err)
			}
		} else if err == nil {
			t.Fatalf("mode %d accepted", i)
		}
	}
	for _, input := range []string{`{}`, `{"data":` + testOCP + `}`, strings.Replace(testOCP, `"HOST":"host1"`, `"HOST":"host1","HOST":"other"`, 1), strings.Replace(testOCP, "2881", "-1", 1), strings.Replace(testOCP, "tcp", "udp", 1)} {
		if _, err := ParseOCP([]byte(input)); err == nil {
			t.Fatal("accepted invalid OCP response")
		}
	}
}

func TestOCPHTTPEnvelope(t *testing.T) {
	const prefix = `{"successful":true,"status":200,"data":{"NET_SERVICE_NAME":"orders","DESCRIPTION":`
	// NET_SERVICE_NAME identifies the logical service, independently of CONNECT_DATA.
	for _, description := range []string{strings.Replace(testOCP, `"SERVICE_NAME":"orders"`, `"SERVICE_NAME":"database-service"`, 1), `{"ADDRESS_LIST":[{"ADDRESS":[{"HOST":"host1"}]}]}`} {
		s, err := ParseOCP([]byte(prefix + description + `}}`))
		if err != nil || s.Service != discoveryTestService || len(s.Endpoints) != 1 {
			t.Fatalf("OCP envelope: %+v %v", s, err)
		}
	}
	for _, input := range []string{strings.Replace(prefix+testOCP+`}}`, `"successful":true`, `"successful":false`, 1), strings.Replace(prefix+testOCP+`}}`, `"status":200`, `"status":500`, 1), prefix + testOCP + `},"data":{}}`, strings.Replace(prefix+testOCP+`}}`, `"NET_SERVICE_NAME":"orders"`, `"NET_SERVICE_NAME":""`, 1)} {
		if _, err := ParseOCP([]byte(input)); err == nil {
			t.Fatal("accepted invalid envelope")
		}
	}
}

func TestTopologyChanges(t *testing.T) {
	var generation atomic.Int32
	source := func(context.Context) (Snapshot, error) {
		s := Snapshot{Service: discoveryTestService, Endpoints: []DiscoveredEndpoint{{Address: Address{Addr: "old:2881"}, Role: 1}}}
		switch generation.Load() {
		case 1:
			s.Endpoints[0].Addr = "new:2881"
		case 2:
			s.Service = "other"
		case 3:
			s.Endpoints[0].Role = 2
		case 4:
			return Snapshot{}, errors.New("unavailable")
		}
		return s, nil
	}
	d, err := NewDiscovery(source, DiscoveryOptions{Service: discoveryTestService, Role: 1, RefreshInterval: 5 * time.Millisecond, RequestTimeout: time.Second, MaxAge: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	route, count, err := d.Get(ctx)
	if err != nil || count != 1 {
		t.Fatalf("initial: %v", err)
	}
	addresses, _ := route.Order(time.Now())
	if addresses[0].Addr != "old:2881" {
		t.Fatal(addresses)
	}
	generation.Store(1)
	for {
		updated, _, e := d.Get(ctx)
		if e != nil {
			t.Fatal(e)
		}
		addresses, _ = updated.Order(time.Now())
		if addresses[0].Addr == "new:2881" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("refresh did not publish")
		case <-time.After(time.Millisecond):
		}
	}
	for _, n := range []int32{2, 3, 4} {
		generation.Store(n)
		if err = waitDiscoveryFailure(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.Get(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func waitDiscoveryFailure(ctx context.Context, d *Discovery) error {
	for {
		if _, _, err := d.Get(ctx); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

func TestTopologyCloseCancelsLookup(t *testing.T) {
	entered := make(chan struct{})
	stopped := make(chan struct{})
	d, err := NewDiscovery(func(ctx context.Context) (Snapshot, error) {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return Snapshot{}, ctx.Err()
	}, DiscoveryOptions{Service: discoveryTestService, RefreshInterval: time.Second, RequestTimeout: time.Second, MaxAge: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, _, e := d.Get(context.Background()); result <- e }()
	<-entered
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	<-stopped
	if e := <-result; !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
}

func TestTopologyRejectsInvalidSnapshots(t *testing.T) {
	for _, s := range []Snapshot{
		{Service: "other", Endpoints: []DiscoveredEndpoint{{Address: Address{Addr: discoveryTestAddress}, Role: 1}}},
		{Service: discoveryTestService, Endpoints: []DiscoveredEndpoint{{Address: Address{Addr: discoveryTestAddress}, Role: 2}}},
		{Service: discoveryTestService, Endpoints: []DiscoveredEndpoint{{Address: Address{Addr: discoveryTestAddress}}}},
		{Service: discoveryTestService, Endpoints: []DiscoveredEndpoint{{Address: Address{Addr: discoveryTestAddress}, Role: 1}, {Address: Address{Addr: "HOST:2881"}, Role: 1}}},
		{Service: discoveryTestService},
	} {
		d, err := NewDiscovery(func(context.Context) (Snapshot, error) { return s, nil }, DiscoveryOptions{Service: discoveryTestService, Role: 1, RefreshInterval: time.Second, RequestTimeout: time.Second, MaxAge: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _, err = d.Get(ctx)
		cancel()
		if err == nil {
			t.Fatal("invalid topology published")
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTopologyConcurrentAndLateResponse(t *testing.T) {
	var calls, active atomic.Int32
	var overlap atomic.Bool
	d, err := NewDiscovery(func(ctx context.Context) (Snapshot, error) {
		calls.Add(1)
		if active.Add(1) != 1 {
			overlap.Store(true)
		}
		defer active.Add(-1)
		<-ctx.Done()
		// Returning success after cancellation must not publish expired data.
		return Snapshot{Service: discoveryTestService, Endpoints: []DiscoveredEndpoint{{Address: Address{Addr: discoveryTestAddress}}}}, nil
	}, DiscoveryOptions{Service: discoveryTestService, RefreshInterval: time.Second, RequestTimeout: 30 * time.Millisecond, MaxAge: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if calls.Load() != 0 {
		t.Fatal("eager lookup")
	}
	results := make(chan error, 20)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range 20 {
		go func() { _, _, e := d.Get(ctx); results <- e }()
	}
	for range 20 {
		if e := <-results; e == nil {
			t.Fatal("late success published")
		}
	}
	if overlap.Load() || calls.Load() != 1 {
		t.Fatalf("lookups=%d overlap=%v", calls.Load(), overlap.Load())
	}
}

func TestTopologyHealthAcrossRefresh(t *testing.T) {
	d, err := NewDiscovery(func(context.Context) (Snapshot, error) {
		return Snapshot{Service: discoveryTestService, Endpoints: []DiscoveredEndpoint{{Address: Address{Addr: discoveryHealthA}}, {Address: Address{Addr: discoveryHealthB}}}}, nil
	}, DiscoveryOptions{Service: discoveryTestService, RefreshInterval: time.Second, RequestTimeout: time.Second, MaxAge: time.Second, Selection: Options{Strategy: RoundRobin, BlacklistTimeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	d.refresh()
	previous, _, err := d.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first, _ := previous.Order(time.Now())
	if first[0].Addr != discoveryHealthA {
		t.Fatal(first)
	}
	d.refresh()
	current, _, err := d.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, _ := current.Order(time.Now())
	if second[0].Addr != discoveryHealthB {
		t.Fatal("round-robin reset across refresh")
	}
	// An older in-flight Connect reports its dial failure after the next generation is published.
	previous.Failure(discoveryHealthA, time.Now())
	healthy, _ := current.Order(time.Now())
	if len(healthy) != 1 || healthy[0].Addr != discoveryHealthB {
		t.Fatal("late dial failure lost across refresh", healthy)
	}
	previous.Success(discoveryHealthA)
	healthy, _ = current.Order(time.Now())
	if len(healthy) != 2 {
		t.Fatal("late success did not clear shared health")
	}
}

func TestTopologyFailureCached(t *testing.T) {
	var calls atomic.Int32
	d, err := NewDiscovery(func(context.Context) (Snapshot, error) { calls.Add(1); return Snapshot{}, ErrUnavailable }, DiscoveryOptions{Service: discoveryTestService, RefreshInterval: time.Hour, RequestTimeout: time.Second, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, _, err = d.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err = d.Get(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("known source failure waited for next refresh: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("lookup retried outside refresh schedule")
	}
}

func TestTopologyCloseBounded(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	d, err := NewDiscovery(func(context.Context) (Snapshot, error) { close(entered); <-release; return Snapshot{}, ErrUnavailable }, DiscoveryOptions{Service: discoveryTestService, RefreshInterval: time.Hour, RequestTimeout: 20 * time.Millisecond, MaxAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); _ = d.Close() }()
	result := make(chan error, 1)
	go func() { _, _, e := d.Get(context.Background()); result <- e }()
	<-entered
	start := time.Now()
	if err = d.Close(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("uncooperative Close: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Close wait unbounded")
	}
	if err = <-result; !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestTNSPolicyHints(t *testing.T) {
	input := strings.Replace(testDescription, "(ADDRESS_LIST=", `(OBLB=ON)(OBLB_RETRY_ALL_DOWNS=110)(OBLB_GROUP_STRATEGY=ROTATION)(OBLB_STRATEGY=RANDOM)(OBLB_BLACKLIST=(REMOVE_STRATEGY=(NAME=TIMEOUT)(TIMEOUT=100))(APPEND_STRATEGY=(NAME=RETRYDURATION)(RETRYTIMES=3)(DURATION=100)))(ADDRESS_LIST=(OBLB_STRATEGY=SERVERAFFINITY)`, 1)
	if s, err := ParseTNS([]byte(input), ""); err != nil || len(s.Endpoints) != 2 {
		t.Fatalf("known policies: %+v %v", s, err)
	}
	input = strings.Replace(testDescription, "(ADDRESS_LIST=", "(ADDRESS_LIST=(OBLB_STRATEGY=RANDOM)(OBLB_STRATEGY=ROTATION)", 1)
	if _, err := ParseTNS([]byte(input), ""); err == nil {
		t.Fatal("duplicate group policy accepted")
	}
}

func FuzzTNSParser(f *testing.F) {
	f.Add(testDescription)
	f.Add("orders=" + testDescription)
	f.Add("(x=(DESCRIPTION=))")
	f.Add(testOCP)
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		_, _ = ParseTNS([]byte(input), discoveryTestService)
		_, _ = ParseTNS([]byte(input), "")
		_, _ = ParseOCP([]byte(input))
	})
}
