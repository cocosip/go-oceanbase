package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"

	oceanbase "github.com/cocosip/go-oceanbase"
)

const (
	topologyCase        = "topology-sources"
	topologyDescription = "(DESCRIPTION=(ADDRESS_LIST=(ADDRESS=(PROTOCOL=TCP)(HOST=127.0.0.1)(PORT=2881))(ADDRESS=(PROTOCOL=TCP)(HOST=127.0.0.2)(PORT=2882)(TYPE=slave)))(CONNECT_DATA=(SERVICE_NAME=obtenant)))"
	topologyMalformed   = "(DESCRIPTION=(ADDRESS=(PROTOCOL=UDP)(HOST=127.0.0.1)))"
	topologyAlias       = "OBCLUSTER"
	topologyTNS         = topologyAlias + " =\n  (DESCRIPTION =\n    (ADDRESS = (PROTOCOL = TCP)(HOST = 127.0.0.3)(PORT = 2883))\n    (CONNECT_DATA = (SERVICE_NAME = obs)))\n"
	topologyOCPBody     = `{"successful":true,"status":200,"data":{"NET_SERVICE_NAME":"obsvc","DESCRIPTION":{"ADDRESS_LIST":[{"ADDRESS":[{"PROTOCOL":"tcp","HOST":"127.0.0.4","PORT":2884,"TYPE":"master"}]}],"CONNECT_DATA":{"SERVICE_NAME":"obsvc"}}}}`
	topologyFirstAddr   = "127.0.0.1:2881"
	topologySecondAddr  = "127.0.0.2:2882"
	topologyTNSAddr     = "127.0.0.3:2883"
	topologyOCPAddr     = "127.0.0.4:2884"
	topologyService     = "obtenant"
	topologyTNSService  = "obs"
	topologyOCPService  = "obsvc"
)

// runTopologySources verifies the public discovery sources without a database:
// an inline TNS description, an explicit tnsnames.ora file, and an OCP endpoint
// each yield the documented service and endpoint roles, while malformed input,
// unknown aliases and non-HTTP endpoints are rejected.
func runTopologySources(ctx context.Context, _ *scenario) error {
	topology, err := oceanbase.ParseAddressDescription(topologyDescription)
	if err != nil {
		return err
	}
	if err = expectTopology(topology, topologyService, []topologyExpectation{
		{addr: topologyFirstAddr, role: oceanbase.RoleAny},
		{addr: topologySecondAddr, role: oceanbase.RoleReadOnly},
	}); err != nil {
		return err
	}
	if _, err = oceanbase.ParseAddressDescription(topologyMalformed); err == nil {
		return checkFailure("malformed address description was accepted")
	}
	if err = checkTNSFileSource(ctx); err != nil {
		return err
	}
	if err = checkOCPSource(ctx); err != nil {
		return err
	}
	if err = checkDiscoveryDSN(ctx); err != nil {
		return err
	}
	for _, endpoint := range []string{"ftp://127.0.0.1/ocp", "http://user:secret@127.0.0.1/ocp"} {
		if _, err = oceanbase.NewOCPSource(endpoint, nil); err == nil {
			return checkFailure("invalid OCP endpoint was accepted")
		}
	}
	return nil
}

type topologyExpectation struct {
	addr string
	role oceanbase.EndpointRole
}

func expectTopology(topology oceanbase.Topology, service string, want []topologyExpectation) error {
	if topology.Service != service || len(topology.Endpoints) != len(want) {
		return checkFailure("discovery source returned an unexpected topology")
	}
	for i := range want {
		if topology.Endpoints[i].Addr != want[i].addr || topology.Endpoints[i].Role != want[i].role {
			return checkFailure("discovery source endpoint changed")
		}
	}
	return nil
}

