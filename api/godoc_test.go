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
	"unicode"
	"unicode/utf8"
)

// TestPackageDocCommentConsistency verifies that:
// 1. Every Go package in the repository has a comprehensive package doc comment.
// 2. Exactly ONE file per package directory contains the package doc comment (f.Doc != nil).
// 3. For non-main packages, that file is named <pkgname>.go.
// 4. For package main binaries in cmd/*, that file is main.go.
// 5. The package doc comment starts with "Package <pkgname>".
func TestPackageDocCommentConsistency(t *testing.T) {
	repoRoot := findRepoRoot(t)

	pkgDirs := discoverPackageDirectories(t, repoRoot)
	if len(pkgDirs) == 0 {
		t.Fatalf("expected at least 1 Go package in repository, found 0")
	}

	for _, dir := range pkgDirs {
		relDir, err := filepath.Rel(repoRoot, dir)
		if err != nil {
			relDir = dir
		}

		t.Run(relDir, func(t *testing.T) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("failed to read dir %s: %v", dir, err)
			}

			fset := token.NewFileSet()
			var docFiles []string
			var docTexts []string
			var nonTestFiles []string
			var detectedPkgName string

			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
					continue
				}

				filePath := filepath.Join(dir, entry.Name())
				f, parseErr := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
				if parseErr != nil {
					t.Fatalf("failed to parse %s: %v", filePath, parseErr)
				}

				isTestFile := strings.HasSuffix(entry.Name(), "_test.go")
				if !isTestFile {
					nonTestFiles = append(nonTestFiles, entry.Name())
					if detectedPkgName == "" {
						detectedPkgName = f.Name.Name
					}
				}

				if f.Doc != nil {
					docText := strings.TrimSpace(f.Doc.Text())
					if docText != "" {
						docFiles = append(docFiles, entry.Name())
						docTexts = append(docTexts, docText)
					}
				}
			}

			if len(nonTestFiles) == 0 {
				t.Fatalf("package in %s has no non-test Go source files", relDir)
			}

			if detectedPkgName == "" {
				detectedPkgName = filepath.Base(dir)
			}

			if len(docFiles) == 0 {
				t.Fatalf("package %s (%s) has NO package doc comment", detectedPkgName, relDir)
			}
			if len(docFiles) > 1 {
				t.Fatalf("package %s (%s) has multiple files with package doc comments: %v (expected only 1)",
					detectedPkgName, relDir, docFiles)
			}

			docFile := docFiles[0]
			docText := docTexts[0]

			if strings.HasSuffix(docFile, "_test.go") {
				t.Fatalf("package doc comment for %s is in a test file (%s); must be in non-test file",
					detectedPkgName, docFile)
			}

			expectedFile := detectedPkgName + ".go"
			if detectedPkgName == "main" {
				expectedFile = "main.go"
			}
			if docFile != expectedFile {
				t.Fatalf("package doc comment for %s in %s is in file %s; expected %s",
					detectedPkgName, relDir, docFile, expectedFile)
			}

			expectedPrefix := "Package " + detectedPkgName
			if !strings.HasPrefix(docText, expectedPrefix) {
				t.Fatalf("package doc comment in %s/%s does not start with %q; got: %q",
					relDir, docFile, expectedPrefix, docText)
			}

			if len(docText) < 20 {
				t.Fatalf("package doc comment in %s/%s is too short (%d chars)", relDir, docFile, len(docText))
			}
		})
	}
}

// TestSymbolDocComments enforces that every exported and unexported symbol (function, method,
// type, constant, and package-level variable) plus every grouped const/var block in non-test
// Go files carries a doc comment starting with the exact identifier name.
func TestSymbolDocComments(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pkgDirs := discoverPackageDirectories(t, repoRoot)
	fset := token.NewFileSet()
	var violations []string

	for _, dir := range pkgDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("failed to read dir %s: %v", dir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}

			filePath := filepath.Join(dir, entry.Name())
			relPath, relErr := filepath.Rel(repoRoot, filePath)
			if relErr != nil {
				relPath = filePath
			}

			f, parseErr := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
			if parseErr != nil {
				t.Fatalf("failed to parse %s: %v", relPath, parseErr)
			}

			violations = append(violations, collectFileDocViolations(fset, relPath, f)...)
		}
	}

	if len(violations) > 0 {
		t.Errorf("found %d symbol doc comment violation(s):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

// TestCollectFileDocViolations proves the symbol documentation check is falsifiable against
// undocumented functions, mismatched doc prefixes, undocumented grouped blocks, undocumented
// unexported types, and blank compile-time interface checks.
func TestCollectFileDocViolations(t *testing.T) {
	tests := []struct {
		name         string
		src          string
		wantCount    int
		wantContains string
	}{
		{
			name:         "undocumented unexported function",
			src:          "package p\n\nfunc helper() {}\n",
			wantCount:    1,
			wantContains: `function helper must have a doc comment starting with "helper"`,
		},
		{
			name:         "mismatched doc prefix",
			src:          "package p\n\n// wrong prefix\nfunc helper() {}\n",
			wantCount:    1,
			wantContains: `function helper must have a doc comment starting with "helper"`,
		},
		{
			name:         "undocumented unexported struct",
			src:          "package p\n\ntype cfg struct{}\n",
			wantCount:    1,
			wantContains: `type cfg must have a doc comment starting with "cfg"`,
		},
		{
			name: "undocumented grouped const block and item",
			src: `package p

const (
	foo = 1
)
`,
			wantCount:    2,
			wantContains: "grouped const block must have a leading doc comment",
		},
		{
			name: "blank compile-time interface assertion is ignored",
			src: `package p

type I interface{}
type T struct{}
var _ I = (*T)(nil)
`,
			wantCount:    2, // Missing docs on I and T only; var _ is skipped.
			wantContains: `type I must have a doc comment starting with "I"`,
		},
		{
			name: "fully documented symbols pass",
			src: `package p

// I describes behavior.
type I interface{}

// T implements I.
type T struct{}

// Compile-time check verifies T satisfies I.
var _ I = (*T)(nil)

// Constants for limits.
const (
	// limitOne caps operations.
	limitOne = 1
)

// helper performs internal work.
func helper() {}
`,
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "synthetic.go", tc.src, parser.ParseComments)
			if err != nil {
				t.Fatalf("failed to parse synthetic snippet: %v", err)
			}
			got := collectFileDocViolations(fset, "synthetic.go", f)
			if len(got) != tc.wantCount {
				t.Fatalf("expected %d violation(s), got %d: %v", tc.wantCount, len(got), got)
			}
			if tc.wantContains != "" && !strings.Contains(strings.Join(got, "\n"), tc.wantContains) {
				t.Errorf("expected violation containing %q, got %v", tc.wantContains, got)
			}
		})
	}
}

