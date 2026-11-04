package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const scenarioDocPath = "docs/go-driver-test-tool.md"

var scenarioRowName = regexp.MustCompile("`([a-z0-9-]+)`")

// TestScenarioDocumentation keeps the tool guide's scenario table in sync with
// the registry: a new scenario without a documented row, or a documented row
// whose scenario disappeared, fails the build instead of drifting silently.
func TestScenarioDocumentation(t *testing.T) {
	path := filepath.Join("..", "..", scenarioDocPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	documented := scenarioTableNames(string(raw))
	registered := make(map[string]bool, len(caseDefinitions()))
	for _, definition := range caseDefinitions() {
		registered[definition.name] = true
		if strings.TrimSpace(definition.description) == "" {
			t.Errorf("scenario %q has no description", definition.name)
		}
		if !documented[definition.name] {
			t.Errorf("scenario %q is missing from the %s scenario table", definition.name, scenarioDocPath)
		}
	}
	for name := range documented {
		if !registered[name] {
			t.Errorf("scenario table documents %q but no scenario registers it", name)
		}
	}
}

func scenarioTableNames(text string) map[string]bool {
	names := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "| Scenario |") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		for _, match := range scenarioRowName.FindAllStringSubmatch(cells[1], -1) {
			names[match[1]] = true
		}
	}
	return names
}
