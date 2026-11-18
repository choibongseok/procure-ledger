package service

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// B31·B32 소스 검사.
//   - AC-1105a: 전표를 승인 상태로 만드는 SQL 은 전표 저장소의 승인 후속(SetStatus)과 지급 전표 승인(ApproveInsertedJournal) 둘뿐이고,
//     ApproveInsertedJournal 은 지급 처리 함수 한 곳(payment_service.go)에서만 부른다.
//   - AC-1130: 전표 행을 쓰는 코드(InsertJournal·자동 전표 insert)는 같은 파일에서 B27 의 월 확인 함수 assertMonthOpen 을 부른다.
//   - AC-1203: 채무 항목의 paid_amount·reserved_amount 를 바꾸는 SQL 은 지급 저장소(payment_repo.go) 한 파일에만 있다.

var journalApproveSQL = regexp.MustCompile(`(?is)update\s+journals\s+set\s+[^;]*\bapproved`)
var payableAmountWrite = regexp.MustCompile(`(?is)update\s+payables\s+set\s+[^;]*\b(paid_amount|reserved_amount)\s*=`)

func checkJournalApprovalPaths(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		for _, l := range f.literals {
			if journalApproveSQL.MatchString(l.text) && name != "internal/repository/ledger_repo.go" && name != "internal/repository/payment_repo.go" {
				bad = append(bad, name+": 전표를 승인 상태로 바꾸는 SQL 이 허용된 저장소 밖에 있다")
			}
		}
		if strings.Contains(f.code, "ApproveInsertedJournal(") && name != "internal/service/payment_service.go" && name != "internal/repository/payment_repo.go" {
			bad = append(bad, name+": 지급 전표 승인 호출이 지급 처리 함수 밖에 있다")
		}
	}
	if f, ok := files["internal/service/payment_service.go"]; ok {
		if n := strings.Count(f.code, "ApproveInsertedJournal("); n != 1 {
			bad = append(bad, "ApproveInsertedJournal 호출은 지급 처리 함수에 정확히 한 번이어야 한다")
		}
	}
	sort.Strings(bad)
	return bad
}

func checkJournalWritesCheckMonth(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		if !strings.HasPrefix(name, "internal/service/") {
			continue
		}
		writes := strings.Contains(f.code, ".InsertJournal(") || strings.Contains(f.code, "repository.JournalInsert{")
		if writes && !strings.Contains(f.code, "assertMonthOpen(") {
			bad = append(bad, name+": 전표를 쓰는데 월 확인(assertMonthOpen)을 부르지 않는다")
		}
	}
	sort.Strings(bad)
	return bad
}

func checkPayableAmountWrites(files map[string]srcFile) []string {
	var bad []string
	for name, f := range files {
		for _, l := range f.literals {
			if payableAmountWrite.MatchString(l.text) && name != "internal/repository/payment_repo.go" {
				bad = append(bad, name+": 채무 항목의 지급 누계·예약을 지급 저장소 밖에서 바꾼다")
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func TestJournalApprovalPathsAreFixed(t *testing.T) {
	if bad := checkJournalApprovalPaths(repoSources(t)); len(bad) > 0 {
		t.Fatalf("%v", bad)
	}
}

func TestJournalWritesCheckMonthOpen(t *testing.T) {
	if bad := checkJournalWritesCheckMonth(repoSources(t)); len(bad) > 0 {
		t.Fatalf("%v", bad)
	}
}

func TestPayableAmountsChangeOnlyInPaymentRepo(t *testing.T) {
	if bad := checkPayableAmountWrites(repoSources(t)); len(bad) > 0 {
		t.Fatalf("%v", bad)
	}
}

func TestGuardJournalChecksFailOnViolation(t *testing.T) {
	approve := parseAll(map[string]string{"internal/service/x.go": "package s\nvar q = `UPDATE journals SET status = 'approved' WHERE id = ?`\nfunc f(){ r.ApproveInsertedJournal(1) }"})
	if len(checkJournalApprovalPaths(approve)) != 2 {
		t.Fatalf("승인 우회를 못 잡는다: %v", checkJournalApprovalPaths(approve))
	}
	month := parseAll(map[string]string{"internal/service/y.go": "package s\nfunc f(){ r.InsertJournal(ctx) }"})
	if len(checkJournalWritesCheckMonth(month)) != 1 {
		t.Fatal("월 확인 누락을 못 잡는다")
	}
	okMonth := parseAll(map[string]string{"internal/service/y.go": "package s\nfunc f(){ r.InsertJournal(ctx); b.assertMonthOpen(ctx) }"})
	if len(checkJournalWritesCheckMonth(okMonth)) != 0 {
		t.Fatal("정상이 잡힘")
	}
	pay := parseAll(map[string]string{"internal/service/z.go": "package s\nvar q = `UPDATE payables SET paid_amount = paid_amount + 1`"})
	if len(checkPayableAmountWrites(pay)) != 1 {
		t.Fatal("채무 누계 우회를 못 잡는다")
	}
}
