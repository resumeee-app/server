package openapi_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedProductionAssertionTypes enumerates the concrete value types permitted in runtime
// type assertions across non-test files (strictly for untyped MCP request argument decoding
// and internal request context holders).
var allowedProductionAssertionTypes = map[string]bool{
	"bool":             true,
	"string":           true,
	"map[string]any":   true,
	"*sessionIDHolder": true,
}

// allowedProductionTypeSwitchCases enumerates the additional concrete case types permitted in
// `switch x.(type)` statements across non-test files for untyped MCP tool argument parsing.
var allowedProductionTypeSwitchCases = map[string]bool{
	"nil":      true,
	"int":      true,
	"float64":  true,
	"[]any":    true,
	"[]string": true,
}

// TestNoInterfaceTypeAssertions ensures non-test Go files do not probe optional or
// sidecar interfaces via runtime type assertions or type-switch clauses.
func TestNoInterfaceTypeAssertions(t *testing.T) {
	repoRoot := findRepoRoot(t)
	fset := token.NewFileSet()
	var violations []string

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		relPath, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			relPath = path
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", relPath, readErr)
		}
		fileViolations, collectErr := collectAssertionViolations(fset, relPath, src)
		if collectErr != nil {
			return collectErr
		}
		violations = append(violations, fileViolations...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking repository for interface type assertion check: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("found %d forbidden interface type assertion(s):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

// TestCollectAssertionViolations proves the guard is falsifiable: forbidden named, qualified,
// and inline interface assertions or type-switch cases must be reported, while allowlisted
// boundary types must not.
func TestCollectAssertionViolations(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantCount    int
		wantContains string
	}{
		{
			name:         "named interface assertion",
			body:         `_ = x.(Provider)`,
			wantCount:    1,
			wantContains: "forbidden type assertion to Provider",
		},
		{
			name:         "qualified auth.Provider assertion",
			body:         `_ = x.(auth.Provider)`,
			wantCount:    1,
			wantContains: "forbidden type assertion to auth.Provider",
		},
		{
			name:         "qualified core.TokenVerifier assertion",
			body:         `_ = x.(core.TokenVerifier)`,
			wantCount:    1,
			wantContains: "forbidden type assertion to core.TokenVerifier",
		},
		{
			name:         "inline interface assertion",
			body:         `_ = x.(interface{ AuthenticateRequest() })`,
			wantCount:    1,
			wantContains: "forbidden type assertion to interface{AuthenticateRequest()}",
		},
		{
			name:         "named interface type switch case",
			body:         `switch x.(type) { case Provider: }`,
			wantCount:    1,
			wantContains: "forbidden type switch case Provider",
		},
		{
			name:         "qualified interface type switch case",
			body:         `switch r := x.(type) { case auth.Provider: _ = r }`,
			wantCount:    1,
			wantContains: "forbidden type switch case auth.Provider",
		},
		{
			name:         "inline interface type switch case",
			body:         `switch x.(type) { case interface{ VerifyIDToken() }: }`,
			wantCount:    1,
			wantContains: "forbidden type switch case interface{VerifyIDToken()}",
		},
		{
			name:         "forbidden case mixed into an allowed case list",
			body:         `switch x.(type) { case string, auth.Provider: }`,
			wantCount:    1,
			wantContains: "forbidden type switch case auth.Provider",
		},
		{
			name:      "allowlisted assertions",
			body:      `_ = x.(string); _ = x.(bool); _ = x.(map[string]any); _ = x.(*sessionIDHolder)`,
			wantCount: 0,
		},
		{
			name:      "allowlisted type switch cases including default",
			body:      `switch v := x.(type) { case nil: case string, int, float64, []any, []string: _ = v; default: }`,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nfunc f(x any) {\n\t" + tt.body + "\n}\n"
			got, err := collectAssertionViolations(token.NewFileSet(), "synthetic.go", src)
			if err != nil {
				t.Fatalf("collectAssertionViolations returned error: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("expected %d violation(s), got %d: %v", tt.wantCount, len(got), got)
			}
			if tt.wantContains != "" && !strings.Contains(got[0], tt.wantContains) {
				t.Errorf("expected violation to contain %q, got %q", tt.wantContains, got[0])
			}
		})
	}

	t.Run("unparseable source returns error", func(t *testing.T) {
		if _, err := collectAssertionViolations(token.NewFileSet(), "broken.go", "package p\nfunc {"); err == nil {
			t.Fatal("expected parse error for malformed source")
		}
	})
}

func collectAssertionViolations(fset *token.FileSet, relPath string, src any) ([]string, error) {
	f, err := parser.ParseFile(fset, relPath, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", relPath, err)
	}

	var violations []string
	report := func(pos token.Pos, format string, typeStr string) {
		violations = append(violations, fmt.Sprintf("%s:%d: "+format, relPath, fset.Position(pos).Line, typeStr))
	}

	ast.Inspect(f, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.TypeAssertExpr:
			if n.Type == nil {
				return true
			}
			typeStr := types.ExprString(n.Type)
			if !allowedProductionAssertionTypes[typeStr] {
				report(n.Pos(), "forbidden type assertion to %s; use a cohesive typed interface instead", typeStr)
			}
		case *ast.TypeSwitchStmt:
			for _, stmt := range n.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					typeStr := types.ExprString(expr)
					if !allowedProductionAssertionTypes[typeStr] && !allowedProductionTypeSwitchCases[typeStr] {
						report(expr.Pos(), "forbidden type switch case %s; use a cohesive typed interface or allowlist a concrete boundary type", typeStr)
					}
				}
			}
		}
		return true
	})

	return violations, nil
}
