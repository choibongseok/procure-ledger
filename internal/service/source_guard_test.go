package service

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// B21 소스 검사(B20 internal/org/source_guard_test.go 와 같은 방식): 규칙이 코드 구조로 지켜지는지 소스를 훑어 판정한다.
// 대상은 저장소 전체(internal, cmd)의 비테스트 Go 소스다. 주석은 빼고, SQL 은 문자열 리터럴 단위로 본다(공백·개행·별칭에 흔들리지 않게 정규식을 쓴다).
// 각 검사는 소스 맵을 받는 함수이고, 위반을 심은 소스로 "실제로 실패한다"는 자기 검증 테스트가 따로 있다.
//
// 한계(완벽한 방어가 목표가 아니다): 문자열 리터럴과 리터럴끼리의 + 이어 붙이기(go/ast 로 접어 본다)만 SQL 로 본다. 변수·함수 호출·fmt.Sprintf 로
// 조립한 SQL, 다른 저장소 파일의 상수 참조는 보지 못한다. 따라서 이 검사는 "실수로 우회하는 코드"를 잡는 안전망이고, 악의적 우회는 코드 리뷰와
// 검증자의 소스 훑기가 맡는다. 외부 발송 검사(AC-148)는 "알림 생성이 service·repository 두 패키지에서만 일어난다"는 전제 위에 있다.
// 다른 패키지(handler·mcp 등)가 알림 행을 직접 만들면 이 검사가 아니라 위임·알림 서비스 한 곳 규칙(notification_repo 만 INSERT)이 막아야 한다.

// srcFile 은 비테스트 Go 파일 하나다. code 는 주석을 공백으로 지운 소스, literals 는 문자열 리터럴(따옴표 포함 해석 값)이다.
type srcFile struct {
	code     string
	literals []literal
	imports  []string
}

type literal struct {
	text string
	line string // 리터럴이 시작하는 소스 줄 전체(예외 판정용)
}

func parseSource(src string) srcFile {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)
	lines := strings.Split(src, "\n")
	out := srcFile{}
	code := []byte(src)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		off := file.Offset(pos)
		switch tok {
		case token.COMMENT:
			for i := off; i < off+len(lit) && i < len(code); i++ {
				if code[i] != '\n' {
					code[i] = ' '
				}
			}
		case token.STRING:
			text := lit
			if u, err := strconv.Unquote(lit); err == nil {
				text = u
			}
			out.literals = append(out.literals, literal{text: text, line: lines[file.Line(pos)-1]})
		}
	}
	out.code = string(code)
	// 리터럴끼리 + 로 이어 붙인 문자열 상수("department_"+"id = ?")도 한 문자열로 접어 SQL 로 본다.
	if f, err := parser.ParseFile(token.NewFileSet(), "", src, 0); err == nil {
		ast.Inspect(f, func(n ast.Node) bool {
			if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.ADD {
				if folded, ok := foldString(be); ok {
					out.literals = append(out.literals, literal{text: folded, line: lines[fset2line(src, be.Pos())]})
				}
			}
			return true
		})
	}
	if f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly); err == nil {
		for _, im := range f.Imports {
			if p, err := strconv.Unquote(im.Path.Value); err == nil {
				out.imports = append(out.imports, p)
			}
		}
	}
	return out
}

// repoSources 는 저장소의 internal·cmd 아래 모든 비테스트 Go 소스다(키: "internal/service/x.go").
func repoSources(t *testing.T) map[string]srcFile {
	t.Helper()
	out := map[string]srcFile{}
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		base := "internal"
		if strings.HasSuffix(root, "cmd") {
			base = "cmd"
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			out[base+"/"+filepath.ToSlash(rel)] = parseSource(string(b))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// foldString 은 문자열 리터럴과 + 로만 이루어진 식을 한 문자열로 접는다.
func foldString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		u, err := strconv.Unquote(x.Value)
		return u, err == nil
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, ok1 := foldString(x.X)
		r, ok2 := foldString(x.Y)
		return l + r, ok1 && ok2
	case *ast.ParenExpr:
		return foldString(x.X)
	}
	return "", false
}

