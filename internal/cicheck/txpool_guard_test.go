package cicheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 트랜잭션 안 풀 연결 사용 금지(B50 판정 후속). 운영 SQLite 는 연결이 하나라, 트랜잭션이 열린 채 같은 서비스의 풀 핸들(s.DB·s.db)이나
// 자체 풀로 읽는 모듈 확인(Enabled·leaveModuleOn)을 부르면 영구 대기한다(PG 에서는 통과해서 시험이 놓친다). 이 시험은 "트랜잭션 핸들(tx)을 받은 함수"와
// "WithTx 에 넘긴 함수" 의 본문에서 그런 호출을 정적으로 찾는다. 동적 감시는 dbtest.OpenGuarded(cmd/server/b50_txpool_test.go)가 맡는다.
//
// 예외는 txPoolAllowed 에 "파일:함수:식" 으로 사유와 함께 둔다(트랜잭션이 이미 끝난 뒤의 읽기 등). 지금은 비어 있어야 한다.
var txPoolAllowed = map[string]string{}

var txPoolRoots = []string{"internal/service", "internal/org", "internal/privacy", "internal/notify", "internal/repository", "internal/module", "internal/handler"}

func TestNoPoolAccessInsideTransactions(t *testing.T) {
	root := root(t)
	var found []string
	fset := token.NewFileSet()
	for _, dir := range txPoolRoots {
		files, _ := filepath.Glob(filepath.Join(root, dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(root, f)
			for _, d := range af.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				if hasTxParam(fd.Type) {
					found = append(found, scanPool(fset, rel, fd.Name.Name, fd.Body)...)
					continue
				}
				// 본문 안에서 tx 핸들을 받는 함수 리터럴(WithTx 콜백)
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if fl, ok := n.(*ast.FuncLit); ok && hasTxParam(fl.Type) {
						found = append(found, scanPool(fset, rel, fd.Name.Name, fl.Body)...)
						return false
					}
					return true
				})
			}
		}
	}
	sort.Strings(found)
	for _, f := range found {
		key := strings.SplitN(f, " @", 2)[0]
		if _, ok := txPoolAllowed[key]; ok {
			continue
		}
		t.Errorf("트랜잭션 안에서 풀 연결을 쓴다(운영 SQLite 에서 영구 대기): %s\n  tx 핸들로 읽도록 고치세요(module.EnabledTx 등).", f)
	}
}

// hasTxParam 은 이름이 tx 이고 타입이 contract.Tx 인 매개변수가 있는지다.
func hasTxParam(ft *ast.FuncType) bool {
	if ft.Params == nil {
		return false
	}
	for _, p := range ft.Params.List {
		sel, ok := p.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Tx" {
			continue
		}
		for _, n := range p.Names {
			if n.Name == "tx" {
				return true
			}
		}
	}
	return false
}

func scanPool(fset *token.FileSet, rel, fn string, body *ast.BlockStmt) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if x.Sel.Name == "DB" || x.Sel.Name == "db" {
				out = append(out, fmt.Sprintf("%s:%s:%s @%s", rel, fn, exprText(x), fset.Position(x.Pos())))
			}
		case *ast.CallExpr:
			name := ""
			switch f := x.Fun.(type) {
			case *ast.SelectorExpr:
				name = f.Sel.Name
			case *ast.Ident:
				name = f.Name
			}
			if name == "Enabled" || name == "leaveModuleOn" || name == "moduleOn" || name == "EnabledKeys" {
				out = append(out, fmt.Sprintf("%s:%s:%s() @%s", rel, fn, name, fset.Position(x.Pos())))
			}
		}
		return true
	})
	return out
}

func exprText(x *ast.SelectorExpr) string {
	if id, ok := x.X.(*ast.Ident); ok {
		return id.Name + "." + x.Sel.Name
	}
	return "?." + x.Sel.Name
}
