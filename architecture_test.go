package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePrefix = "github.com/monet88/antigravity-cloak/"

func parseNonTestGoFiles(t *testing.T, dir string) (*token.FileSet, []*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, f)
	}
	return fset, files
}

func TestArchitectureImportBoundaries(t *testing.T) {
	_, specFiles := parseNonTestGoFiles(t, filepath.Join("internal", "spec"))
	for _, f := range specFiles {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, ".") {
				t.Errorf("internal/spec must use stdlib only, found import %q", path)
			}
		}
	}

	clientEntries, err := os.ReadDir(filepath.Join("internal", "clients"))
	if err != nil {
		t.Fatalf("read internal/clients: %v", err)
	}
	for _, entry := range clientEntries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join("internal", "clients", entry.Name())
		_, files := parseNonTestGoFiles(t, dir)
		for _, f := range files {
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.Contains(path, ".") && path != modulePrefix+"internal/spec" {
					t.Errorf("%s may only import stdlib or internal/spec, found %q", dir, path)
				}
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Recv == nil && fn.Name.IsExported() && fn.Name.Name != "Spec" {
					t.Errorf("%s must stay data-only (only Spec() exported), found func %s", dir, fn.Name.Name)
				}
			}
		}
	}

	_, cloakFiles := parseNonTestGoFiles(t, filepath.Join("internal", "cloak"))
	for _, f := range cloakFiles {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, modulePrefix+"internal/clients/") {
				t.Errorf("internal/cloak must not import client module %q", path)
			}
		}
	}
}

func TestArchitectureCloakExportedSurface(t *testing.T) {
	want := map[string]bool{
		"HandlePluginLifecycle":        true,
		"HandleRequestComplete":        true,
		"HandleRequestInterceptBefore": true,
		"HandleResponseIntercept":      true,
		"HandleStreamChunkIntercept":   true,
		"Initialize":                   true,
		"MustEnvelope":                 true,
		"MustErrorEnvelope":            true,
	}

	_, files := parseNonTestGoFiles(t, filepath.Join("internal", "cloak"))
	got := make(map[string]bool)
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					got[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if name.IsExported() {
								got[name.Name] = true
							}
						}
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							got[s.Name.Name] = true
						}
					}
				}
			}
		}
	}

	for name := range got {
		if !want[name] {
			t.Errorf("unexpected exported symbol in internal/cloak: %s", name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing expected exported symbol in internal/cloak: %s", name)
		}
	}
}

func TestArchitectureNoUnusedProductionDecls(t *testing.T) {
	dirs := []string{
		filepath.Join("internal", "spec"),
		filepath.Join("internal", "cloak"),
	}
	clientEntries, err := os.ReadDir(filepath.Join("internal", "clients"))
	if err != nil {
		t.Fatalf("read internal/clients: %v", err)
	}
	for _, entry := range clientEntries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join("internal", "clients", entry.Name()))
		}
	}

	for _, dir := range dirs {
		fset, files := parseNonTestGoFiles(t, dir)
		decls := make(map[string]token.Pos)
		skipPos := make(map[token.Pos]bool)

		for _, f := range files {
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil && !d.Name.IsExported() && d.Name.Name != "init" && d.Name.Name != "main" && d.Name.Name != "_" {
						decls[d.Name.Name] = d.Name.Pos()
						skipPos[d.Name.Pos()] = true
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if !name.IsExported() && name.Name != "_" {
									decls[name.Name] = name.Pos()
									skipPos[name.Pos()] = true
								}
							}
						case *ast.TypeSpec:
							if !s.Name.IsExported() && s.Name.Name != "_" {
								decls[s.Name.Name] = s.Name.Pos()
								skipPos[s.Name.Pos()] = true
							}
						}
					}
				}
			}

			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					skipPos[x.Sel.Pos()] = true
				case *ast.KeyValueExpr:
					if id, ok := x.Key.(*ast.Ident); ok {
						skipPos[id.Pos()] = true
					}
				case *ast.Field:
					for _, name := range x.Names {
						skipPos[name.Pos()] = true
					}
				}
				return true
			})
		}

		used := make(map[string]bool)
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && !skipPos[id.Pos()] {
					if _, exists := decls[id.Name]; exists {
						used[id.Name] = true
					}
				}
				return true
			})
		}

		var unused []string
		for name, pos := range decls {
			if !used[name] {
				p := fset.Position(pos)
				unused = append(unused, name+" ("+filepath.ToSlash(p.Filename)+")")
			}
		}
		sort.Strings(unused)
		for _, item := range unused {
			t.Errorf("unused unexported production declaration in %s: %s", dir, item)
		}
	}
}

