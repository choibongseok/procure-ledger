package service_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/knowledge"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// B47 판정 후속: 업로드 원문에 개인정보 패턴이 있으면 원문 보기·내려받기는 B41 열람 기록을 응답 전에 남기고(fail-closed),
// 업로드 응답에는 개인정보 경고와 색인 대기 안내가 실린다.

const piiMD = "## 제1조 (비상 연락)\n담당자 이메일은 kim.minsu@example.com 이고 휴대전화는 010-1234-5678 이다.\n\n## 제2조 (지급 계좌)\n지급 계좌는 110-123-456789 이다.\n"

func TestKnowledgeUploadWarnsAboutPersonalInfoAndShowsIndexingNotice(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	u := k.upload(t, as(uAdmin), domain.KnowledgeUploadInput{Title: "비상 연락망", Filename: "연락망.md", Content: []byte(piiMD)})
	if len(u.Warnings) != 1 || u.Warnings[0] != knowledge.MsgPIIWarning {
		t.Fatalf("개인정보 경고: %v", u.Warnings)
	}
	if u.State != "pending" || u.Notice != knowledge.MsgIndexing {
		t.Fatalf("색인 대기 안내: state=%q notice=%q", u.State, u.Notice)
	}
	// 개인정보가 없는 파일은 경고가 없다. 색인이 끝난 목록에는 안내도 없다.
	plain := k.upload(t, as(uAdmin), domain.KnowledgeUploadInput{})
	if len(plain.Warnings) != 0 {
		t.Fatalf("경고가 없어야 함: %v", plain.Warnings)
	}
	rows, err := k.admin.Uploads(as(uAdmin))
	if err != nil || len(rows) != 2 {
		t.Fatalf("목록: %v %v", rows, err)
	}
	for _, r := range rows {
		if r.State != "ready" || r.Notice != "" || len(r.Warnings) != 0 {
			t.Fatalf("색인 뒤 목록: %+v", r)
		}
	}
	// 새 버전에도 같은 경고가 실린다.
	nv, err := k.admin.Replace(as(uAdmin), plain.ID, "구매규정.md", []byte(piiMD+"\n## 제3조 (추가)\n추가 연락처는 02-123-4567 이다.\n"))
	if err != nil || len(nv.Warnings) != 1 {
		t.Fatalf("새 버전 경고: %+v %v", nv, err)
	}
}

func TestKnowledgeOpenRecordsPrivacyAccessAndFailsClosed(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	n := &fakeNoter{}
	k.svc.Privacy = n
	pii := k.upload(t, as(uAdmin), domain.KnowledgeUploadInput{Title: "비상 연락망", Filename: "연락망.md", Content: []byte(piiMD)})
	plain := k.upload(t, as(uAdmin), domain.KnowledgeUploadInput{})

	// 개인정보가 없는 문서: 기록 없음.
	if _, err := k.svc.Open(as(uStaff), plain.ID); err != nil || len(n.got) != 0 {
		t.Fatalf("개인정보 없는 문서: %v 기록 %d", err, len(n.got))
	}
	// 있는 문서: 연락처(이메일·전화)와 계좌를 범주별로 한 건씩, 값은 담지 않는다.
	view, err := k.svc.Open(as(uStaff), pii.ID)
	if err != nil || len(view.Sections) == 0 {
		t.Fatalf("원문 보기: %v", err)
	}
	if len(n.got) != 2 {
		t.Fatalf("기록 %d건: %+v", len(n.got), n.got)
	}
	c, a := n.got[0], n.got[1]
	if c.Category != privacy.CatContact || c.Screen != privacy.ScreenKnowledgeDoc || strings.Join(c.Fields, ",") != "email,phone" || c.UnknownSubjects != 2 || len(c.Subjects) != 0 {
		t.Fatalf("연락처 기록: %+v", c)
	}
	if a.Category != privacy.CatAccount || strings.Join(a.Fields, ",") != "account_no" || a.UnknownSubjects != 1 {
		t.Fatalf("계좌 기록: %+v", a)
	}
	if n.by[0].UserID != uStaff {
		t.Fatalf("열람자: %+v", n.by[0])
	}
	// 원본 내려받기도 기록한다(다른 화면 이름).
	n.got = nil
	if _, err := k.svc.File(as(uStaff), pii.ID); err != nil || len(n.got) != 2 || n.got[0].Screen != privacy.ScreenKnowledgeFile {
		t.Fatalf("내려받기 기록: %v %+v", err, n.got)
	}
	// 기록하지 못하면 원문도 원본도 주지 않는다(fail-closed).
	n.fail = errors.New("기록 저장소 장애")
	if v, err := k.svc.Open(as(uStaff), pii.ID); !errors.Is(err, knowledge.ErrPrivacyNote) || len(v.Sections) != 0 {
		t.Fatalf("기록 실패 시 원문을 줌: %+v %v", v, err)
	}
	if f, err := k.svc.File(as(uStaff), pii.ID); !errors.Is(err, knowledge.ErrPrivacyNote) || len(f.Data) != 0 {
		t.Fatalf("기록 실패 시 원본을 줌: %v", err)
	}
	// 개인정보가 없는 문서는 기록이 막혀도 열린다.
	if _, err := k.svc.Open(as(uStaff), plain.ID); err != nil {
		t.Fatalf("개인정보 없는 문서는 열려야 함: %v", err)
	}
	// 기록기가 없는 조립에서는 개인정보가 있는 문서를 열 수 없다.
	k.svc.Privacy = nil
	if _, err := k.svc.Open(as(uStaff), pii.ID); !errors.Is(err, knowledge.ErrPrivacyNote) {
		t.Fatalf("기록기 없음: %v", err)
	}
}
