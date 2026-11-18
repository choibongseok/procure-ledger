package service_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/fake"
	"github.com/choibongseok/procure-ledger/internal/service"
)

func (e *env) personal() *service.PersonalLineService {
	b := service.Base{DB: e.db, Txs: contract.SQLTxManager{DB: e.db}, Audit: e.rec, Masker: fake.Masker{}, Org: leftAwareOrg{e.org}, Now: func() time.Time { return e.now }}
	return service.NewPersonalLineService(b)
}

// AC-140 개인 결재선: 본인 것만 보고 지운다(남의 것은 404), 이름 유일(같은 이름은 덮어쓴다), 최대 30개, 불러올 때 퇴사 대상은 안내와 함께 빠진다.
func TestPersonalLines(t *testing.T) {
	e := newEnv(t)
	p := e.personal()
	edits := []domain.LineEdit{{Role: "agree", TargetType: "user", TargetID: uFinance}, {Role: "cc", TargetType: "user", TargetID: uStaff2}}
	saved, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: "재무 합의", FormCode: "GEN", Edits: edits})
	if err != nil || saved.ID == 0 {
		t.Fatalf("저장: %v", err)
	}
	// 같은 이름은 덮어쓴다(목록에 한 개).
	if _, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: "재무 합의", FormCode: "GEN", Edits: edits[:1]}); err != nil {
		t.Fatal(err)
	}
	list, _ := p.List(as(uStaff))
	if len(list) != 1 || len(list[0].Edits) != 1 {
		t.Fatalf("덮어쓰기: %+v", list)
	}
	// 남의 것: 목록에 없고 불러오기·삭제는 찾을 수 없음.
	if l, _ := p.List(as(uLead)); len(l) != 0 {
		t.Fatal("남의 개인 결재선이 보임")
	}
	if _, _, err := p.Load(as(uLead), saved.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("남의 것 불러오기: %v", err)
	}
	if err := p.Delete(as(uLead), saved.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("남의 것 삭제: %v", err)
	}
	// 이름·역할·개수 검증.
	if _, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: " ", Edits: edits}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("빈 이름: %v", err)
	}
	if _, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: "x", Edits: []domain.LineEdit{{Role: "boss"}}}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("알 수 없는 역할: %v", err)
	}
	for i := 0; i < 29; i++ {
		if _, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: strings.Repeat("가", 1) + string(rune('A'+i%26)) + string(rune('a'+i/26)), Edits: edits}); err != nil {
			t.Fatalf("%d번째 저장: %v", i, err)
		}
	}
	if _, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: "서른한째", Edits: edits}); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("31번째가 저장됨: %v", err)
	}
	// 불러올 때 퇴사한 합의 대상은 빠지고 안내가 남는다.
	if _, err := p.Save(as(uStaff), domain.PersonalLineInput{Name: "퇴사 포함", Edits: edits[:0]}); err == nil {
		_ = err // 30개 한도라 저장되지 않아도 괜찮다(위에서 확인)
	}
	e.deactivate(t, uFinance)
	got, notes, err := p.Load(as(uStaff), saved.ID)
	if err != nil || len(got.Edits) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "재무님은 퇴사했거나 재직 중이 아니어서 결재선에서 뺐습니다.") {
		t.Fatalf("퇴사 대상 제외: %+v %v %v", got.Edits, notes, err)
	}
	// 삭제와 감사.
	if err := p.Delete(as(uStaff), saved.ID); err != nil {
		t.Fatal(err)
	}
	if !e.rec.Has("personal_line.save", "personal_line", "success") || !e.rec.Has("personal_line.delete", "personal_line", "success") {
		t.Fatal("감사 기록 없음")
	}
	// 기안 권한이 없으면 403.
	if _, err := p.List(as(uAuditor)); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("감사자: %v", err)
	}
}

// AC-141 승인·동의 의견은 선택이고 이력에 남는다. 반려·이견은 사유가 필수다(위 병렬 합의 테스트가 이견 사유를 검증한다).
func TestApproveCommentIsOptionalAndRecorded(t *testing.T) {
	e := newEnv(t)
	pr := e.genSubmitted(t, uStaff, "의견")
	decideAs(t, e, uLead, pr.ID, true, "")
	decideAs(t, e, uHead, pr.ID, true, "문제 없습니다")
	hist, _ := e.approval.History(as(uStaff), pr.ID)
	comments := map[domain.ApprovalAction][]string{}
	for _, h := range hist {
		comments[h.Action] = append(comments[h.Action], h.Comment)
	}
	if got := comments[domain.ActionApprove]; len(got) != 2 || got[0] != "" || got[1] != "문제 없습니다" {
		t.Fatalf("승인 의견: %v", got)
	}
	cur, _ := e.docs().Get(as(uStaff), pr.ID)
	if cur.Steps[1].Comment != "문제 없습니다" {
		t.Fatalf("결재선 표 의견: %q", cur.Steps[1].Comment)
	}
}
