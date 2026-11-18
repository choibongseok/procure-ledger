package org

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// B41 ①: 원문(연락처·사번)이 응답에 실리는 org 경로의 열람 기록. 기록기는 시험용 가짜다.

type fakeNoter struct {
	got  []privacy.Access
	by   []contract.Principal
	fail error
}

func (f *fakeNoter) Note(_ context.Context, p contract.Principal, a privacy.Access) error {
	if f.fail != nil {
		return f.fail
	}
	if !privacy.ValidScreen(a.Screen) {
		return fmt.Errorf("허용되지 않은 화면 이름: %q", a.Screen)
	}
	f.got, f.by = append(f.got, a), append(f.by, p)
	return nil
}

func (f *fakeNoter) NoteTx(ctx context.Context, _ contract.Tx, p contract.Principal, a privacy.Access) error {
	return f.Note(ctx, p, a)
}

func (e *env) viewer(uid int64, ch contract.Channel, perms ...contract.Permission) contract.Principal {
	return contract.Principal{UserID: uid, Name: "열람자", Role: contract.RoleStaff, Channel: ch, Perms: contract.NewPermSet(append(perms, contract.PermOrgRead)...)}
}

func privacyEnv(t *testing.T) (*env, *fakeNoter, int64, int64, int64) {
	e := newEnv(t)
	_, _, _, t1, _ := e.baseOrg()
	a := e.reg(Command{Kind: KindHire, Name: "김하나", Email: "kim@pl.test", Role: contract.RoleStaff, Rank: 1, ToUnit: t1, Effective: "2024-01-02", Reason: "입사", Phone: "010-1111-2222"}).UserID
	b := e.reg(Command{Kind: KindHire, Name: "이둘", Email: "lee@pl.test", Role: contract.RoleStaff, Rank: 1, ToUnit: t1, Effective: "2024-01-02", Reason: "입사", Phone: "010-3333-4444"}).UserID
	c := e.reg(Command{Kind: KindHire, Name: "박셋", Email: "park@pl.test", Role: contract.RoleStaff, Rank: 1, ToUnit: t1, Effective: "2024-01-02", Reason: "입사", Phone: "010-5555-6666"}).UserID
	n := &fakeNoter{}
	e.svc.SetPrivacy(n)
	return e, n, a, b, c
}