func checkTNSFileSource(ctx context.Context) error {
	file, err := os.CreateTemp("", "obtest-tns-*.ora")
	if err != nil {
		return err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err = file.WriteString(topologyTNS); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	source, err := oceanbase.NewTNSFileSource(path, topologyAlias)
	if err != nil {
		return err
	}
	topology, err := source(ctx)
	if err != nil {
		return err
	}
	if err = expectTopology(topology, topologyTNSService, []topologyExpectation{
		{addr: topologyTNSAddr, role: oceanbase.RoleAny},
	}); err != nil {
		return err
	}
	missing, err := oceanbase.NewTNSFileSource(path, "OTHER")
	if err != nil {
		return err
	}
	if _, err = missing(ctx); err == nil {
		return checkFailure("unknown TNS alias was accepted")
	}
	absent, err := oceanbase.NewTNSFileSource(path+".missing", topologyAlias)
	if err != nil {
		return err
	}
	if _, err = absent(ctx); err == nil {
		return checkFailure("missing TNS file was accepted")
	}
	return nil
}

func checkDiscoveryDSN(ctx context.Context) error {
	values := url.Values{"addressDescription": {topologyDescription}}.Encode()
	cfg, err := oceanbase.ParseDSN("oceanbase://app@127.0.0.1:2881/demo?" + values)
	if err != nil {
		return err
	}
	if err = expectTopology(oceanbase.Topology{Service: "", Endpoints: cfg.Routing.Endpoints}, "", []topologyExpectation{
		{addr: topologyFirstAddr, role: oceanbase.RoleAny},
		{addr: topologySecondAddr, role: oceanbase.RoleReadOnly},
	}); err != nil {
		return err
	}
	for _, dsn := range []string{
		"oceanbase://app@127.0.0.1:2881/demo?tnsFile=" + url.QueryEscape("/nonexistent.ora") + "&discoveryService=" + topologyService,
		"oceanbase://app@127.0.0.1:2881/demo?tnsFile=%2Fnonexistent.ora&tnsAlias=OB&ocpURL=http%3A%2F%2F127.0.0.1%2Focp&discoveryService=" + topologyService,
		"oceanbase://app@127.0.0.1:2881/demo?addressDescription=" + url.QueryEscape(topologyDescription) + "&addresses=127.0.0.2%3A2881",
	} {
		if _, err = oceanbase.ParseDSN(dsn); err == nil {
			return checkFailure("conflicting discovery DSN options were accepted")
		}
	}
	file, err := os.CreateTemp("", "obtest-tns-dsn-*.ora")
	if err != nil {
		return err
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err = file.WriteString(topologyTNS); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	dsn := "oceanbase://app@127.0.0.1:2881/demo?tnsFile=" + url.QueryEscape(path) + "&tnsAlias=" + topologyAlias + "&discoveryService=" + topologyService
	cfg, err = oceanbase.ParseDSN(dsn)
	if err != nil {
		return err
	}
	if cfg.Discovery.Source == nil || cfg.Discovery.Service != topologyService {
		return checkFailure("DSN tnsFile option did not install a discovery source")
	}
	ocpDSN := "oceanbase://app@127.0.0.1:2881/demo?ocpURL=http%3A%2F%2F127.0.0.1%2Focp&discoveryService=" + topologyOCPService
	ocpCfg, err := oceanbase.ParseDSN(ocpDSN)
	if err != nil || ocpCfg.Discovery.Source == nil {
		return checkFailure("DSN ocpURL option did not install a discovery source")
	}
	topology, err := cfg.Discovery.Source(ctx)
	if err != nil {
		return err
	}
	return expectTopology(topology, topologyTNSService, []topologyExpectation{
		{addr: topologyTNSAddr, role: oceanbase.RoleAny},
	})
}

func checkOCPSource(ctx context.Context) error {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, topologyOCPBody)
	}))
	defer server.Close()
	source, err := oceanbase.NewOCPSource(server.URL+"/ocp", server.Client())
	if err != nil {
		return err
	}
	topology, err := source(ctx)
	if err != nil {
		return err
	}
	return expectTopology(topology, topologyOCPService, []topologyExpectation{
		{addr: topologyOCPAddr, role: oceanbase.RoleReadWrite},
	})
}
