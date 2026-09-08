package httpui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBetaEmittedURLBuildersResolveToRegisteredRoutes(t *testing.T) {
	t.Parallel()

	repository, packageDirectory := betaRouteRepository(t)
	documented := readBetaRouteInventory(t, filepath.Join(repository, "docs", "beta1-routes.md"))
	patterns := make([]string, 0, len(documented))
	seenPatterns := make(map[string]struct{}, len(documented))
	for route := range documented {
		pattern := strings.SplitN(route, " ", 2)[1]
		if _, exists := seenPatterns[pattern]; !exists {
			seenPatterns[pattern] = struct{}{}
			patterns = append(patterns, pattern)
		}
	}

	entries, err := os.ReadDir(packageDirectory)
	if err != nil {
		t.Fatalf("read HTTP package: %v", err)
	}
	emitted := 0
	dynamic := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "url_builder.go" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(packageDirectory, name), nil, 0)
		if err != nil {
			t.Fatalf("parse emitted URL source %s: %v", name, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, ok := selector.X.(*ast.Ident)
			if !ok || receiver.Name != "builder" || !isBetaURLBuilderMethod(selector.Sel.Name) {
				return true
			}
			segments, resolvable := betaEmittedSegments(call, selector.Sel.Name)
			if !resolvable {
				dynamic++
				return true
			}
			emitted++
			if !betaEmittedPathMatches(segments, patterns) {
				t.Fatalf("%s emits an unregistered path shape %q", name, "/"+strings.Join(segments, "/"))
			}
			return true
		})
	}
	if emitted == 0 || dynamic != 13 {
		t.Fatalf("emitted URL scan framing = resolved %d, intentionally data-derived %d; review changed builders", emitted, dynamic)
	}
}

func isBetaURLBuilderMethod(method string) bool {
	switch method {
	case "Path", "PathWithQuery", "PathWithQueryAndFragment", "Absolute", "AbsoluteWithQuery":
		return true
	default:
		return false
	}
}

func betaEmittedSegments(call *ast.CallExpr, method string) ([]string, bool) {
	if len(call.Args) == 0 {
		return nil, method == "Path" || method == "Absolute"
	}
	expressions := call.Args
	if strings.Contains(method, "WithQuery") {
		literal, ok := call.Args[0].(*ast.CompositeLit)
		if !ok {
			return nil, false
		}
		expressions = literal.Elts
	} else if call.Ellipsis.IsValid() {
		return nil, false
	}
	segments := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		literal, ok := expression.(*ast.BasicLit)
		if ok && literal.Kind == token.STRING {
			value, err := strconv.Unquote(literal.Value)
			if err != nil || value == "" || strings.Contains(value, "/") {
				return nil, false
			}
			segments = append(segments, value)
			continue
		}
		segments = append(segments, "*")
	}
	return segments, true
}

func betaEmittedPathMatches(emitted []string, patterns []string) bool {
	for _, pattern := range patterns {
		candidate := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
		if pattern == "/" {
			candidate = nil
		}
		if len(candidate) != len(emitted) {
			continue
		}
		matches := true
		for index := range candidate {
			patternDynamic := strings.HasPrefix(candidate[index], "{") && strings.HasSuffix(candidate[index], "}")
			if emitted[index] != "*" && !patternDynamic && emitted[index] != candidate[index] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