// fset2line 은 src 안의 위치 pos(파일 집합과 무관한 단순 위치 대신 줄 번호)를 0 기준 줄 인덱스로 바꾼다.
// ParseFile 이 쓴 파일 집합은 이 함수 밖에 있어 pos 를 바이트 오프셋으로 환산한다(파일 하나만 파싱하므로 base 는 1).
func fset2line(src string, pos token.Pos) int {
	off := int(pos) - 1
	if off < 0 {
		off = 0
	}
	if off > len(src) {
		off = len(src)
	}
	return strings.Count(src[:off], "\n")
}

func parseAll(m map[string]string) map[string]srcFile {
	out := map[string]srcFile{}
	for k, v := range m {
		out[k] = parseSource(v)
	}
	return out
}

func isSQL(s string) bool {
	return sqlWord.MatchString(s)
}

var sqlWord = regexp.MustCompile(`(?i)\b(select|update|insert|delete|from|where|join)\b`)

// ── AC-23 ────────────────────────────────────────────────────────────────

var positionInSQL = regexp.MustCompile(`(?i)\bposition\b`)

// positionAllowed 는 users.position 을 읽거나 쓰는 것이 허용된 파일이다. 결재 판정이 아니라 조직 호환 투영(쓰기)·계정 표시·개발 시드다.
var positionAllowed = map[string]string{
	"internal/org/projection.go": "조직 호환 투영(users.position 쓰기)",
	"internal/org/backfill.go":   "0010 이전 데이터의 조직 이관(옛 직책 읽기)",
	"internal/auth/users.go":     "계정 요약 표시(직책 칸은 결재 판정에 쓰이지 않는다)",
	"internal/auth/devseed.go":   "개발 시드 사용자 생성",
}

func checkNoPositionReads(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		if _, ok := positionAllowed[name]; ok {
			continue
		}
		for _, l := range f.literals {
			if isSQL(l.text) && positionInSQL.MatchString(l.text) {
				bad = append(bad, name+": SQL 이 users.position 을 읽는다")
				break
			}
		}
	}
	return bad
}

// AC-23: 결재·위임·구매·재고·조직 미리보기를 포함해 저장소 전체의 SQL 이 users.position 을 읽지 않는다(허용 목록 제외). 직책은 조직 조회에서 얻는다.
func TestApprovalPathsDoNotReadUsersPosition(t *testing.T) {
	for _, b := range checkNoPositionReads(repoSources(t)) {
		t.Error(b)
	}
}

func TestGuardPositionCheckFailsOnViolation(t *testing.T) {
	bad := checkNoPositionReads(parseAll(map[string]string{
		"internal/handler/x.go": "package h\nvar q = `SELECT\n  u.position FROM users u`\n",
		"internal/org/y.go":     "package o\nconst q = \"SELECT id FROM users WHERE position = 'team_lead'\"\n",
	}))
	if len(bad) != 2 {
		t.Fatalf("위반 2건이 잡혀야 함: %v", bad)
	}
	if ok := checkNoPositionReads(parseAll(map[string]string{"internal/h/z.go": "package h\n// position 은 주석\nvar x = `SELECT id FROM users`\n"})); len(ok) != 0 {
		t.Fatalf("위반이 아닌데 잡힘: %v", ok)
	}
}

// ── AC-45a ───────────────────────────────────────────────────────────────

// 부서·귀속 조직을 매개변수와 비교하는 조건(= ?, = :d, IN (…), 공백·개행·별칭 무관).
var viewCompare = regexp.MustCompile(`(?is)\b(?:\w+\.)?(department_id|owner_unit_id)\s*(?:=|<>|!=|\bIN\b|\bIS\b(?:\s+NOT\b)?)\s*(?:\?|:\w+|@\w+|\$\d+|\()`)
var assignStmt = regexp.MustCompile(`(?is)^\s*(update|insert|delete)\b`)
var prTables = regexp.MustCompile(`(?i)\b(purchase_requests|purchase_orders|goods_receipts)\b`)