// hasSymbolDocPrefix reports whether doc is non-nil and starts with name followed by a non-identifier rune.
func hasSymbolDocPrefix(doc *ast.CommentGroup, name string) bool {
	if doc == nil {
		return false
	}
	text := strings.TrimSpace(doc.Text())
	if !strings.HasPrefix(text, name) || len(text) <= len(name) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[len(name):])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
}

// hasNonEmptyDoc reports whether doc contains non-whitespace commentary.
func hasNonEmptyDoc(doc *ast.CommentGroup) bool {
	return doc != nil && strings.TrimSpace(doc.Text()) != ""
}

// collectFileDocViolations inspects all top-level declarations in a single parsed AST file.
func collectFileDocViolations(fset *token.FileSet, relPath string, f *ast.File) []string {
	var violations []string

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !hasSymbolDocPrefix(d.Doc, d.Name.Name) {
				line := fset.Position(d.Pos()).Line
				violations = append(violations, fmt.Sprintf("%s:%d: function %s must have a doc comment starting with %q", relPath, line, d.Name.Name, d.Name.Name))
			}
		case *ast.GenDecl:
			violations = append(violations, collectGenDeclDocViolations(fset, relPath, d)...)
		}
	}

	return violations
}

// collectGenDeclDocViolations checks type, const, and var declarations against AGENTS.md doc rules.
func collectGenDeclDocViolations(fset *token.FileSet, relPath string, decl *ast.GenDecl) []string {
	switch decl.Tok {
	case token.IMPORT:
		return nil
	case token.TYPE:
		return collectTypeDocViolations(fset, relPath, decl)
	case token.CONST:
		return collectValueDeclDocViolations(fset, relPath, decl, "const")
	case token.VAR:
		if allBlankIdents(decl) {
			return nil
		}
		return collectValueDeclDocViolations(fset, relPath, decl, "var")
	default:
		return nil
	}
}

// collectTypeDocViolations verifies doc comments on single or grouped type declarations.
func collectTypeDocViolations(fset *token.FileSet, relPath string, decl *ast.GenDecl) []string {
	var violations []string
	for _, spec := range decl.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		doc := ts.Doc
		if !decl.Lparen.IsValid() && len(decl.Specs) == 1 {
			doc = decl.Doc
		}
		if !hasSymbolDocPrefix(doc, ts.Name.Name) {
			line := fset.Position(ts.Pos()).Line
			violations = append(violations, fmt.Sprintf("%s:%d: type %s must have a doc comment starting with %q", relPath, line, ts.Name.Name, ts.Name.Name))
		}
	}
	return violations
}

// collectValueDeclDocViolations verifies grouped block headers and per-identifier doc comments on const/var declarations.
func collectValueDeclDocViolations(fset *token.FileSet, relPath string, decl *ast.GenDecl, kind string) []string {
	var violations []string
	grouped := decl.Lparen.IsValid()

	if grouped && !hasNonEmptyDoc(decl.Doc) {
		line := fset.Position(decl.Pos()).Line
		violations = append(violations, fmt.Sprintf("%s:%d: grouped %s block must have a leading doc comment", relPath, line, kind))
	}

	for _, spec := range decl.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		doc := decl.Doc
		if grouped {
			doc = vs.Doc
		}
		for _, ident := range vs.Names {
			if ident.Name == "_" {
				continue
			}
			if !hasSymbolDocPrefix(doc, ident.Name) {
				line := fset.Position(ident.Pos()).Line
				violations = append(violations, fmt.Sprintf("%s:%d: %s %s must have a doc comment starting with %q", relPath, line, kind, ident.Name, ident.Name))
			}
		}
	}

	return violations
}

// allBlankIdents reports whether every declared identifier in decl is the blank identifier "_".
func allBlankIdents(decl *ast.GenDecl) bool {
	hasAny := false
	for _, spec := range decl.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, ident := range vs.Names {
			hasAny = true
			if ident.Name != "_" {
				return false
			}
		}
	}
	return hasAny
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if realDir, err := filepath.EvalSymlinks(dir); err == nil {
		dir = realDir
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			realDir, evalErr := filepath.EvalSymlinks(dir)
			if evalErr == nil {
				return realDir
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root containing go.mod from %s", dir)
		}
		dir = parent
	}
}

func discoverPackageDirectories(t *testing.T, repoRoot string) []string {
	t.Helper()
	var pkgDirs []string
	seen := make(map[string]bool)

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "web" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasSuffix(d.Name(), ".go") {
			dir := filepath.Dir(path)
			if !seen[dir] {
				seen[dir] = true
				pkgDirs = append(pkgDirs, dir)
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("failed to walk repository directories: %v", err)
	}

	return pkgDirs
}
