package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// B42 3단계(erp): 연동 API 의 전표 초안 제출. 작성자는 토큰 발급자이고, 이후 상태 전이·승인·SoD 는 일반 전표와 같은 서비스 경로다.

// apiCtx 는 토큰 발급자 issuer 가 만든 API 토큰 주체다(Perms 는 범위가 주는 것뿐: api:access + api:journal_draft).
func apiCtx(issuer, tokenID int64) context.Context {
	return contract.WithPrincipal(context.Background(), contract.Principal{UserID: issuer, Name: "API(시험)", Role: "api", Channel: contract.ChannelAPI, Auth: contract.AuthAPI, TokenID: tokenID,
		Perms: contract.NewPermSet(contract.PermAPIAccess, contract.PermAPIJournalDraft)})
}

func (e *env) countRows(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func apiDraft(memo string, amount int64) domain.JournalInput {
	return domain.JournalInput{PostingDate: "2026-11-10", Memo: memo, Lines: []domain.JournalLineInput{
		{AccountCode: "5220", Side: "D", Amount: amount}, {AccountCode: "1010", Side: "C", Amount: amount}}}
}

func TestCreateDraftFromAPIIsDraftWithIssuerAsAuthor(t *testing.T) {
	e := newEnv(t)
	j, created, err := e.journals.CreateDraftFromAPI(apiCtx(uFinance, 5), apiDraft("연동", 30_000), "EXT-1")
	if err != nil || !created {
		t.Fatalf("초안 제출: %v %v", err, created)
	}
	if j.Status != domain.JournalDraft || j.Kind != domain.JournalGeneral || j.SourceType != "api" || j.CreatedByID != uFinance {
		t.Fatalf("초안·일반·api 출처·작성자 = 발급자: %+v", j)
	}
	// 같은 토큰·같은 ref·같은 내용은 같은 전표(created=false), 다른 내용은 충돌, 다른 토큰의 같은 ref 는 별개.
	if again, created, err := e.journals.CreateDraftFromAPI(apiCtx(uFinance, 5), apiDraft("연동", 30_000), "EXT-1"); err != nil || created || again.No != j.No {
		t.Fatalf("멱등: %v %v %+v", err, created, again)
	}
	if _, _, err := e.journals.CreateDraftFromAPI(apiCtx(uFinance, 5), apiDraft("연동", 31_000), "EXT-1"); !isErr(err, contract.ErrInvalidState) {
		t.Fatalf("같은 ref 다른 내용은 충돌: %v", err)
	}
	if other, created, err := e.journals.CreateDraftFromAPI(apiCtx(uFinance, 6), apiDraft("연동", 30_000), "EXT-1"); err != nil || !created || other.No == j.No {
		t.Fatalf("다른 토큰의 같은 ref 는 별개 전표: %v %v", err, created)
	}
	// 화면 사용자(ledger:write 가 있어도 api:journal_draft 가 없다)는 이 경로를 쓸 수 없다. 거부는 감사에 남는다.
	if _, _, err := e.journals.CreateDraftFromAPI(as(uFinance), apiDraft("x", 1000), "EXT-2"); !isErr(err, contract.ErrForbidden) {
		t.Fatalf("내부 권한이 없으면 거부: %v", err)
	}
	// 마감된 달·잘못된 입력은 아무것도 남기지 않는다.
	n := e.countRows(t, `SELECT COUNT(*) FROM journals`)
	bad := apiDraft("계정 오류", 100)
	bad.Lines[0].AccountCode = "9999"
	if _, _, err := e.journals.CreateDraftFromAPI(apiCtx(uFinance, 5), bad, "EXT-3"); !isErr(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 계정: %v", err)
	}
	unbal := apiDraft("불균형", 100)
	unbal.Lines[1].Amount = 90
	if _, _, err := e.journals.CreateDraftFromAPI(apiCtx(uFinance, 5), unbal, "EXT-UNBAL"); !isErr(err, contract.ErrValidation) || !strings.Contains(err.Error(), "차변 합계와 대변 합계가 같지 않습니다") {
		t.Fatalf("차대 불균형은 거부: %v", err)
	}
	// 화면의 일반 임시 저장은 불균형도 저장된다(동작 유지).
	if _, err := e.journals.Create(as(uFinance), unbal); err != nil {
		t.Fatalf("일반 임시 저장은 그대로: %v", err)
	}
	n++ // 일반 임시 저장 한 건이 늘었다
	if _, _, err := e.journals.CreateDraftFromAPI(apiCtx(uFinance, 5), apiDraft("ref 없음", 100), "  "); !isErr(err, contract.ErrValidation) {
		t.Fatalf("빈 external_ref: %v", err)
	}
	if e.countRows(t, `SELECT COUNT(*) FROM journals`) != n {
		t.Fatal("거부된 제출이 전표를 남겼다")
	}
	// 감사: 성공은 api.journal_draft(채널 api, 원문 ref 없음·해시만), 거부도 남는다.
	ok, denied := 0, 0
	for _, r := range e.rec.Records() {
		if r.Action != "api.journal_draft" {
			continue
		}
		switch r.Result {
		case "success":
			hash := false
			for _, d := range r.Diff {
				hash = hash || d.Key == "external_ref_hash"
				if d.New == "EXT-1" || d.Old == "EXT-1" {
					t.Fatalf("감사에 external_ref 원문이 있다: %+v", r.Diff)
				}
			}
			if r.Channel == contract.ChannelAPI && hash {
				ok++
			}
		case "denied":
			denied++
		}
	}
	if ok < 2 || denied == 0 {
		t.Fatalf("성공 감사(해시만) 2건 이상과 거부 감사가 있어야 한다: 성공 %d 거부 %d", ok, denied)
	}
}

// 승인은 일반 전표와 같은 훅이 하고, 작성자(= 토큰 발급자)와 승인자가 같으면 B41 SoD(jv.create_approve)가 판정한다. API 경로가 판정을 우회하지 않는다.
func TestAPIDraftApprovalStillGoesThroughSoD(t *testing.T) {
	f := &fakeSoD{force: true, mode: map[string]string{sod.RuleJVCreateApprove: sod.ModeBlock}}
	e := newEnvSoD(t, f)
	e.jvForm(t)
	v := e.vendor(t)
	in := domain.JournalInput{PostingDate: "2026-11-10", Memo: "연동 매입", Lines: []domain.JournalLineInput{
		{AccountCode: "1210", Side: "D", Amount: 50_000}, {AccountCode: "2110", Side: "C", Amount: 50_000, VendorID: v.ID}}}
	j, created, err := e.journals.CreateDraftFromAPI(apiCtx(uFinance, 5), in, "EXT-SOD")
	if err != nil || !created {
		t.Fatal(err, created)
	}
	req, err := e.journals.RequestApproval(as(uFinance), j.No)
	if err != nil {
		t.Fatalf("API 초안도 일반 승인 요청 경로를 탄다: %v", err)
	}
	_, err = e.approval.Decide(as(uFinance2), req.DocID, domain.Decision{Approve: true, Comment: "확인"})
	if err == nil {
		_, err = e.approval.Agree(as(uFinance2), req.DocID, domain.Decision{Approve: true, Comment: "확인"})
	}
	if _, ok := sod.IsBlocked(err); !ok {
		t.Fatalf("전표 승인이 SoD 로 차단되어야 함: %v", err)
	}
	cs := f.rules(sod.RuleJVCreateApprove)
	if len(cs) == 0 || cs[len(cs)-1].Other != uFinance || cs[len(cs)-1].Subject.No != j.No {
		t.Fatalf("판정은 작성자(발급자)를 앞 단계 행위자로 받는다: %+v", cs)
	}
	got, gerr := e.journals.Get(as(uFinance), j.No)
	if gerr != nil || got.Status != domain.JournalRequested {
		t.Fatalf("차단되면 승인되지 않는다: %+v %v", got, gerr)
	}
}
