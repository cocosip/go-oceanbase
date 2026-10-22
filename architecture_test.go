package oceanbase_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type listedPackage struct {
	ImportPath                         string
	ForTest                            string
	Standard                           bool
	Imports, TestImports, XTestImports []string
}

func TestPackageDependencies(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-json", "-test", "-tags=integration", "./...")
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("package graph: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var packages []listedPackage
	for {
		var p listedPackage
		if err = decoder.Decode(&p); err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, p)
	}
	if violations := dependencyViolations("github.com/cocosip/go-oceanbase", packages); len(violations) > 0 {
		t.Fatal(strings.Join(violations, "\n"))
	}
}

func TestPackageDependencyRules(t *testing.T) {
	const module = "example.test/driver"
	for _, tc := range []struct {
		name, from, to string
		test           bool
		want           int
	}{
		{"connect to control", module + "/internal/connect", module + "/internal/control", false, 1},
		{"statement to execution", module + "/internal/statement", module + "/internal/execution", false, 1},
		{"statement to parameters", module + "/internal/statement", module + "/internal/parameters", false, 1},
		{"codec to catalog", module + "/internal/valuecodec", module + "/internal/catalog", false, 1},
		{"codec to parameters", module + "/internal/valuecodec", module + "/internal/parameters", false, 1},
		{"codec to streaminput", module + "/internal/valuecodec", module + "/internal/streaminput", false, 1},
		{"statement to call", module + "/internal/statement", module + "/internal/call", false, 1},
		{"codec to call", module + "/internal/valuecodec", module + "/internal/call", false, 1},
		{"batch to execution", module + "/internal/batch", module + "/internal/execution", false, 1},
		{"public config to root", module + "/config", module, false, 1},
		{"root to wire", module, module + "/internal/wire", false, 0},
		{"wire to root", module + "/internal/wire", module, false, 1},
		{"auth to wire", module + "/internal/auth", module + "/internal/wire", false, 1},
		{"production to helper", module, module + "/internal/testutil", false, 1},
		{"test to helper", module, module + "/internal/testutil", true, 0},
		{"production to public helper", module, module + "/internal/publictest", false, 1},
		{"test to public helper", module, module + "/internal/publictest", true, 0},
		{"codec to execution", module + "/internal/valuecodec", module + "/internal/execution", false, 1},
		{"session to control", module + "/internal/session", module + "/internal/control", false, 1},
		{"helper to driver", module + "/internal/testutil", module, false, 1},
		{"helper to third party", module + "/internal/testutil", "example.test/other", false, 1},
		{"helper to stdlib", module + "/internal/testutil", "net", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := listedPackage{ImportPath: tc.from, Imports: []string{tc.to}}
			if tc.test {
				p.ForTest = tc.from
			}
			violations := dependencyViolations(module, []listedPackage{p, {ImportPath: "net", Standard: true}})
			if len(violations) != tc.want {
				t.Fatalf("got %v, want %d violations", violations, tc.want)
			}
		})
	}
}

