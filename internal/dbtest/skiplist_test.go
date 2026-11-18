package dbtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 목록 밖의 SkipPG 는 실패하고, 기한이 지난 항목도 실패한다(AC-1741).
func TestCheckSkipAllowed(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if err := checkSkipAllowed("internal/notify", "TestTransitionTableMatchesTrigger", now); err != nil {
		t.Fatalf("목록의 항목이 거절됨: %v", err)
	}
	if err := checkSkipAllowed("internal/notify", "TestTransitionTableMatchesTrigger/하위", now); err != nil {
		t.Fatalf("하위 시험이 거절됨: %v", err)
	}
	if checkSkipAllowed("internal/notify", "TestSomethingElse", now) == nil {
		t.Error("목록 밖 시험이 허용됨")
	}
	if checkSkipAllowed("internal/auth", "TestTransitionTableMatchesTrigger", now) == nil {
		t.Error("다른 패키지의 같은 이름이 허용됨")
	}
	if checkSkipAllowed("internal/notify", "TestTransitionTableMatchesTrigger", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) == nil {
		t.Error("기한이 지난 항목이 허용됨")
	}
}

// 소스의 모든 SkipPG 호출은 허용 목록에 있어야 하고, 목록의 모든 항목은 실제 호출이 있어야 한다(SQLite 모드에서도 정적으로 검사한다).
func TestSkipPGCallsMatchAllowList(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "web", "build", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		pkg := filepath.ToSlash(filepath.Dir(path))
		pkg = strings.TrimPrefix(pkg, "../../")
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "SkipPG" {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "dbtest" {
						found[pkg+"|"+fn.Name.Name] = true
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, s := range AllowedPGSkips {
		if s.Reason == "" || s.Owner == "" || s.Until == "" {
			t.Errorf("%s %s: 사유·담당·기한이 모두 있어야 함", s.Pkg, s.Test)
		}
		if _, err := time.Parse("2006-01-02", s.Until); err != nil {
			t.Errorf("%s %s: 기한 형식 %q", s.Pkg, s.Test, s.Until)
		}
		listed[s.Pkg+"|"+s.Test] = true
	}
	for k := range found {
		if !listed[k] {
			t.Errorf("허용 목록에 없는 SkipPG 호출: %s", k)
		}
	}
	for k := range listed {
		if !found[k] {
			t.Errorf("허용 목록에만 있고 호출이 없는 항목(지우세요): %s", k)
		}
	}
}
