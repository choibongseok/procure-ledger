package cicheck

import (
	"encoding/json"
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

// 법인 필터 래칫(B44 명세 8.4, AC-4405). 법인 소유 표를 읽거나 쓰는 SQL 문자열이 entity_id 조건 없이 나가는 곳을 파일별로 센 기준선(entity_baseline.json)이다.
// 새 파일이나 늘어난 수는 실패하고(법인 조건을 빠뜨리면 다른 법인의 데이터가 보인다), 법인 축을 붙여 줄이면 "기준선을 줄이세요"로 실패하므로 같은 커밋에서 갱신한다:
//
//	PL_UPDATE_ENTITY_BASELINE=1 go test ./internal/cicheck -run TestEntityFilterRatchet
//
// 1단계(법인 열이 아직 없다)에서는 현재 상태를 그대로 기준선으로 잡고, 2단계(업무 데이터 법인 축)가 끝날 때 0 이 목표다.
// 정말 법인을 가로지르는 코드(그룹 보고서·마이그레이션·백업)는 기준선에 올리지 말고 entityRatchetExempt 에 사유와 함께 둔다.
const entityBaselineFile = "entity_baseline.json"

// entityOwnedTables 는 법인 소유 표의 최상위 표다(명세 5절). 줄·이력 표는 부모를 따라가므로 세지 않고, 재고 원장은 창고(warehouses)로 법인을 안다.
var entityOwnedTables = []string{
	"journals", "payables", "payment_requests", "bank_accounts", "card_transactions", "budgets", "purchase_requests", "purchase_orders",
	"receipts", "month_closes", "tax_invoices", "import_batches", "warehouses", "corporate_cards", "card_imports", "budget_revisions", "company_assets",
	"stock_valuation", // B51 0104: 평가 줄이 법인 열을 가진다. 기존 읽기·쓰기를 (법인, 품목) 키로 바꾸는 2~3단계 전까지는 기준선에 건수가 남는다
}

// entityRatchetExempt 는 법인 조건 없이 표 전체를 다루는 것이 맞는 디렉터리(사유)다.
var entityRatchetExempt = map[string]string{
	"internal/dbmigrate":     "SQLite→PG 이관은 표를 통째로 복사한다",
	"internal/backup":        "백업·복구는 DB 전체를 다룬다",
	"internal/tools":         "개발 도구",
	"internal/demo":          "데모 데이터 넣기",
	"internal/cicheck":       "검사 자신",
	"internal/onboarding":    "도입 점검표는 설치 전체의 상태를 본다",
	"internal/org":           "사람의 결재 대기 확인(조직 변경·퇴사 판정)은 법인과 무관하게 전체를 본다",
	"internal/scim":          "SCIM 퇴사 처리의 결재 대기 확인은 법인과 무관하게 전체를 본다",
	"internal/privacy":       "열람 기록의 정보주체 이름 조회는 전역이다",
	"internal/docverify/kit": "시험용 픽스처",
	"cmd/eval":               "평가 러너 리그",
}

var sqlVerb = regexp.MustCompile(`(?i)\b(SELECT|UPDATE|INSERT|DELETE)\b`)

func entityTableRef() *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b(FROM|JOIN|UPDATE|INTO)\s+(` + strings.Join(entityOwnedTables, "|") + `)\b`)
}

func entityUnscopedCounts(t *testing.T) map[string]int {
	t.Helper()
	ref := entityTableRef()
	out := map[string]int{}
	for _, dir := range []string{"internal", "cmd"} {
		base := root(t, dir)
		err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root(t), p)
			rel = filepath.ToSlash(rel)
			for ex := range entityRatchetExempt {
				if strings.HasPrefix(rel, ex+"/") {
					return nil
				}
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, nil, 0)
			if perr != nil {
				return perr
			}
			n := entityUnscopedInFile(f, ref)
			if n > 0 {
				out[rel] = n
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestEntityFilterRatchet(t *testing.T) {
	got := entityUnscopedCounts(t)
	path := filepath.Join("entity_baseline.json")
	if os.Getenv("PL_UPDATE_ENTITY_BASELINE") == "1" {
		b, _ := json.MarshalIndent(map[string]any{"tables": entityOwnedTables, "files": got}, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("기준선 파일이 없습니다(PL_UPDATE_ENTITY_BASELINE=1 로 만드세요): %v", err)
	}
	var base struct {
		Files map[string]int `json:"files"`
	}
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	var grew, shrank []string
	files := map[string]bool{}
	for f := range got {
		files[f] = true
	}
	for f := range base.Files {
		files[f] = true
	}
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		switch g, b := got[f], base.Files[f]; {
		case g > b:
			grew = append(grew, fmt.Sprintf("%s: %d → %d", f, b, g))
		case g < b:
			shrank = append(shrank, fmt.Sprintf("%s: %d → %d", f, b, g))
		}
	}
	if len(grew) > 0 {
		t.Errorf("법인 조건(entity_id) 없이 법인 소유 표(%s)를 다루는 SQL 이 늘었습니다. 조건을 붙이거나(contract.EntityScope.Cond), 정말 법인을 가로지르는 코드면 entityRatchetExempt 에 사유와 함께 두세요:\n  %s", strings.Join(entityOwnedTables, ", "), strings.Join(grew, "\n  "))
	}
	if len(shrank) > 0 {
		t.Errorf("법인 조건 없는 SQL 이 줄었습니다. 기준선을 줄이세요(PL_UPDATE_ENTITY_BASELINE=1 go test ./internal/cicheck -run TestEntityFilterRatchet):\n  %s", strings.Join(shrank, "\n  "))
	}
}

// entityScopeCalls 는 법인 조건을 SQL 에 붙이는 도우미 함수 이름이다. SQL 문자열이 이 함수 호출 안에 있거나 같은 `+` 연결식 안에 이 호출이 있으면 법인 조건이 붙은 것으로 센다(문자열 조각에 entity_id 가 없어도).
var entityScopeCalls = map[string]bool{"SQLEntity": true, "SQLEntityID": true, "EntitySQL": true, "ScopeSQL": true, "SQLEntityIn": true}

func callName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

// entityUnscopedInFile 은 한 파일에서 법인 조건 없이 법인 소유 표를 다루는 SQL 단위의 수다. 단위는 `+` 로 이어진 문자열 연결식(또는 혼자 있는 문자열)이다.
func entityUnscopedInFile(f *ast.File, ref *regexp.Regexp) int {
	scopedLits := map[*ast.BasicLit]bool{}
	ast.Inspect(f, func(node ast.Node) bool {
		if c, ok := node.(*ast.CallExpr); ok && (callName(c) == "EntitySQL" || callName(c) == "ScopeSQL") {
			ast.Inspect(c, func(m ast.Node) bool {
				if l, ok := m.(*ast.BasicLit); ok {
					scopedLits[l] = true
				}
				return true
			})
		}
		return true
	})
	consumed := map[ast.Node]bool{}
	n := 0
	judge := func(text string, scoped bool) {
		if sqlVerb.MatchString(text) && ref.MatchString(text) && !scoped && !strings.Contains(text, "entity_id") {
			n++
		}
	}
	ast.Inspect(f, func(node ast.Node) bool {
		if consumed[node] {
			return true
		}
		switch x := node.(type) {
		case *ast.BinaryExpr:
			if x.Op != token.ADD {
				return true
			}
			var sb strings.Builder
			scoped := false
			ast.Inspect(x, func(m ast.Node) bool {
				consumed[m] = true
				switch y := m.(type) {
				case *ast.BasicLit:
					if y.Kind == token.STRING {
						if v, err := strconv.Unquote(y.Value); err == nil {
							sb.WriteString(v)
						}
						if scopedLits[y] {
							scoped = true
						}
					}
				case *ast.CallExpr:
					if entityScopeCalls[callName(y)] {
						scoped = true
					}
				}
				return true
			})
			judge(sb.String(), scoped)
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if v, err := strconv.Unquote(x.Value); err == nil {
					judge(v, scopedLits[x])
				}
			}
		}
		return true
	})
	return n
}