// 조직도 사람 카드: 다른 사람의 연락처 원문이 실리면 한 번 기록한다. 본인·가린 응답(권한 없음, MCP 채널)은 기록하지 않는다.
func TestPrivacyNoteCard(t *testing.T) {
	e, n, a, b, _ := privacyEnv(t)
	bypass := e.viewer(a, contract.ChannelWeb, contract.PermMaskBypass)
	card, err := e.svc.Card(e.ctx, bypass, b, "")
	if err != nil || card.Phone != "010-3333-4444" {
		t.Fatalf("원문이 나가야 함: %+v %v", card, err)
	}
	if len(n.got) != 1 {
		t.Fatalf("기록 %d건", len(n.got))
	}
	g := n.got[0]
	if g.Category != privacy.CatContact || g.Screen != privacy.ScreenPersonCard || g.Export || len(g.Subjects) != 1 || g.Subjects[0] != (privacy.Subject{Type: privacy.SubjectUser, ID: b}) ||
		strings.Join(g.Fields, ",") != "email,phone" || n.by[0].UserID != a {
		t.Fatalf("기록 내용: %+v 열람자 %d", g, n.by[0].UserID)
	}
	// 기록에 원문 값이 없다.
	if s := fmt.Sprintf("%+v", g); strings.Contains(s, "3333") || strings.Contains(s, "lee@pl.test") {
		t.Fatalf("기록에 원문이 있다: %s", s)
	}
	// 본인 것, 권한 없는 열람자, MCP 채널(권한자도 가림)은 기록 없음.
	n.got = nil
	if _, err := e.svc.Card(e.ctx, bypass, a, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Card(e.ctx, e.viewer(a, contract.ChannelWeb), b, ""); err != nil {
		t.Fatal(err)
	}
	if c, err := e.svc.Card(e.ctx, e.viewer(a, contract.ChannelMCP, contract.PermMaskBypass), b, ""); err != nil || strings.Contains(c.Phone, "3333") {
		t.Fatalf("MCP 는 가려야 함: %+v %v", c, err)
	}
	if len(n.got) != 0 {
		t.Fatalf("기록하지 않아야 하는 경로가 %d건 기록함: %+v", len(n.got), n.got)
	}
}

// 사람 목록: 응답 한 번에 한 번, 원문이 실린 모든 사람(본인 제외)이 정보주체다. 원문 포함 내보내기는 내보내기로 기록한다.
func TestPrivacyNoteListAndExport(t *testing.T) {
	e, n, a, b, c := privacyEnv(t)
	bypass := e.viewer(a, contract.ChannelWeb, contract.PermMaskBypass)
	cards, err := e.svc.SearchPeople(e.ctx, bypass, contract.PeopleQuery{Limit: 50})
	if err != nil || len(cards) < 3 {
		t.Fatalf("목록: %d %v", len(cards), err)
	}
	if len(n.got) != 1 || n.got[0].Screen != privacy.ScreenPeopleList || n.got[0].Export {
		t.Fatalf("목록 기록: %+v", n.got)
	}
	subs := map[int64]bool{}
	for _, s := range n.got[0].Subjects {
		subs[s.ID] = true
	}
	if !subs[b] || !subs[c] || subs[a] || len(subs) != 2 {
		t.Fatalf("정보주체는 본인을 뺀 원문 노출자여야 함: %v", subs)
	}
	// 내보내기: 원문 포함(maskContacts=false).
	n.got = nil
	if _, _, err := e.svc.ListPeople(e.ctx, bypass, contract.PeopleQuery{Limit: 50}, 100, false); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 1 || !n.got[0].Export || n.got[0].Dataset != "org_people" || n.got[0].Screen != privacy.ScreenPeopleExport || len(n.got[0].Subjects) != 2 {
		t.Fatalf("내보내기 기록: %+v", n.got)
	}
	// 가린 내보내기는 기록 없음.
	n.got = nil
	if _, _, err := e.svc.ListPeople(e.ctx, bypass, contract.PeopleQuery{Limit: 50}, 100, true); err != nil || len(n.got) != 0 {
		t.Fatalf("가린 내보내기: %v 기록 %d", err, len(n.got))
	}
}

// 기록하지 못하면 원문을 싣지 않고 실패한다(조용히 마스킹으로 내리지 않는다).
func TestPrivacyNoteFailClosed(t *testing.T) {
	e, n, a, b, _ := privacyEnv(t)
	n.fail = errors.New("디스크 가득 참")
	bypass := e.viewer(a, contract.ChannelWeb, contract.PermMaskBypass)
	card, err := e.svc.Card(e.ctx, bypass, b, "")
	if !errors.Is(err, ErrPrivacyNote) || card.Phone != "" || card.Name != "" {
		t.Fatalf("카드: %v %+v", err, card)
	}
	if strings.Contains(err.Error(), "디스크") {
		t.Fatal("내부 원인이 노출됨")
	}
	if cards, err := e.svc.SearchPeople(e.ctx, bypass, contract.PeopleQuery{Limit: 50}); !errors.Is(err, ErrPrivacyNote) || cards != nil {
		t.Fatalf("목록: %v %v", err, cards)
	}
	if cards, _, err := e.svc.ListPeople(e.ctx, bypass, contract.PeopleQuery{Limit: 50}, 100, false); !errors.Is(err, ErrPrivacyNote) || cards != nil {
		t.Fatalf("내보내기: %v", err)
	}
	// 가려진 응답은 기록이 필요 없으므로 기록기 장애와 무관하게 나간다.
	if c, err := e.svc.Card(e.ctx, e.viewer(a, contract.ChannelWeb), b, ""); err != nil || strings.Contains(c.Phone, "3333") {
		t.Fatalf("가린 카드: %v %+v", err, c)
	}
}

// 가져오기 미리보기: 영향 목록에 기존 구성원의 사번이 실리면 그 사람들을 사번 열람으로 기록한다.
func TestPrivacyNoteImportPreviewEmployeeNo(t *testing.T) {
	e, _, b := importEnv(t)
	n := &fakeNoter{}
	e.svc.SetPrivacy(n)
	e.reg(Command{Kind: KindHeadSet, UnitID: e.unitByCode("T1"), UserID: b, Effective: "2026-03-01", Reason: "시험"})
	if _, err := e.db.Exec(`INSERT INTO vendors (id, name) VALUES (1, '거래처')`); err != nil {
		t.Fatal(err)
	}
	staff := e.hire("요청자", "r@pl.test", e.unitByCode("T2"), contract.RoleStaff, "2024-01-02")
	now := e.now.UTC().Format("2006-01-02T15:04:05Z")
	if _, err := e.db.Exec(`INSERT INTO purchase_requests (id, no, requester_id, department_id, vendor_id, title, status, total_amount, round, created_at, updated_at)
		VALUES (1, 'PR-000001', ?, 1, 1, '복사용지', 'in_review', 1000, 1, ?, ?)`, staff, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO approval_steps (pr_id, round, seq, step_kind, status) VALUES (1, 1, 1, 'team_lead', 'pending')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO approval_step_assignees (pr_id, round, seq, user_id, name_at) VALUES (1, 1, 1, ?, 'x')`, b); err != nil {
		t.Fatal(err)
	}
	people := "사번,이름,이메일,직위,주소속조직코드,퇴사일,퇴사구분\nE002,박철수,b@pl.test,과장,T1,2026-03-31,자진\n"
	pv, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(people))
	if err != nil || len(pv.Impact) != 1 || !strings.Contains(pv.Impact[0].Who, "E002") {
		t.Fatalf("미리보기: %v %+v", err, pv)
	}
	if len(n.got) != 1 {
		t.Fatalf("기록 %d건", len(n.got))
	}
	g := n.got[0]
	if g.Category != privacy.CatEmployeeNo || g.Screen != privacy.ScreenImportPreview || len(g.Subjects) != 1 || g.Subjects[0].ID != b || strings.Join(g.Fields, ",") != "employee_no" {
		t.Fatalf("기록: %+v", g)
	}
	// 기록 실패 시 미리보기를 내주지 않는다.
	n.fail = errors.New("x")
	if pv, err := e.svc.PreviewImport(e.ctx, e.admin, inputOf(people)); !errors.Is(err, ErrPrivacyNote) || pv != nil {
		t.Fatalf("fail-closed: %v", err)
	}
}
