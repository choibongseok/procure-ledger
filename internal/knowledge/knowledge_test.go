package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/dbtest"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/migrations"
)

func TestSplitLinesChunksOverlapAndLimits(t *testing.T) {
	short := []repository.KLine{{Heading: "제목", Text: "분기 점검"}, {Heading: "사유", Text: "정기 점검"}}
	ch, tooLong := SplitLines(short, "body", "")
	if tooLong || len(ch) != 1 || !strings.Contains(ch[0].Text, "제목: 분기 점검\n사유: 정기 점검") || ch[0].Heading != "제목" || ch[0].LocatorKind != "body" {
		t.Fatalf("짧은 줄은 한 조각: %+v", ch)
	}
	long := []repository.KLine{{Heading: "본문", Text: strings.Repeat("가나다라마바사 ", 400)}} // 약 2800자
	ch, _ = SplitLines(long, "page", "3")
	if len(ch) < 3 {
		t.Fatalf("긴 줄은 여러 조각: %d", len(ch))
	}
	for i, c := range ch {
		if n := len([]rune(c.Text)); n > ChunkRunes || c.Locator != "3" || c.LocatorKind != "page" {
			t.Fatalf("조각 %d: %d자 %+v", i, n, c.Locator)
		}
	}
	// 겹침: 앞 조각의 끝 100자가 다음 조각 앞에 있다.
	a, b := []rune(ch[0].Text), []rune(ch[1].Text)
	if string(a[len(a)-ChunkOverlap:]) != string(b[:ChunkOverlap]) {
		t.Fatal("조각 겹침이 100자가 아니다")
	}
	// 한도: 200KB 를 넘으면 앞부분만 쓰고 표시한다. 조각 500개 상한.
	huge := make([]repository.KLine, 0, 600)
	for i := 0; i < 600; i++ {
		huge = append(huge, repository.KLine{Heading: "줄", Text: strings.Repeat("나", 700)})
	}
	ch, tooLong = SplitLines(huge, "body", "")
	if !tooLong || len(ch) > MaxChunksOfDoc || len(ch) == 0 {
		t.Fatalf("한도: %d 조각, tooLong=%v", len(ch), tooLong)
	}
	if ContentHash("a", ch) == ContentHash("b", ch) {
		t.Fatal("머리말이 해시에 안 들어감")
	}
}

func TestSnippetCentersOnQueryAndSourceLine(t *testing.T) {
	text := strings.Repeat("앞부분 ", 100) + "장비 교체 절차를 설명한다 " + strings.Repeat("뒷부분 ", 100)
	sn := snippetOf(text, "장비 교체", 200)
	if !strings.Contains(sn, "장비 교체") || len([]rune(sn)) > 210 || !strings.HasPrefix(sn, "…") || !strings.HasSuffix(sn, "…") {
		t.Fatalf("발췌: %q", sn)
	}
	h := domain.KnowledgeHit{Kind: domain.KnowledgeAttachment, Title: "견적서.pdf", DocNo: "AB-2026-0012", LocatorKind: "page", Locator: "3"}
	if got := SourceLine(h); got != "결재 문서 AB-2026-0012 · 견적서.pdf 3쪽" {
		t.Fatalf("첨부 출처: %q", got)
	}
	h = domain.KnowledgeHit{Kind: domain.KnowledgeRegulation, Title: "구매 규정", LocatorKind: "clause", Locator: "제5조", Heading: "금액 구간별 결재선"}
	if got := SourceLine(h); got != "사내 규정 「구매 규정」 제5조 (금액 구간별 결재선)" {
		t.Fatalf("규정 출처: %q", got)
	}
}

type fakeOpener struct {
	open    map[int64]bool
	recheck bool
}

func (f fakeOpener) Openable(_ context.Context, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = f.open[id]
	}
	return out, nil
}
func (f fakeOpener) RelationOpenable(ctx context.Context, ids []int64) (map[int64]bool, error) {
	return f.Openable(ctx, ids)
}
func (f fakeOpener) Recheck(context.Context, int64) (bool, error) { return f.recheck, nil }

