package cicheck

// B33: 권한 판정은 Principal.Can(실효 권한) 한 곳이고, 업무·MCP 코드는 역할 이름을 직접 비교하지 않는다.
// 이 검사는 go/ast 로 역할 직접 비교를 찾는다(정규식은 변형 표기에 약해서 쓰지 않는다):
//   - Role 필드나 역할 상수를 다른 값(문자열 상수·변수·별칭)과 ==·!= 로 비교, 그 값에 대한 switch·case
//   - 역할 표 조회(RolePermissions[...])·RoleHas·ActiveByRole 호출
//   - SQL 문자열 안의 role = 'admin'·role IN ('admin', ...) (작은따옴표·큰따옴표 모두)
//
// 두 Role 필드끼리의 비교(예: 변경 전후 diff)는 하드코딩된 역할 검사가 아니므로 통과한다.
// 허용 목록(roleCompareAllowlist)은 "아직 치환하지 못한 파일"의 래칫이다:
//   - 목록 밖의 새 위반은 실패한다.
//   - 목록의 파일에서 위반이 모두 사라졌는데 목록에 남아 있으면 실패한다(목록은 줄어들기만 한다).
//
// 위반 위치 목록을 보려면: ROLE_COMPARE_REPORT=1 go test ./internal/cicheck -run TestNoRoleStringComparisons -v

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var roleNames = map[string]bool{"admin": true, "finance": true, "manager": true, "staff": true, "auditor": true}

var roleConsts = map[string]bool{"RoleAdmin": true, "RoleFinance": true, "RoleManager": true, "RoleStaff": true, "RoleAuditor": true}

// SQL 문자열 안의 역할 비교(작은·큰따옴표 모두).
var roleSQLRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brole\s*(=|<>|!=)\s*['"](admin|finance|manager|staff|auditor)['"]`),
	regexp.MustCompile(`(?i)\brole\s+(NOT\s+)?IN\s*\(\s*['"](admin|finance|manager|staff|auditor)['"]`),
}

// unwrap 은 괄호·역참조와 string(...)·contract.Role(...) 변환을 벗긴다.
func unwrap(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.CallExpr:
			if len(x.Args) != 1 {
				return e
			}
			switch f := x.Fun.(type) {
			case *ast.Ident:
				if f.Name != "string" && f.Name != "Role" {
					return e
				}
			case *ast.SelectorExpr:
				if f.Sel.Name != "Role" {
					return e
				}
			default:
				return e
			}
			e = x.Args[0]
		default:
			return e
		}
	}
}

// isRoleField 는 식이 어떤 값의 Role 필드(p.Role, string(p.Role), *patch.Role …)인지다.
func isRoleField(e ast.Expr) bool {
	s, ok := unwrap(e).(*ast.SelectorExpr)
	return ok && s.Sel.Name == "Role"
}

// isRoleConst 는 식이 contract.RoleAdmin 같은 역할 상수(또는 그것의 변환)인지다.
func isRoleConst(e ast.Expr) bool {
	switch x := unwrap(e).(type) {
	case *ast.SelectorExpr:
		return roleConsts[x.Sel.Name]
	case *ast.Ident:
		return roleConsts[x.Name]
	}
	return false
}

// isRoleLit 는 식이 역할 이름 문자열 상수("admin" 등, 변환 포함)인지다.
func isRoleLit(e ast.Expr) bool {
	b, ok := unwrap(e).(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(b.Value)
	return err == nil && roleNames[v]
}

// roleValue 는 식이 하드코딩된 역할 값(역할 상수·역할 이름 문자열·그것으로 정한 같은 파일의 변수)인지다.
func roleValue(e ast.Expr, vars map[string]bool) bool {
	if isRoleConst(e) || isRoleLit(e) {
		return true
	}
	id, ok := unwrap(e).(*ast.Ident)
	return ok && vars[id.Name]
}

// roleVars 는 역할 상수·역할 이름으로 값을 정한 변수·상수 이름을 모은다(별칭 우회 방지, 파일 단위).
func roleVars(f *ast.File) map[string]bool {
	vars := map[string]bool{}
	for pass := 0; pass < 2; pass++ { // 별칭의 별칭까지
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				for i, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok && i < len(x.Rhs) && roleValue(x.Rhs[i], vars) {
						vars[id.Name] = true
					}
				}
			case *ast.ValueSpec:
				for i, id := range x.Names {
					if i < len(x.Values) && roleValue(x.Values[i], vars) {
						vars[id.Name] = true
					}
				}
			}
			return true
		})
	}
	return vars
}

