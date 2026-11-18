package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) searchIDs(t *testing.T, who int64, f domain.DocSearchFilter) []int64 {
	t.Helper()
	res, err := e.search.Search(as(who), f)
	if err != nil {
		t.Fatalf("검색(%d): %v", who, err)
	}
	var ids []int64
	for _, r := range res.Rows {
		ids = append(ids, r.ID)
	}
	if res.Total != len(ids) {
		t.Fatalf("총 건수 %d, 행 %d", res.Total, len(ids))
	}
	return ids
}

func has(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// AC-810·811: 부분 일치(2자 포함)와 열람 범위: 본인·조직장·전체 열람자는 보이고 다른 부서 직원은 건수도 안 보인다. 임시저장은 기안자 본인만.
func TestDocSearchScopeAndPartialMatch(t *testing.T) {
	e := newEnv(t)
	mine := e.genSubmitted(t, uStaff, "구매 정산 보고")
	other := e.genSubmitted(t, uStaff2, "영업 회의록")
	draft := e.genDraft(t, uStaff, "구매 임시 메모")
	q := domain.DocSearchFilter{Query: "구매"}
	if ids := e.searchIDs(t, uStaff, q); !has(ids, mine.ID) || !has(ids, draft.ID) {
		t.Fatalf("본인 문서(임시저장 포함): %v", ids)
	}
	if ids := e.searchIDs(t, uStaff2, q); len(ids) != 0 {
		t.Fatalf("다른 부서 직원에게 보임: %v", ids)
	}
	if ids := e.searchIDs(t, uLead, q); !has(ids, mine.ID) || has(ids, draft.ID) {
		t.Fatalf("조직장: 하위 직원의 상신 문서는 보이고 임시저장은 안 보여야 함: %v", ids)
	}
	if ids := e.searchIDs(t, uAuditor, domain.DocSearchFilter{Query: "회의록"}); !has(ids, other.ID) {
		t.Fatalf("감사(전체 열람): %v", ids)
	}
	if ids := e.searchIDs(t, uFinance, q); has(ids, draft.ID) {
		t.Fatalf("임시저장은 기안자 본인만: %v", ids)
	}
	// 일치한 곳 이름.
	res, _ := e.search.Search(as(uStaff), q)
	if len(res.Rows) == 0 || res.Rows[0].Matched != "제목" {
		t.Fatalf("일치한 곳: %+v", res.Rows)
	}
}

// AC-810: %·_ 는 문자 그대로 찾는다(E6). 검색어 1자·조건 없음·날짜 오류는 문구로 거부한다.
func TestDocSearchEscapesAndValidation(t *testing.T) {
	e := newEnv(t)
	a := e.genSubmitted(t, uStaff, "100%할인_행사")
	e.genSubmitted(t, uStaff, "1000할인Z행사")
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "100%할인"}); len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("퍼센트: %v", ids)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "인_행"}); len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("밑줄: %v", ids)
	}
	for _, c := range []struct {
		f    domain.DocSearchFilter
		want string
	}{
		{domain.DocSearchFilter{}, "검색어나 조건을 하나 이상 입력하세요."},
		{domain.DocSearchFilter{Query: "가"}, "검색어는 2자 이상 입력하세요."},
		{domain.DocSearchFilter{From: "2026-11-10", To: "2026-11-01"}, "종료일은 시작일 이후여야 합니다."},
	} {
		if _, err := e.search.Search(as(uStaff), c.f); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%+v: %v", c.f, err)
		}
	}
	// 직원은 되고 권한 없는 호출은 거부(검색 권한은 draft:read).
	if _, err := e.search.Search(context.Background(), domain.DocSearchFilter{Query: "구매"}); err == nil {
		t.Fatal("인증 없는 호출이 통과함")
	}
}