// isBudgetAggregate 는 예산 사용액·진행 중 합계 집계 SQL(SELECT … SUM(CASE … status …) FROM purchase_requests WHERE …)만 참이다.
func isBudgetAggregate(sql string) bool {
	t := strings.ToLower(sql)
	return strings.HasPrefix(strings.TrimSpace(t), "select") && strings.Count(t, "sum(case when status") == 2 && strings.Contains(t, "budget_month = ?") && !strings.Contains(t, "owner_unit_id")
}

func checkViewConditionOnlyInViewScope(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		if name == "internal/repository/viewscope.go" {
			continue
		}
		for _, l := range f.literals {
			// 예산 저장소(B25)의 부서·월 합계 집계 SQL 한 곳만 예외다(열람 조건이 아니라 집계다. 누가 어느 부서 합계를 보는지는 BudgetService 가 권한으로 정한다).
			// 파일 전체가 아니라 이 집계 리터럴만 빼므로 같은 파일에 다른 부서 조건이 들어오면 잡힌다.
			if name == "internal/repository/budget_repo.go" && isBudgetAggregate(l.text) {
				continue
			}
			if !viewCompare.MatchString(l.text) || assignStmt.MatchString(l.text) {
				continue
			}
			// 호출자가 고른 부서 필터(PRFilter.DepartmentID)는 열람 범위가 아니라 사용자 선택 조건이다.
			if strings.Contains(l.line, "Filter.DepartmentID") {
				continue
			}
			// 구매요청·발주·입고 표를 다루는 저장소 파일, 또는 구매요청 별칭 p 를 쓰는 조각만 대상이다.
			if prTables.MatchString(l.text) || strings.HasPrefix(name, "internal/repository/purchase") || strings.HasPrefix(name, "internal/repository/inventory") ||
				regexp.MustCompile(`\bp\.(department_id|owner_unit_id)\b`).MatchString(l.text) {
				bad = append(bad, name+": 부서·귀속 조직으로 구매요청을 직접 거르는 조건이 있다(ViewScope.Cond 를 쓴다): "+strings.TrimSpace(l.text))
			}
		}
		if strings.HasPrefix(name, "internal/service/") && (strings.Contains(f.code, "scopeDept") || strings.Contains(f.code, "ScopeDept")) {
			bad = append(bad, name+": 옛 부서 범위(scopeDept)를 쓴다")
		}
	}
	return bad
}

// AC-45a: 구매요청(과 그 발주·입고)의 열람 조건은 repository/viewscope.go 의 ViewScope.Cond 한 곳에만 있다.
func TestPurchaseViewConditionLivesInOnePlace(t *testing.T) {
	srcs := repoSources(t)
	for _, b := range checkViewConditionOnlyInViewScope(srcs) {
		t.Error(b)
	}
	// 열람이 걸리는 저장소 조회(구매요청 단건·목록·초안 중복, 발주 단건·목록, 입고 단건)는 모두 같은 Cond 를 부른다.
	need := map[string]int{"internal/repository/purchase_repo.go": 3, "internal/repository/inventory_repo.go": 2}
	for file, min := range need {
		if n := len(regexp.MustCompile(`\.\s*Cond\s*\(\s*\)`).FindAllString(srcs[file].code, -1)); n < min {
			t.Errorf("%s: ViewScope.Cond() 호출이 %d회(기대 %d회 이상)", file, n, min)
		}
	}
}