// The table describes production imports. Tests may use the public facade and shared peers.
//
//nolint:goconst // Literal package paths keep the explicit dependency table reviewable.
func allowedProjectDependencies() map[string][]string {
	return map[string][]string{
		"":                    {"config", "types", "dberrors", "metadata", "obproxy", "internal/configspec", "internal/authentication", "internal/transport", "internal/connect", "internal/session", "internal/command", "internal/sqltext", "internal/valueformat", "internal/valuecodec", "internal/rowset", "internal/catalog", "internal/streaminput", "internal/parameters", "internal/statement", "internal/lob", "internal/materialize", "internal/cursor", "internal/call", "internal/batch", "internal/xa", "internal/lifetime", "internal/control", "internal/diagnostic", "internal/execution", "internal/wire", "internal/auth", "internal/charset", "internal/routing"},
		"config":              {"internal/configspec", "internal/auth", "internal/charset", "internal/routing", "internal/wire"},
		"types":               {"internal/valueformat", "dberrors", "internal/wire"},
		"dberrors":            {"config", "internal/routing"},
		"metadata":            {},
		"obproxy":             {"config", "dberrors"},
		"internal/configspec": {"internal/wire", "internal/charset", "internal/routing"},
		"internal/auth":       {}, "internal/charset": {}, "internal/routing": {}, "internal/wire": {},
		"internal/authentication":      {"config", "internal/auth", "internal/charset", "internal/wire", "dberrors"},
		"internal/transport":           {"dberrors"},
		"internal/connect":             {"config", "internal/configspec", "internal/routing", "internal/transport", "internal/authentication", "internal/session", "internal/command", "internal/valuecodec", "internal/diagnostic", "dberrors", "internal/wire", "internal/charset", "internal/sqltext"},
		"internal/session":             {"config", "internal/wire", "dberrors"},
		"internal/command":             {"internal/session", "internal/wire", "internal/charset", "internal/valuecodec", "internal/sqltext", "dberrors", "config", "internal/streaminput"},
		"internal/sqltext":             {"config", "dberrors"},
		"internal/valueformat":         {"internal/configspec", "internal/wire", "dberrors"},
		"internal/valuecodec":          {"config", "types", "internal/valueformat", "internal/wire", "internal/charset", "dberrors", "internal/configspec"},
		"internal/rowset":              {"config", "internal/wire", "internal/valuecodec", "dberrors"},
		"internal/catalog":             {"internal/rowset", "metadata", "config", "internal/configspec", "internal/sqltext", "internal/command", "internal/session", "internal/valuecodec", "internal/valueformat", "internal/wire", "dberrors"},
		"internal/streaminput":         {"config", "internal/charset", "internal/wire", "dberrors"},
		"internal/parameters":          {"internal/session", "internal/catalog", "internal/configspec", "internal/valueformat", "internal/valuecodec", "internal/streaminput", "internal/sqltext", "config", "types", "internal/charset", "internal/wire", "dberrors"},
		"internal/statement":           {"internal/session", "internal/command", "internal/sqltext", "internal/valuecodec", "internal/streaminput", "internal/wire", "config", "internal/configspec", "dberrors"},
		"internal/lob":                 {"internal/session", "internal/command", "internal/statement", "internal/catalog", "internal/valuecodec", "internal/streaminput", "internal/wire", "config", "dberrors", "internal/charset", "internal/valueformat", "types"},
		"internal/materialize":         {"internal/session", "internal/command", "internal/statement", "internal/catalog", "internal/lob", "internal/rowset", "internal/valuecodec", "internal/wire", "dberrors", "config", "internal/charset", "internal/valueformat", "types"},
		"internal/cursor":              {"internal/session", "internal/command", "internal/statement", "internal/parameters", "internal/catalog", "internal/materialize", "internal/rowset", "internal/sqltext", "internal/wire", "config", "dberrors", "internal/valueformat", "types"},
		"internal/call":                {"internal/session", "internal/command", "internal/statement", "internal/parameters", "internal/catalog", "internal/materialize", "internal/rowset", "internal/valuecodec", "internal/sqltext", "internal/wire", "config", "internal/configspec", "dberrors", "internal/streaminput", "internal/valueformat", "types"},
		"internal/batch":               {"internal/session", "internal/command", "internal/statement", "internal/parameters", "internal/lifetime", "internal/sqltext", "internal/valuecodec", "internal/catalog", "internal/wire", "types", "config", "dberrors"},
		"internal/xa":                  {"internal/session", "internal/command", "internal/statement", "internal/catalog", "internal/materialize", "types", "internal/sqltext", "internal/wire", "config", "dberrors", "internal/valueformat"},
		"internal/lifetime":            {"internal/session", "internal/cursor", "internal/lob", "internal/statement", "dberrors"},
		"internal/control":             {"internal/session", "internal/command", "internal/connect", "internal/statement", "internal/lifetime", "internal/sqltext", "internal/valuecodec", "config", "dberrors", "internal/wire", "internal/authentication", "internal/catalog", "internal/configspec", "internal/cursor", "internal/lob", "internal/valueformat"},
		"internal/diagnostic":          {"config", "internal/wire", "dberrors"},
		"internal/execution":           {"internal/connect", "internal/control", "internal/lifetime", "internal/batch", "internal/call", "internal/cursor", "internal/parameters", "internal/xa", "internal/materialize", "internal/lob", "internal/catalog", "internal/statement", "internal/rowset", "internal/valuecodec", "internal/sqltext", "internal/session", "internal/command", "internal/streaminput", "internal/diagnostic", "config", "types", "metadata", "dberrors", "internal/wire", "internal/charset"},
		"internal/batch/integration":   {},
		"internal/connect/integration": {},
		"internal/control/integration": {},
		"internal/publictest":          {"", "config", "types", "metadata", "dberrors", "internal/catalog", "internal/command", "internal/session", "internal/valuecodec", "internal/valueformat", "internal/wire", "internal/testpeer"},
		"internal/testutil":            {},
		"internal/testpeer":            {"internal/session", "config", "internal/wire", "internal/charset", "internal/testutil"},
		"internal/integrationtest":     {"", "config", "types", "dberrors", "metadata"},
	}
}

