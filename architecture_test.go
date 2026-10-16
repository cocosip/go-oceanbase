package oceanbase_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
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
		{"root to wire", module, module + "/internal/wire", false, 0},
		{"wire to root", module + "/internal/wire", module, false, 1},
		{"auth to wire", module + "/internal/auth", module + "/internal/wire", false, 1},
		{"production to helper", module, module + "/internal/testutil", false, 1},
		{"test to helper", module, module + "/internal/testutil", true, 0},
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

func dependencyViolations(module string, packages []listedPackage) []string {
	standard := make(map[string]bool, len(packages))
	for _, p := range packages {
		standard[cleanImport(p.ImportPath)] = p.Standard
	}
	leaves := map[string]bool{}
	for _, name := range []string{"wire", "auth", "routing", "charset"} {
		leaves[module+"/internal/"+name] = true
	}
	helper := module + "/internal/testutil"
	var violations []string
	for _, p := range packages {
		path := cleanImport(p.ImportPath)
		if !strings.HasPrefix(path, module) || strings.HasSuffix(path, ".test") {
			continue
		}
		from := strings.TrimSuffix(path, "_test")
		isTest := p.ForTest != "" || path != from
		if strings.HasPrefix(from, module+"/internal/") && !leaves[from] && from != helper {
			violations = append(violations, "undeclared internal package: "+from)
		}
		check := func(imports []string, testEdge bool) {
			for _, raw := range imports {
				to := cleanImport(raw)
				project := to == module || strings.HasPrefix(to, module+"/")
				allowed := true
				switch {
				case from == helper:
					allowed = standard[to]
				case to == helper:
					allowed = testEdge
				case leaves[from] && project:
					allowed = to == from
				case from == module && project:
					allowed = to == module || leaves[to]
				case strings.HasPrefix(to, module+"/internal/"):
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

func cleanImport(path string) string { clean, _, _ := strings.Cut(path, " "); return clean }