func TestGuardViewConditionCheckFailsOnViolation(t *testing.T) {
	cases := map[string]string{
		"공백·개행":   "package r\nvar q = `SELECT 1 FROM purchase_requests p WHERE\n   p.department_id\t=   ?`\n",
		"이름 매개변수": "package r\nvar q = `SELECT 1 FROM purchase_requests p WHERE p.department_id = :d`\n",
		"IN 목록":   "package r\nvar q = `SELECT 1 FROM purchase_requests p WHERE owner_unit_id IN (1,2)`\n",
		"서비스 SQL": "package s\nvar q = `SELECT id FROM purchase_orders o JOIN purchase_requests p ON p.id=o.pr_id WHERE p.department_id = ?`\n",
		"옛 범위":    "package s\nfunc f() { scopeDept() }\n",
		"IS ?":    "package r\nvar q = `SELECT 1 FROM purchase_requests p WHERE p.department_id IS ?`\n",
		"이어 붙이기":  "package r\nvar q = \"SELECT 1 FROM purchase_requests p WHERE p.department_\" + \"id = ?\"\n",
	}
	for name, src := range cases {
		path := "internal/repository/other.go"
		if name == "서비스 SQL" || name == "옛 범위" {
			path = "internal/service/other.go"
		}
		if bad := checkViewConditionOnlyInViewScope(parseAll(map[string]string{path: src})); len(bad) == 0 {
			t.Errorf("%s: 위반을 잡지 못함", name)
		}
	}
	ok := parseAll(map[string]string{
		"internal/repository/viewscope.go": "package r\nvar q = `p.department_id = ?`\n",
		"internal/repository/purchase_repo.go": "package r\nvar a = `UPDATE purchase_requests SET owner_unit_id = ?, department_id = ? WHERE id = ?`\n" +
			"func f() { add(\"p.department_id = ?\", f.Filter.DepartmentID) }\n",
		"internal/docverify/s.go": "package d\nvar q = `SELECT 1 FROM documents d WHERE d.department_id = ?`\n",
	})
	if bad := checkViewConditionOnlyInViewScope(ok); len(bad) != 0 {
		t.Fatalf("허용돼야 하는데 잡힘: %v", bad)
	}
}

// ── 위임 생성 한 곳 ───────────────────────────────────────────────────────

var insertDelegation = regexp.MustCompile(`(?is)\b(?:insert|replace)\s+(?:or\s+\w+\s+)?into\s+approval_delegations\b`)
var insertCall = regexp.MustCompile(`\.\s*Insert\s*\(`)
var delegationRef = regexp.MustCompile(`\bSQLDelegations\b|\bDelegations\b`)

func checkDelegationRowsOnlyByService(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		insert := false
		for _, l := range f.literals {
			if insertDelegation.MatchString(l.text) {
				insert = true
			}
		}
		call := delegationRef.MatchString(f.code) && insertCall.MatchString(f.code)
		switch name {
		case "internal/repository/delegation_repo.go":
			if !insert {
				bad = append(bad, name+": 위임 INSERT 가 저장소에 있어야 한다")
			}
		case "internal/service/delegation_service.go":
			if !call {
				bad = append(bad, name+": 위임 저장소 Insert 호출이 있어야 한다")
			}
		default:
			if insert || call {
				bad = append(bad, name+": 위임 서비스 밖에서 위임 행을 만든다(서열 규칙 우회)")
			}
		}
	}
	return bad
}

// 위임 행은 위임 서비스의 Create 한 곳에서만 만들어진다(DB 의 서열 트리거를 제거한 대신 이 구조가 서열 규칙을 지킨다).
func TestDelegationRowsAreCreatedOnlyByDelegationService(t *testing.T) {
	srcs := repoSources(t)
	for _, b := range checkDelegationRowsOnlyByService(srcs) {
		t.Error(b)
	}
	// 서열 판정은 canDelegateTo 한 곳이다. 위임 지정은 그 검사를 거친다.
	if n := len(regexp.MustCompile(`canDelegateTo\s*\(`).FindAllString(srcs["internal/service/delegation_service.go"].code, -1)); n < 3 {
		t.Error("위임 지정(checkCreate)과 후보 목록이 canDelegateTo 한 곳을 거치지 않는다")
	}
}