func dependencyViolations(module string, packages []listedPackage) []string {
	standard := make(map[string]bool, len(packages))
	for _, p := range packages {
		standard[cleanImport(p.ImportPath)] = p.Standard
	}
	rules := allowedProjectDependencies()
	var violations []string
	for _, p := range packages {
		path := cleanImport(p.ImportPath)
		if path != module && !strings.HasPrefix(path, module+"/") || strings.HasSuffix(path, ".test") {
			continue
		}
		from := strings.TrimSuffix(path, "_test")
		relative := strings.TrimPrefix(strings.TrimPrefix(from, module), "/")
		isTest := p.ForTest != "" || path != from
		permitted, declared := rules[relative]
		if strings.HasPrefix(relative, "internal/") && !declared {
			violations = append(violations, "undeclared internal package: "+from)
		}
		check := func(imports []string, testEdge bool) {
			for _, raw := range imports {
				to := cleanImport(raw)
				project := to == module || strings.HasPrefix(to, module+"/")
				target := strings.TrimPrefix(strings.TrimPrefix(to, module), "/")
				helper := target == "internal/testutil" || target == "internal/testpeer" || target == "internal/integrationtest" || target == "internal/publictest"
				allowed := true
				switch {
				case relative == "internal/testutil":
					allowed = standard[to]
				case helper:
					allowed = testEdge || relative == "internal/publictest" && (target == "internal/testpeer" || target == "internal/testutil")
				case !project:
					allowed = true
				case testEdge:
					allowed = true
				case to == from:
					allowed = true
				case declared:
					allowed = false
					for _, d := range permitted {
						if d == target {
							allowed = true
							break
						}
					}
				case strings.HasPrefix(target, "internal/"):
					allowed = false
				}
				if !allowed {
					violations = append(violations, fmt.Sprintf("forbidden dependency: %s -> %s", path, to))
				}
			}
		}
		check(p.Imports, isTest)
		check(p.TestImports, true)
		check(p.XTestImports, true)
	}
	return violations
}

func TestRootFacadeFiles(t *testing.T) {
	expected := strings.Fields("doc.go driver.go connector.go conn.go stmt.go tx.go config.go types.go errors.go metadata.go obproxy.go execution.go batch.go cursor.go lob.go call.go long_data.go local_infile.go session.go xa.go trace.go escape.go")
	allowed := make(map[string]bool, len(expected))
	for _, name := range expected {
		allowed[name] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file, "_test.go") && !allowed[file] {
			t.Errorf("implementation still in root: %s", file)
		}
	}
	for _, name := range expected {
		if _, err = os.Stat(name); err != nil {
			t.Errorf("missing facade file %s: %v", name, err)
		}
	}
}

func cleanImport(path string) string { clean, _, _ := strings.Cut(path, " "); return clean }