// roleCollections 는 역할 값 목록([]contract.Role{...}·[]string{"admin",...})과 역할 키 맵(map[contract.Role]T{...})으로 정한 이름을 모은다.
func roleCollections(f *ast.File, vars map[string]bool) (sets, maps map[string]bool) {
	sets, maps = map[string]bool{}, map[string]bool{}
	isRoleMapType := func(t ast.Expr) bool {
		m, ok := t.(*ast.MapType)
		if !ok {
			return false
		}
		switch k := m.Key.(type) {
		case *ast.SelectorExpr:
			return k.Sel.Name == "Role"
		case *ast.Ident:
			return k.Name == "Role"
		}
		return false
	}
	classify := func(name string, rhs ast.Expr) {
		cl, ok := rhs.(*ast.CompositeLit)
		if !ok {
			return
		}
		if isRoleMapType(cl.Type) {
			maps[name] = true
			return
		}
		for _, el := range cl.Elts {
			if roleValue(el, vars) {
				sets[name] = true
				return
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for i, l := range x.Lhs {
				if id, ok := l.(*ast.Ident); ok && i < len(x.Rhs) {
					classify(id.Name, x.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, id := range x.Names {
				if i < len(x.Values) {
					classify(id.Name, x.Values[i])
				}
			}
		}
		return true
	})
	return sets, maps
}

// scanRoleCompare 는 소스 한 파일에서 역할 직접 비교의 위치("줄: 설명")를 모은다.
func scanRoleCompare(filename string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	vars := roleVars(f)
	sets, maps := roleCollections(f, vars)
	add := func(n ast.Node, what string) {
		out = append(out, fmt.Sprintf("%d: %s", fset.Position(n.Pos()).Line, what))
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BinaryExpr:
			if x.Op != token.EQL && x.Op != token.NEQ {
				return true
			}
			switch {
			case isRoleConst(x.X) || isRoleConst(x.Y):
				add(x, "역할 상수와 비교")
			case (isRoleField(x.X) && roleValue(x.Y, vars)) || (isRoleField(x.Y) && roleValue(x.X, vars)):
				add(x, "Role 필드를 하드코딩된 역할 값과 비교")
			}
		case *ast.SwitchStmt:
			if x.Tag == nil || !isRoleField(x.Tag) {
				// 태그 없는 switch 안의 case 비교는 BinaryExpr 로 잡힌다.
				for _, st := range x.Body.List {
					if cc, ok := st.(*ast.CaseClause); ok {
						for _, e := range cc.List {
							if isRoleConst(e) {
								add(e, "case 역할 상수")
							}
						}
					}
				}
				return true
			}
			for _, st := range x.Body.List {
				if cc, ok := st.(*ast.CaseClause); ok {
					for _, e := range cc.List {
						if roleValue(e, vars) {
							add(e, "Role 필드 switch 의 역할 case")
						}
					}
				}
			}
		case *ast.IndexExpr:
			id := baseName(x.X)
			switch {
			case id == "RolePermissions":
				add(x, "역할 표 조회")
			case maps[id] && (isRoleField(x.Index) || roleValue(x.Index, vars)):
				add(x, "역할 키 맵 조회")
			}
		case *ast.CallExpr:
			id := baseName(x.Fun)
			if id == "RoleHas" || id == "ActiveByRole" {
				add(x, id+" 호출")
			}
			switch {
			case id == "EqualFold" && len(x.Args) == 2 && ((isRoleField(x.Args[0]) && roleValue(x.Args[1], vars)) || (isRoleField(x.Args[1]) && roleValue(x.Args[0], vars))):
				add(x, "strings.EqualFold 로 역할 비교")
			case (id == "Contains" || id == "Index") && len(x.Args) == 2 && isRoleField(x.Args[1]):
				inline := false
				if cl, ok := x.Args[0].(*ast.CompositeLit); ok {
					for _, el := range cl.Elts {
						inline = inline || roleValue(el, vars)
					}
				}
				if sid, ok := x.Args[0].(*ast.Ident); inline || (ok && sets[sid.Name]) {
					add(x, "역할 목록 포함 여부 검사")
				}
			}
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(x.Value)
			if err != nil {
				return true
			}
			for _, re := range roleSQLRes {
				if re.MatchString(s) {
					add(x, "SQL 의 역할 비교")
					break
				}
			}
		}
		return true
	})
	return out, nil
}

func baseName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return ""
}