func TestGuardDelegationCheckFailsOnViolation(t *testing.T) {
	base := map[string]string{
		"internal/repository/delegation_repo.go": "package r\nvar q = `INSERT INTO approval_delegations (a) VALUES (?)`\n",
		"internal/service/delegation_service.go": "package s\nfunc f() { s.Delegations.Insert(ctx) }\n",
	}
	if bad := checkDelegationRowsOnlyByService(parseAll(base)); len(bad) != 0 {
		t.Fatalf("정상 구성이 잡힘: %v", bad)
	}
	for name, src := range map[string]string{
		"공백 둘":      "package h\nvar q = `INSERT   INTO\n approval_delegations (a) VALUES (?)`\n",
		"OR IGNORE": "package h\nvar q = `INSERT OR IGNORE INTO approval_delegations (a) VALUES (?)`\n",
		"호출 공백":     "package h\nfunc f() { d := Delegations\n d .\n Insert(ctx) }\n",
		"REPLACE":   "package h\nvar q = `REPLACE INTO approval_delegations (a) VALUES (?)`\n",
		"이어 붙이기":    "package h\nvar q = \"INSERT INTO approval_\" + \"delegations (a) VALUES (?)\"\n",
	} {
		m := map[string]string{"internal/handler/x.go": src}
		for k, v := range base {
			m[k] = v
		}
		if bad := checkDelegationRowsOnlyByService(parseAll(m)); len(bad) == 0 {
			t.Errorf("%s: 위반을 잡지 못함", name)
		}
	}
}

// ── AC-148 ───────────────────────────────────────────────────────────────

var forbiddenImports = map[string]bool{"net/smtp": true, "net/mail": true, "net/http": true, "net/rpc": true, "net": true, "os/exec": true}
var externalWords = regexp.MustCompile(`(?i)slack|webhook|sendmail|twilio|kakao|smtp`)

func checkNoExternalDelivery(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		if !strings.HasPrefix(name, "internal/service/") && !strings.HasPrefix(name, "internal/repository/") {
			continue
		}
		for _, im := range f.imports {
			if forbiddenImports[im] {
				bad = append(bad, name+": 외부 전송 패키지 "+im+" 를 import 한다")
			}
		}
		if externalWords.MatchString(f.code) {
			bad = append(bad, name+": 외부 발송(메일·메신저·웹훅)을 연상시키는 코드가 있다")
		}
	}
	return bad
}

// AC-148: 알림은 앱 안 알림만이다. 서비스·저장소 패키지 전체가 메일·메신저·네트워크 전송 패키지를 쓰지 않는다.
func TestNotificationCodeHasNoExternalDelivery(t *testing.T) {
	for _, b := range checkNoExternalDelivery(repoSources(t)) {
		t.Error(b)
	}
}

func TestGuardExternalDeliveryCheckFailsOnViolation(t *testing.T) {
	for name, src := range map[string]string{
		"smtp import": "package service\nimport \"net/smtp\"\nvar _ = smtp.SendMail\n",
		"별칭 import":   "package service\nimport h \"net/http\"\nvar _ = h.Get\n",
		"웹훅 호출":       "package repository\nfunc f() { postToSlackWebhook() }\n",
	} {
		path := "internal/service/other.go"
		if strings.HasPrefix(src, "package repository") {
			path = "internal/repository/other.go"
		}
		if bad := checkNoExternalDelivery(parseAll(map[string]string{path: src})); len(bad) == 0 {
			t.Errorf("%s: 위반을 잡지 못함", name)
		}
	}
	if bad := checkNoExternalDelivery(parseAll(map[string]string{"internal/service/ok.go": "package service\nimport \"fmt\"\nvar _ = fmt.Sprint\n"})); len(bad) != 0 {
		t.Fatalf("정상이 잡힘: %v", bad)
	}
}

// ── 첨부 내용 격리(B23 AC-156 7) ───────────────────────────────────────────