// AC-812: 민감 필드(지급 대상)와 첨부는 검색되지 않고, 일반 필드·표 셀은 검색된다. AC-815: 저장 경로가 검색 텍스트를 같은 트랜잭션에서 갱신하고 기동 때 채우기가 멱등이다.
func TestDocSearchSensitiveFieldsExcludedAndRefreshed(t *testing.T) {
	e := newEnv(t)
	docs := e.docs()
	fields := map[string]any{"spend_date": "2026-11-10", "spend_kind": "출장", "payee": "김가나다", "items": []any{map[string]any{"name": "택시요금", "amount": 15000}}, "total": 15000}
	d, err := docs.CreateDraft(as(uStaff), domain.DocDraftInput{FormCode: "EXP", Title: "출장비 정산", Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "김가나다"}); len(ids) != 0 {
		t.Fatalf("민감 필드가 검색됨: %v", ids)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "택시요금"}); !has(ids, d.ID) {
		t.Fatalf("표 셀 값: %v", ids)
	}
	// 수정하면 같은 트랜잭션에서 갱신된다.
	fields["items"] = []any{map[string]any{"name": "기차표", "amount": 15000}}
	if _, err := docs.UpdateDraft(as(uStaff), d.ID, domain.DocDraftInput{FormCode: "EXP", Title: "출장비 정산", Fields: fields}); err != nil {
		t.Fatal(err)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "택시요금"}); len(ids) != 0 {
		t.Fatalf("이전 내용이 남음: %v", ids)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "기차표"}); !has(ids, d.ID) {
		t.Fatalf("수정한 내용: %v", ids)
	}
	// 기동 때 채우기: 비운 뒤 다시 채우면 같은 결과, 한 번 더 불러도 멱등.
	if _, err := e.db.Exec(`UPDATE purchase_requests SET search_text = ''`); err != nil {
		t.Fatal(err)
	}
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Now: func() time.Time { return e.now }}
	for i := 0; i < 2; i++ {
		if err := service.BackfillSearchText(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Query: "기차표"}); !has(ids, d.ID) {
		t.Fatalf("채우기 뒤: %v", ids)
	}
}

// AC-816(성능 기록): 문서 1만 건에서 전체 열람자·직원의 검색 시간을 잰다. 1초를 넘으면 FTS5 후속을 결정한다(명세 D4). 여기서는 넉넉한 3초를 실패 기준으로 둔다.
func TestDocSearchPerformance10k(t *testing.T) {
	e := newEnv(t)
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		who := uStaff
		if i%3 == 0 {
			who = uStaff2
		}
		if _, err := tx.Exec(`INSERT INTO purchase_requests (no, requester_id, department_id, title, reason, status, total_amount, round, created_at, updated_at, search_text)
			VALUES (?,?,1,?, '', 'in_review', 1000, 1, '2026-11-01T00:00:00Z', '2026-11-01T00:00:00Z', ?)`,
			"X-"+time.Now().Format("150405.000000")+"-"+string(rune('a'+i%26))+strings.Repeat("z", i%7)+string(rune('A'+(i/26)%26))+strings.Repeat("y", (i/676)%9)+string(rune('0'+i%10)), who,
			"문서 제목 번호 "+strings.Repeat("가나다", 3), "문서 제목 번호 가나다가나다가나다 구매 요청 본문 내용 입니다 "+strings.Repeat("품목명 ", 20)+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, who := range []int64{uAuditor, uStaff} {
		start := time.Now()
		res, err := e.search.Search(as(who), domain.DocSearchFilter{Query: "본문 내용"})
		el := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("사용자 %d: 1만 건 중 %d건 일치, %v", who, res.Total, el)
		if el > 3*time.Second {
			t.Fatalf("검색이 너무 느림: %v", el)
		}
	}
}

// AC-813: 양식·상태·기안자·기간 조건이 결합된다.
func TestDocSearchFilters(t *testing.T) {
	e := newEnv(t)
	g := e.genSubmitted(t, uStaff, "일반 문서 하나")
	p := e.submitted(t, uStaff, 100_000)
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{FormCode: "GEN"}); len(ids) != 1 || ids[0] != g.ID {
		t.Fatalf("양식: %v", ids)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Statuses: []string{"in_review"}}); !has(ids, g.ID) || !has(ids, p.ID) {
		t.Fatalf("상태: %v", ids)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Statuses: []string{"done"}}); len(ids) != 0 {
		t.Fatalf("완료 상태: %v", ids)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Requester: "직원", From: "2026-11-01", To: "2026-11-30"}); !has(ids, g.ID) {
		t.Fatalf("기안자·기간: %v", ids)
	}
	if ids := e.searchIDs(t, uStaff, domain.DocSearchFilter{Requester: "없는사람"}); len(ids) != 0 {
		t.Fatalf("기안자: %v", ids)
	}
}