// 이 파일들은 역할 이름을 정당하게 다룬다: 권한 표의 코드 정본, 템플릿 동기화, 시드·시험 도우미.
var roleCompareExempt = map[string]string{
	"internal/contract/permission.go": "기본 템플릿의 코드 정본(RolePermissions)과 RoleHas",
	"internal/contract/principal.go":  "계산되지 않은 Principal 의 호환 판정(시험·가짜 구현)",
	"internal/access/access.go":       "템플릿 동기화",
	"internal/auth/users.go":          "기본 역할 템플릿의 위험 권한 조회(templateRisk)와 사용자 수정 시 알려진 역할 확인",
	"internal/auth/credentials.go":    "이전 형식(역할 목록) 2단계 정책의 권한 이관(legacyPerms)",
}

// 아직 치환하지 못한 파일. 비어 가는 것이 목표다.
// 임시 허용(ai 가 policysearch·llmgw 를 고치는 중): 아래 경로는 그 변경이 머지되면 지운다(남아 있는데 위반이 없으면 이 시험이 알려 준다).
var roleCompareAllowlist = map[string]string{}

func TestNoRoleStringComparisons(t *testing.T) {
	found := map[string][]string{} // 파일 → "줄: 내용"
	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.Walk(root(t, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root(t), path)
			rel = filepath.ToSlash(rel)
			if _, ok := roleCompareExempt[rel]; ok || strings.HasPrefix(rel, "cmd/eval/") || strings.Contains(rel, "/fake/") || strings.Contains(rel, "/devseed") ||
				strings.HasPrefix(rel, "internal/docverify/kit/") || strings.HasPrefix(rel, "internal/tools/") || strings.HasPrefix(rel, "internal/cicheck/") {
				return nil
			}
			hits, err := scanRoleCompare(path, nil)
			if err != nil {
				t.Errorf("%s 를 읽을 수 없음: %v", rel, err)
				return nil
			}
			if len(hits) > 0 {
				found[rel] = hits
			}
			return nil
		})
	}
	if os.Getenv("ROLE_COMPARE_REPORT") == "1" {
		files := make([]string, 0, len(found))
		for f := range found {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			for _, l := range found[f] {
				t.Logf("%s:%s", f, l)
			}
		}
	}
	for f, lines := range found {
		if _, ok := roleCompareAllowlist[f]; !ok {
			t.Errorf("%s 에 역할 이름 직접 비교가 있음(권한은 Principal.Can/access.PermsOf 로 판정): %s", f, strings.Join(lines, " | "))
		}
	}
	for f := range roleCompareAllowlist {
		if _, ok := found[f]; !ok {
			t.Errorf("%s 는 허용 목록에 있으나 위반이 없음: 목록에서 지우세요(목록은 줄어들기만 합니다)", f)
		}
	}
}