var attachContentRe = regexp.MustCompile(`(?is)\bcontent\b.*\bdocument_attachments\b|\bdocument_attachments\b.*\bcontent\b`)

// checkAttachmentContentConfined 는 첨부 내용(content 열)을 읽거나 쓰는 SQL 이 첨부 서비스 한 파일에만 있고, LLM 게이트웨이·MCP 패키지가
// 첨부 표를 아예 건드리지 않는지 본다. 감사 diff 에는 이름·크기·해시 키만 쓴다(첨부 서비스 테스트가 본다).
func checkAttachmentContentConfined(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		for _, l := range f.literals {
			if !strings.Contains(strings.ToLower(l.text), "document_attachments") {
				continue
			}
			switch {
			case name == "internal/service/doc_attach.go":
			case name == "internal/repository/retention_repo.go":
				// 폐기(B29)는 첨부 내용을 읽지 않고 지우기만 한다. 비우는 UPDATE 한 곳이다.
			case name == "internal/repository/knowledge_repo.go":
				// 사내 지식 색인(B47 3단계)은 첨부 내용을 읽어 글만 뽑아 조각으로 저장한다. 원본·전체 글은 어떤 응답에도 나가지 않는다(명세 10절).
			case strings.HasPrefix(name, "internal/llmgw/") || strings.HasPrefix(name, "internal/mcp/") || strings.HasPrefix(name, "internal/mcpusage/"):
				bad = append(bad, name+": LLM·MCP 코드가 첨부 표를 읽는다")
			case attachContentRe.MatchString(l.text):
				bad = append(bad, name+": 첨부 내용 열을 첨부 서비스 밖에서 다룬다")
			}
		}
	}
	return bad
}

func TestAttachmentContentStaysInAttachmentService(t *testing.T) {
	for _, b := range checkAttachmentContentConfined(repoSources(t)) {
		t.Error(b)
	}
}

func TestGuardAttachmentCheckFailsOnViolation(t *testing.T) {
	if bad := checkAttachmentContentConfined(parseAll(map[string]string{
		"internal/mcp/x.go":     "package m\nvar q = `SELECT filename FROM document_attachments`\n",
		"internal/handler/y.go": "package h\nvar q = `SELECT content FROM document_attachments WHERE id = ?`\n",
	})); len(bad) != 2 {
		t.Fatalf("위반 2건이 잡혀야 함: %v", bad)
	}
	if bad := checkAttachmentContentConfined(parseAll(map[string]string{
		"internal/service/doc_attach.go":  "package s\nvar q = `SELECT content FROM document_attachments`\n",
		"internal/service/doc_service.go": "package s\nvar q = `SELECT filename FROM document_attachments`\n",
	})); len(bad) != 0 {
		t.Fatalf("정상이 잡힘: %v", bad)
	}
}

// AC-402(B24): 재고를 바꾸는 원장 쓰기는 한 저장소 파일(inventory_repo.go 의 InsertLedgerWH)만 한다. 다른 곳에서 stock_ledger 에 넣거나 고치지 않는다.
func TestLedgerWritesOnlyInOneRepoFile(t *testing.T) {
	ledgerWrite := regexp.MustCompile(`(?is)\b(insert\s+(or\s+\w+\s+)?into|update|delete\s+from|replace\s+into)\s+stock_ledger\b`)
	n := 0
	for name, f := range repoSources(t) {
		for _, l := range f.literals {
			if ledgerWrite.MatchString(l.text) {
				n++
				if name != "internal/repository/inventory_repo.go" {
					t.Errorf("%s: 원장 쓰기는 inventory_repo.go 의 InsertLedgerWH 한 곳에서만 한다", name)
				}
			}
		}
	}
	if n != 1 {
		t.Errorf("원장 INSERT 가 정확히 한 곳이어야 하는데 %d 곳이다(검사가 낡았거나 쓰기 경로가 늘었다)", n)
	}
}