func seededService(t *testing.T, op Opener) *Service {
	t.Helper()
	db := dbtest.Open(t)
	if err := migrations.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"가 문서", "나 문서"} {
		id := int64(i + 1)
		if _, err := db.Exec(`INSERT INTO knowledge_docs (id, entity_id, source_kind, source_id, title, doc_no, extract_state, indexed_at) VALUES (?, 1, 'approval_doc', ?, ?, ?, 'ready', 'x')`, id, id, title, "D-"+string(rune('0'+id))); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO knowledge_chunks (doc_id, entity_id, seq, locator_kind, text) VALUES (?, 1, 1, 'body', '장비 교체 절차를 정리한 본문')`, id); err != nil {
			t.Fatal(err)
		}
	}
	txs := contract.SQLTxManager{DB: db}
	return &Service{Txs: txs, DB: db, Audit: fake.NewRecorder(), Masker: fake.Masker{}, Opener: op, Store: NewStore(db)}
}

func ctxOf() context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: 5, Name: "직원", Role: contract.RoleStaff, Channel: contract.ChannelWeb, DepartmentID: 1})
}

// AC-4710: 첫 판정(Openable)을 일부러 열어 두어도 두 번째 확인(Recheck)이 어긋나면 그 결과가 빠진다.
func TestRecheckDropsResultWhenOpenableIsWrong(t *testing.T) {
	svc := seededService(t, fakeOpener{open: map[int64]bool{1: true, 2: true}, recheck: false})
	res, err := svc.Search(ctxOf(), domain.KnowledgeFilter{Query: "장비 교체"})
	if err != nil || len(res.Hits) != 0 {
		t.Fatalf("재확인이 걸러야 한다: %+v %v", res, err)
	}
	svc.Opener = fakeOpener{open: map[int64]bool{1: true}, recheck: true}
	res, err = svc.Search(ctxOf(), domain.KnowledgeFilter{Query: "장비 교체"})
	if err != nil || len(res.Hits) != 1 || res.Hits[0].DocNo != "D-1" {
		t.Fatalf("열 수 있는 문서만: %+v %v", res, err)
	}
	// 시험 훅: 재확인을 서비스 쪽에서 바꿔도 같은 결과(재확인이 최종 관문).
	svc.recheck = func(context.Context, int64) (bool, error) { return false, nil }
	if res, _ = svc.Search(ctxOf(), domain.KnowledgeFilter{Query: "장비 교체"}); len(res.Hits) != 0 {
		t.Fatalf("훅: %+v", res)
	}
}

// 더 보기(Offset)·종류 거름·한도: 건수·점수가 응답에 없고 More 만 있다.
func TestOffsetMoreAndKinds(t *testing.T) {
	svc := seededService(t, fakeOpener{open: map[int64]bool{1: true, 2: true}, recheck: true})
	res, err := svc.Search(ctxOf(), domain.KnowledgeFilter{Query: "장비 교체", Limit: 1})
	if err != nil || len(res.Hits) != 1 || !res.More {
		t.Fatalf("첫 쪽: %+v %v", res, err)
	}
	res2, err := svc.Search(ctxOf(), domain.KnowledgeFilter{Query: "장비 교체", Limit: 1, Offset: 1})
	if err != nil || len(res2.Hits) != 1 || res2.More || res2.Hits[0].DocNo == res.Hits[0].DocNo {
		t.Fatalf("둘째 쪽: %+v %v", res2, err)
	}
	if res, _ = svc.Search(ctxOf(), domain.KnowledgeFilter{Query: "장비 교체", Kinds: []string{domain.KnowledgeManual}}); len(res.Hits) != 0 {
		t.Fatalf("종류 거름: %+v", res)
	}
	rec := svc.Audit.(*fake.Recorder)
	if !rec.Has(ActionSearch, audit.TargetKnowledge, audit.ResultSuccess) {
		t.Fatal("검색 감사 없음")
	}
}

// AC-4711: 검색 서비스 코드에 관계 판정(결재선·참조·부서·소유 조직 비교)을 직접 쓰지 못한다. 열람 판정은 ViewScope 한 곳이다.
func TestSearchCodeDoesNotReimplementViewRules(t *testing.T) {
	banned := regexp.MustCompile(`approval_steps|approval_step_current|document_recipients|document_receipts|department_id|owner_unit_id|dept_visible|requester_id`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if m := banned.Find(b); m != nil {
			t.Errorf("%s: 열람 관계 판정 문자열 %q 은 ViewScope(repository/viewscope.go)에만 있어야 한다", f, m)
		}
	}
}