var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
var mdInlineCodeRe = regexp.MustCompile("`[^`]*`")
var mdHeadingRe = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)

func markdownHeadingSlugs(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	slugs := make(map[string]bool)
	counts := make(map[string]int)
	inFence := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := mdHeadingRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		heading := strings.TrimRight(m[1], "# ")
		heading = strings.ReplaceAll(heading, "`", "")
		heading = strings.ToLower(heading)
		var b strings.Builder
		for _, r := range heading {
			switch {
			case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == ' ':
				b.WriteRune(r)
			}
		}
		base := strings.ReplaceAll(strings.TrimSpace(b.String()), " ", "-")
		if base == "" {
			continue
		}
		if n := counts[base]; n == 0 {
			slugs[base] = true
			counts[base] = 1
		} else {
			slugs[base+"-"+strconv.Itoa(n)] = true
			counts[base] = n + 1
		}
	}
	return slugs, nil
}

func TestArchitectureMarkdownLinksAndDocsLayout(t *testing.T) {
	docEntries, err := os.ReadDir("docs")
	if err != nil {
		t.Fatalf("read docs/: %v", err)
	}
	for _, e := range docEntries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			t.Errorf("loose markdown file directly in docs/: %s", e.Name())
		}
	}

	skipDirs := map[string]bool{".git": true, ".ref": true, "dist": true, ".worktrees": true, "worktrees": true}
	var mdFiles []string
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			mdFiles = append(mdFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk markdown files: %v", err)
	}

	slugCache := make(map[string]map[string]bool)
	for _, mdPath := range mdFiles {
		raw, err := os.ReadFile(mdPath)
		if err != nil {
			t.Fatalf("read %s: %v", mdPath, err)
		}
		inFence := false
		for lineNo, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			cleaned := mdInlineCodeRe.ReplaceAllString(line, "")
			for _, m := range mdLinkRe.FindAllStringSubmatch(cleaned, -1) {
				target := m[1]
				if target == "" || strings.HasPrefix(target, "<") ||
					strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
					strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "file://") {
					continue
				}
				filePart, anchorPart, _ := strings.Cut(target, "#")
				resolved := mdPath
				if filePart != "" {
					resolved = filepath.Clean(filepath.Join(filepath.Dir(mdPath), filepath.FromSlash(filePart)))
				}
				if _, err := os.Stat(resolved); err != nil {
					t.Errorf("%s:%d broken relative link %q (%v)", filepath.ToSlash(mdPath), lineNo+1, target, err)
					continue
				}
				if anchorPart != "" && strings.HasSuffix(strings.ToLower(resolved), ".md") {
					slugs, ok := slugCache[resolved]
					if !ok {
						slugs, err = markdownHeadingSlugs(resolved)
						if err != nil {
							t.Errorf("parse headings %s: %v", resolved, err)
							continue
						}
						slugCache[resolved] = slugs
					}
					if !slugs[strings.ToLower(anchorPart)] {
						t.Errorf("%s:%d broken anchor #%s in %s", filepath.ToSlash(mdPath), lineNo+1, anchorPart, filepath.ToSlash(resolved))
					}
				}
			}
		}
	}
}
