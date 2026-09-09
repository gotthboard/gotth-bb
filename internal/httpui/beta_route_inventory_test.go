package httpui

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestBetaRouteInventoryMatchesProductionRegistrations(t *testing.T) {
	t.Parallel()

	repository, packageDirectory := betaRouteRepository(t)
	documented := readBetaRouteInventory(t, filepath.Join(repository, "docs", "beta1-routes.md"))
	registered := extractProductionRoutes(t, packageDirectory)

	for _, route := range []string{
		"GET /login",
		"GET /auth/callback",
		"GET /auth/revalidate",
		"POST /logout",
		"GET /register",
		"GET /registration/admission/{mode}",
		"POST /registration/intake/approval",
		"GET /setup",
		"POST /setup/administrator",
	} {
		addBetaRoute(t, registered, route, "manual production dispatch")
	}
	for _, asset := range []string{
		appStylesheetFilename,
		previousAppStylesheetFilename,
		olderAppStylesheetFilename,
		legacyAppStylesheetFilename,
		"htmx-2.0.10.min.js",
		discoveryResponseFilename,
		markdownToolbarFilename,
	} {
		for _, method := range []string{"GET", "HEAD"} {
			addBetaRoute(t, registered, method+" /static/"+asset, "static production registration")
		}
	}

	if missing := betaRouteDifference(registered, documented); len(missing) != 0 {
		t.Fatalf("production routes missing from Beta inventory: %v", missing)
	}
	if invented := betaRouteDifference(documented, registered); len(invented) != 0 {
		t.Fatalf("Beta inventory contains unregistered production routes: %v", invented)
	}
}

func betaRouteRepository(t *testing.T) (string, string) {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve route inventory test source")
	}
	packageDirectory := filepath.Dir(source)
	return filepath.Clean(filepath.Join(packageDirectory, "..", "..")), packageDirectory
}

func readBetaRouteInventory(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open Beta route inventory: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close Beta route inventory: %v", err)
		}
	}()

	routes := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 9 {
			t.Fatalf("malformed Beta route row: %q", line)
		}
		methods := strings.Trim(strings.TrimSpace(fields[1]), "`")
		pattern := strings.Trim(strings.TrimSpace(fields[2]), "`")
		csrf := strings.TrimSpace(fields[4])
		if pattern == "Pattern" || !strings.HasPrefix(pattern, "/") {
			continue
		}
		for index, field := range fields[3:8] {
			if strings.TrimSpace(field) == "" {
				t.Fatalf("Beta route %s has an empty metadata column %d", pattern, index+3)
			}
		}
		for _, method := range strings.Split(methods, ",") {
			method = strings.TrimSpace(method)
			switch method {
			case "GET", "HEAD", "POST":
			default:
				t.Fatalf("Beta route %s has unsupported method %q", pattern, method)
			}
			if method == "POST" && csrf != "yes" && !(pattern == "/registration/intake/approval" && csrf == "external signed assertion") {
				t.Fatalf("Beta mutation %s %s is not recorded as CSRF-protected", method, pattern)
			}
			if method != "POST" && csrf != "no" {
				t.Fatalf("Beta read %s %s has invalid CSRF metadata %q", method, pattern, csrf)
			}
			addBetaRoute(t, routes, method+" "+pattern, "Beta route inventory")
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan Beta route inventory: %v", err)
	}
	if len(routes) == 0 {
		t.Fatal("Beta route inventory is empty")
	}
	return routes
}

func extractProductionRoutes(t *testing.T, directory string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read HTTP package: %v", err)
	}
	routes := make(map[string]struct{})
	methodRegistrations := 0
	filesParsed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, name), nil, 0)
		if err != nil {
			t.Fatalf("parse production route source %s: %v", name, err)
		}
		filesParsed++
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			method := strings.ToUpper(selector.Sel.Name)
			switch method {
			case "GET", "POST":
				pattern, route := betaRouteLiteral(call)
				if !route {
					return true
				}
				addBetaRoute(t, routes, method+" "+pattern, name)
				return true
			}
			receiver, knownRouter := selector.X.(*ast.Ident)
			if !knownRouter || (receiver.Name != "router" && receiver.Name != "publicRouter" && receiver.Name != "privateRouter") {
				if _, potentialRoute := betaRouteLiteral(call); potentialRoute {
					t.Fatalf("unaccounted path operation %s in %s", selector.Sel.Name, name)
				}
				return true
			}
			switch method {
			case "METHOD":
				methodRegistrations++
			case "USE", "NOTFOUND":
				// Middleware and the fixed missing-route handler do not register a
				// method/path pair.
			case "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE":
				t.Fatalf("unaccounted production route registration method %s in %s", method, name)
			default:
				t.Fatalf("unaccounted router operation %s in %s", selector.Sel.Name, name)
			}
			return true
		})
	}
	if filesParsed == 0 || methodRegistrations != 7 {
		t.Fatalf("production route source framing = files %d, dynamic Method registrations %d", filesParsed, methodRegistrations)
	}
	return routes
}

func betaRouteLiteral(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	pattern, err := strconv.Unquote(literal.Value)
	return pattern, err == nil && strings.HasPrefix(pattern, "/")
}

func addBetaRoute(t *testing.T, routes map[string]struct{}, route, source string) {
	t.Helper()
	if _, exists := routes[route]; exists {
		if source == "Beta route inventory" {
			t.Fatalf("%s repeats %s", source, route)
		}
		return
	}
	routes[route] = struct{}{}
}

func betaRouteDifference(left, right map[string]struct{}) []string {
	difference := make([]string, 0)
	for route := range left {
		if _, exists := right[route]; !exists {
			difference = append(difference, route)
		}
	}
	sort.Strings(difference)
	return difference
}
