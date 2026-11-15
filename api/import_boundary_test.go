package api_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nonStoragePackageDirs enumerates all non-binary packages that must stay free of direct database/ORM imports.
var nonStoragePackageDirs = []string{
	"api",
	"auth",
	"server",
	"internal/api",
	"internal/mcp",
	"internal/config",
}

// nonFirebasePackageDirs enumerates packages that must remain decoupled from resumeee-auth-firebase directly.
var nonFirebasePackageDirs = []string{
	"api",
	"server",
	"internal/api",
	"internal/mcp",
	"internal/config",
}

// nonMCPPackageDirs enumerates packages that must remain decoupled from mark3labs/mcp-go directly.
var nonMCPPackageDirs = []string{
	"api",
	"auth",
	"server",
	"internal/api",
	"internal/config",
}

// forbiddenStoragePrefixes lists SQL driver, ORM, and concrete storage package prefixes prohibited outside cmd/*.
var forbiddenStoragePrefixes = []string{
	"database/sql",
	"gorm.io/",
	"github.com/glebarez/",
	"modernc.org/",
	"github.com/mattn/go-sqlite3",
	"github.com/lib/pq",
	"github.com/jackc/pgx",
	"github.com/go-sql-driver/mysql",
	"github.com/resumeee-app/resumeee-storage-sql",
}

// TestImportBoundaries enforces architectural dependency separation across all Go files (including tests):
// - Only cmd/* binaries wire concrete SQL storage libraries.
// - Only auth (and cmd/*) may depend on resumeee-auth-firebase.
// - Only internal/mcp and cmd/mcp may depend directly on github.com/mark3labs/mcp-go.
func TestImportBoundaries(t *testing.T) {
	repoRoot := findRepoRoot(t)
	fset := token.NewFileSet()
	var violations []string

	checkGroup := func(dirs []string, rule string, forbidden []string) {
		for _, relDir := range dirs {
			dir := filepath.Join(repoRoot, filepath.FromSlash(relDir))
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				t.Fatalf("expected package directory %s to exist at %s", relDir, dir)
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("failed to read dir %s: %v", dir, err)
			}

			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
					continue
				}
				filePath := filepath.Join(dir, entry.Name())
				relPath := filepath.Join(relDir, entry.Name())
				fileViolations, err := checkImportBoundaryViolations(fset, relPath, filePath, rule, forbidden)
				if err != nil {
					t.Fatalf("checking imports for %s: %v", relPath, err)
				}
				violations = append(violations, fileViolations...)
			}
		}
	}

	checkGroup(nonStoragePackageDirs, "storage/ORM", forbiddenStoragePrefixes)
	checkGroup(nonFirebasePackageDirs, "firebase auth", []string{"github.com/resumeee-app/resumeee-auth-firebase"})
	checkGroup(nonMCPPackageDirs, "MCP SDK", []string{"github.com/mark3labs/mcp-go"})

	if len(violations) > 0 {
		t.Errorf("found %d import boundary violation(s):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

// TestCheckImportBoundaryViolations proves the import boundary guard is falsifiable against
// forbidden database, ORM, Firebase, and MCP SDK imports while allowing clean domain imports.
func TestCheckImportBoundaryViolations(t *testing.T) {
	fset := token.NewFileSet()

	tests := []struct {
		name         string
		src          string
		rule         string
		forbidden    []string
		wantCount    int
		wantContains string
	}{
		{
			name:         "flags direct gorm import",
			src:          "package api\nimport _ \"gorm.io/gorm\"\n",
			rule:         "storage/ORM",
			forbidden:    forbiddenStoragePrefixes,
			wantCount:    1,
			wantContains: "gorm.io/gorm",
		},
		{
			name:         "flags resumeee-storage-sql subpackage import",
			src:          "package server\nimport _ \"github.com/resumeee-app/resumeee-storage-sql/sql\"\n",
			rule:         "storage/ORM",
			forbidden:    forbiddenStoragePrefixes,
			wantCount:    1,
			wantContains: "resumeee-storage-sql/sql",
		},
		{
			name:         "flags direct database/sql import",
			src:          "package auth\nimport _ \"database/sql\"\n",
			rule:         "storage/ORM",
			forbidden:    forbiddenStoragePrefixes,
			wantCount:    1,
			wantContains: "database/sql",
		},
		{
			name:         "flags direct firebase auth import in internal/api",
			src:          "package api\nimport _ \"github.com/resumeee-app/resumeee-auth-firebase\"\n",
			rule:         "firebase auth",
			forbidden:    []string{"github.com/resumeee-app/resumeee-auth-firebase"},
			wantCount:    1,
			wantContains: "resumeee-auth-firebase",
		},
		{
			name:         "flags direct mcp-go import in server",
			src:          "package server\nimport _ \"github.com/mark3labs/mcp-go/server\"\n",
			rule:         "MCP SDK",
			forbidden:    []string{"github.com/mark3labs/mcp-go"},
			wantCount:    1,
			wantContains: "mark3labs/mcp-go/server",
		},
		{
			name:      "allows core and auth imports",
			src:       "package server\nimport (\n\t\"github.com/resumeee-app/resumeee-core\"\n\t\"github.com/resumeee-app/resumeee-api/auth\"\n)\n",
			rule:      "storage/ORM",
			forbidden: forbiddenStoragePrefixes,
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parser.ParseFile(fset, "synthetic.go", tc.src, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("failed to parse synthetic snippet: %v", err)
			}
			got := findForbiddenImports(f, "synthetic.go", tc.rule, tc.forbidden)
			if len(got) != tc.wantCount {
				t.Fatalf("expected %d violation(s), got %d: %v", tc.wantCount, len(got), got)
			}
			if tc.wantContains != "" && !strings.Contains(strings.Join(got, "\n"), tc.wantContains) {
				t.Errorf("expected violation containing %q, got %v", tc.wantContains, got)
			}
		})
	}
}

// checkImportBoundaryViolations parses imports of filePath and flags matches against forbidden prefixes.
func checkImportBoundaryViolations(fset *token.FileSet, relPath, filePath, rule string, forbidden []string) ([]string, error) {
	f, err := parser.ParseFile(fset, filePath, nil, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", relPath, err)
	}
	return findForbiddenImports(f, relPath, rule, forbidden), nil
}

// findForbiddenImports scans parsed AST import specs and returns boundary violations for forbidden prefixes.
func findForbiddenImports(f *ast.File, relPath, rule string, forbidden []string) []string {
	if f == nil {
		return nil
	}
	var violations []string
	for _, imp := range f.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		for _, prefix := range forbidden {
			trimmed := strings.TrimSuffix(prefix, "/")
			if importPath == trimmed || strings.HasPrefix(importPath, prefix) {
				violations = append(violations, fmt.Sprintf("%s: imports forbidden %s dependency %s", relPath, rule, importPath))
			}
		}
	}
	return violations
}
