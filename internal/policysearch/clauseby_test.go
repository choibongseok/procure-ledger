package policysearch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/migrations"
)

// ClauseBy: 볼 수 있는 조항은 원문과 출처가 나오고, 볼 수 없는 문서의 조항과 없는 조항은 같은 ErrNotFound·같은 문구다.
func TestClauseByVisibleHiddenAndMissingLookTheSame(t *testing.T) {
	e := newEnv(t, nil)
	h, err := e.svc.ClauseBy(ctxAs(contract.RoleStaff, 1), "approval-authority", "제2조")
	if err != nil {
		t.Fatal(err)
	}
	if h.DocSlug != "approval-authority" || h.DocTitle != "전결 규정" || h.ClauseNo != "제2조" || h.Heading != "지출 결의서의 전결" ||
		!strings.Contains(h.Text, "부서장이 최종 결재") || h.Source != "전결 규정 제2조(지출 결의서의 전결)" {
		t.Fatalf("조항: %+v", h)
	}
	// 증빙 판정 규정은 직원에게 보이지 않는다. 없는 조항과 구분되지 않는다.
	_, hidden := e.svc.ClauseBy(ctxAs(contract.RoleStaff, 1), "evidence-verification", "제1조")
	_, missing := e.svc.ClauseBy(ctxAs(contract.RoleStaff, 1), "evidence-verification", "제99조")
	_, noDoc := e.svc.ClauseBy(ctxAs(contract.RoleStaff, 1), "no-such-doc", "제1조")
	for name, err := range map[string]error{"범위 밖": hidden, "없는 조항": missing, "없는 문서": noDoc} {
		if !errors.Is(err, contract.ErrNotFound) {
			t.Fatalf("%s 는 ErrNotFound 여야 함: %v", name, err)
		}
	}
	if hidden.Error() != missing.Error() || hidden.Error() != noDoc.Error() {
		t.Fatalf("문구가 다르다(존재 여부 노출): %q / %q / %q", hidden, missing, noDoc)
	}
	// 볼 수 있는 역할(재무)은 같은 조항을 읽는다.
	if _, err := e.svc.ClauseBy(ctxAs(contract.RoleFinance, 2), "evidence-verification", "제1조"); err != nil {
		t.Fatalf("재무는 증빙 규정을 읽는다: %v", err)
	}
	// 입력 검증과 감사.
	if _, err := e.svc.ClauseBy(ctxAs(contract.RoleStaff, 1), " ", "제1조"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("빈 slug: %v", err)
	}
	var clauseReads int
	for _, r := range e.rec.Records() {
		if r.Action == ActionSearch && r.TargetType == audit.TargetPolicy && r.TargetID == "clause" {
			clauseReads++
		}
	}
	if clauseReads != 5 {
		t.Fatalf("조항 읽기마다 감사 1건(성공·숨김·없음 모두): %d", clauseReads)
	}
}

// 로그인하지 않은 호출과 규정 검색 권한이 없는 호출은 거부된다.
func TestClauseByNeedsLogin(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.svc.ClauseBy(context.Background(), "approval-authority", "제2조"); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatalf("비로그인: %v", err)
	}
}

// ---- 전결 규정 시드와 문서 대조(0017 시드의 authority_rules 가 가리키는 조항이 문서에 있고 같은 말을 한다) ----

var finalTitle = map[int]string{1: "팀장", 2: "부서장", 3: "본부장", 4: "대표"}

type authorityRow struct {
	form       string
	min, max   int64
	finalLevel int
	slug, no   string
}

func won(n int64) string {
	if n%10000 == 0 {
		return fmt.Sprintf("%d만원", n/10000)
	}
	return fmt.Sprintf("%d원", n)
}

// checkAuthorityClause 는 전결 조항이 양식 이름, 금액 경계, 최종 결재자 직책, "전결" 을 그 규칙 그대로 말하는지 본다.
func checkAuthorityClause(r authorityRow, text string) error {
	if !strings.Contains(text, r.form) {
		return fmt.Errorf("양식 이름 %q 이 없음: %s", r.form, text)
	}
	if !strings.Contains(text, "전결") {
		return fmt.Errorf("전결 표현이 없음: %s", text)
	}
	title := finalTitle[r.finalLevel]
	if !strings.Contains(text, title+"이 최종 결재") {
		return fmt.Errorf("최종 결재자 %q 표현이 없음: %s", title, text)
	}
	for lvl, other := range finalTitle {
		if lvl != r.finalLevel && strings.Contains(text, other) {
			return fmt.Errorf("다른 직책 %q 이 나옴: %s", other, text)
		}
	}
	want := map[int64]bool{r.min: true}
	if r.max != 0 {
		want[r.max] = true
	}
	got := map[int64]bool{}
	for _, m := range amountRe.FindAllStringSubmatch(text, -1) {
		n := parseWon(m[1], m[2])
		if n != 0 {
			got[n] = true
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("금액 표현 %v 가 기대 경계 %v 와 다름: %s", got, want, text)
	}
	for k := range want {
		if !got[k] {
			return fmt.Errorf("금액 경계 %s 가 없음: %s", won(k), text)
		}
	}
	return nil
}

func parseWon(num, unit string) int64 {
	var n int64
	for _, c := range strings.ReplaceAll(num, ",", "") {
		n = n*10 + int64(c-'0')
	}
	switch unit {
	case "만":
		n *= 10000
	case "억":
		n *= 100_000_000
	}
	return n
}

func TestAuthoritySeedMatchesPolicyDoc(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t)
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT f.name, a.min_amount, COALESCE(a.max_amount, 0), a.final_level, a.policy_slug, a.clause_no
		FROM authority_rules a JOIN approval_forms f ON f.id = a.form_id WHERE a.active = 1 AND a.policy_slug <> '' ORDER BY a.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seeds []authorityRow
	for rows.Next() {
		var r authorityRow
		if err := rows.Scan(&r.form, &r.min, &r.max, &r.finalLevel, &r.slug, &r.no); err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, r)
	}
	if len(seeds) == 0 {
		t.Fatal("시드 전결 규정이 있어야 함")
	}
	ix := realIndex(t)
	all := vw("staff", "구매팀")
	var texts []string
	for _, r := range seeds {
		h, ok := ix.Find(r.slug, r.no, all)
		if !ok {
			t.Fatalf("전결 규정 %+v 의 조항 %s/%s 가 문서에 없음", r, r.slug, r.no)
		}
		if err := checkAuthorityClause(r, h.Clause.Text); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, h.Clause.Text)
	}
	// 문서를 고의로 틀리게 만든 사본은 실패해야 한다(검사가 헐겁지 않다).
	r, txt := seeds[0], texts[0]
	for name, patch := range map[string]func(string) string{
		"하한 금액": func(x string) string { return strings.Replace(x, "1000만원 이상", "1500만원 이상", 1) },
		"상한 금액": func(x string) string { return strings.Replace(x, "3000만원 미만", "5000만원 미만", 1) },
		"최종 결재자": func(x string) string {
			return strings.Replace(x, "부서장이 최종 결재", "본부장이 최종 결재", 1)
		},
		"전결 표현": func(x string) string { return strings.ReplaceAll(x, "전결", "생략") },
		"양식 이름": func(x string) string { return strings.Replace(x, "지출 결의서", "구매 품의서", 1) },
	} {
		bad := patch(txt)
		if bad == txt {
			t.Fatalf("%s: 사본이 원문과 같다(시험이 틀림)", name)
		}
		if checkAuthorityClause(r, bad) == nil {
			t.Fatalf("%s: 틀린 문구를 통과시킴: %s", name, bad)
		}
	}
}
