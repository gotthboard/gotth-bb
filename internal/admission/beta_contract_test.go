package admission

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const maximumContractDocumentBytes = 1 << 20

var (
	prdRequirementPattern   = regexp.MustCompile(`\*\*([A-Z]+-[0-9]{3}):\*\*`)
	traceRequirementPattern = regexp.MustCompile("(?m)^\\| `([A-Z]+-[0-9]{3})` \\| ([^|]+) \\| ([^|]+) \\| (implemented|inapplicable|blocked) \\|$")
)

func TestBetaTraceabilityAccountsForEveryProductRequirement(t *testing.T) {
	t.Parallel()

	repository := repositoryRoot(t)
	prd := readContractDocument(t, filepath.Join(repository, "docs", "prd.md"))
	traceability := readContractDocument(t, filepath.Join(repository, "docs", "beta1-traceability.md"))

	product := uniqueRequirementSet(t, "product", prdRequirementPattern.FindAllStringSubmatch(prd, -1))
	traceRows := traceRequirementPattern.FindAllStringSubmatch(traceability, -1)
	traced := uniqueRequirementSet(t, "traceability", traceRows)
	if len(product) == 0 || len(traceRows) == 0 {
		t.Fatal("Beta requirement inventory is empty")
	}

	for _, row := range traceRows {
		if strings.TrimSpace(row[2]) == "" || strings.TrimSpace(row[3]) == "" {
			t.Fatalf("traceability row %s has an empty implementation or evidence reference", row[1])
		}
	}
	if difference := setDifference(product, traced); len(difference) != 0 {
		t.Fatalf("product requirements missing from Beta traceability: %v", difference)
	}
	if difference := setDifference(traced, product); len(difference) != 0 {
		t.Fatalf("Beta traceability invents product requirements: %v", difference)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve admission test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func readContractDocument(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect %s: %v", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumContractDocumentBytes {
		t.Fatalf("contract document %s has invalid mode or size", filepath.Base(path))
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	if strings.IndexByte(string(contents), 0) >= 0 || !strings.HasSuffix(string(contents), "\n") {
		t.Fatalf("contract document %s has invalid text framing", filepath.Base(path))
	}
	return string(contents)
}

func uniqueRequirementSet(t *testing.T, label string, matches [][]string) map[string]struct{} {
	t.Helper()
	set := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		identifier := match[1]
		if _, exists := set[identifier]; exists {
			t.Fatalf("%s repeats requirement %s", label, identifier)
		}
		set[identifier] = struct{}{}
	}
	return set
}

func setDifference(left, right map[string]struct{}) []string {
	difference := make([]string, 0)
	for identifier := range left {
		if _, exists := right[identifier]; !exists {
			difference = append(difference, identifier)
		}
	}
	sort.Strings(difference)
	return difference
}