// 검사기 자체의 시험: 검증자가 지적한 변이 4건을 포함해 변형 표기를 잡고, 정당한 코드는 통과시킨다.
func TestRoleCompareScannerCatchesMutations(t *testing.T) {
	wrap := func(body string) string {
		return "package x\nimport \"github.com/choibongseok/procure-ledger/internal/contract\"\nvar _ = contract.RoleAdmin\nfunc f(p contract.Principal, q contract.Principal, db DB, patch *contract.Role) {\n" + body + "\n}\n"
	}
	bad := map[string]string{
		"문자열 변환 비교":           `_ = string(p.Role) == "admin"`,
		"큰따옴표 SQL IN":         "db.Query(`SELECT id FROM users WHERE role IN (\"admin\", \"finance\")`)",
		"큰따옴표 SQL =":          `db.Query("SELECT id FROM users WHERE role = \"admin\"")`,
		"string switch":       "switch string(p.Role) {\ncase \"admin\":\n}",
		"별칭 변수 비교":            "r := contract.RoleAdmin\n_ = p.Role == r",
		"상수 비교":               `_ = p.Role == contract.RoleAdmin`,
		"case 상수":             "switch p.Role {\ncase contract.RoleAdmin:\n}",
		"역할 표 조회":             `_ = contract.RolePermissions[p.Role]`,
		"RoleHas 호출":          `_ = contract.RoleHas(p.Role, "x")`,
		"작은따옴표 SQL":           "db.Query(`SELECT 1 FROM users WHERE role = 'admin'`)",
		"역변환 비교":              `_ = contract.Role("admin") == p.Role`,
		"다른 값과 Role 비교":       `x := "finance"; _ = p.Role != contract.Role(x)`,
		"!= 상수 비교":            `_ = p.Role != contract.RoleStaff`,
		"== string(상수)":       `_ = x == string(contract.RoleAdmin)`,
		"ActiveByRole 호출":     `_ = repo.ActiveByRole(ctx, contract.RoleFinance)`,
		"SQL <> 역할":           "db.Query(`SELECT 1 FROM users WHERE role <> 'staff'`)",
		"SQL != 역할":           "db.Query(`SELECT 1 FROM users WHERE role != 'manager'`)",
		"SQL IN 작은따옴표":        "db.Query(`SELECT 1 FROM users WHERE role IN ('admin','finance')`)",
		"SQL NOT IN":          "db.Query(`SELECT 1 FROM users WHERE role NOT IN (\"auditor\")`)",
		"string(Role) case":   "switch string(p.Role) {\ncase \"finance\", \"admin\":\n}",
		"EqualFold 비교":        `_ = strings.EqualFold(string(p.Role), "admin")`,
		"EqualFold 상수":        `_ = strings.EqualFold(string(contract.RoleAdmin), string(p.Role))`,
		"slices.Contains 인라인": `_ = slices.Contains([]contract.Role{contract.RoleAdmin, contract.RoleFinance}, p.Role)`,
		"slices.Contains 변수":  "admins := []string{\"admin\", \"finance\"}\n_ = slices.Contains(admins, string(p.Role))",
		"맵 조회(변수)":            "m := map[contract.Role]bool{contract.RoleAdmin: true}\n_ = m[p.Role]",
		"맵 조회(상수 키)":          "m := map[contract.Role]string{}\n_ = m[contract.RoleAdmin]",
	}
	for name, body := range bad {
		hits, err := scanRoleCompare("x.go", wrap(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(hits) == 0 {
			t.Errorf("%s: 잡지 못함: %s", name, body)
		}
	}
	good := map[string]string{
		"Role 필드끼리 비교(diff)": `_ = p.Role != q.Role`,
		"포인터 nil 확인":         `_ = patch == nil`,
		"빈 값 확인":             `_ = string(p.Role) == ""`,
		"권한 판정":              `_ = p.Can(contract.PermRoleManage)`,
		"관계 없는 문자열 비교":       `s := "admin"; _ = s == "admin"`,
		"관계 없는 EqualFold":    `_ = strings.EqualFold("a", "A")`,
		"관계 없는 slices":       "names := []string{\"a\", \"b\"}\n_ = slices.Contains(names, \"a\")",
		"권한 맵 조회":            "m := map[contract.Permission]bool{}\n_ = m[contract.PermRoleManage]",
	}
	for name, body := range good {
		hits, err := scanRoleCompare("x.go", wrap(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(hits) != 0 {
			t.Errorf("%s: 잘못 잡음 %v: %s", name, hits, body)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
