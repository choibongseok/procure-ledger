package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/knowledge"
)

// B47 2단계(ai·platform): 업로드 매뉴얼·규정. 열람 범위(역할 AND 부서 AND 법인), 교체·삭제의 즉시 반영, 거부 문구, 원문 보기·내려받기의 같은 판정.

const regMD = "## 제5조 (금액 구간별 결재선)\n구매 금액이 일백만 원을 넘으면 팀장과 부서장이 결재한다.\n\n## 제6조 (예외)\n긴급 구매는 사후 결재한다.\n"

func (k *kenv) upload(t *testing.T, ctx context.Context, in domain.KnowledgeUploadInput) domain.KnowledgeUpload {
	t.Helper()
	if in.Filename == "" {
		in.Filename = "구매규정.md"
	}
	if in.Content == nil {
		in.Content = []byte(regMD)
	}
	if in.Kind == "" {
		in.Kind = "regulation"
	}
	if in.Title == "" {
		in.Title = "구매 규정"
	}
	u, err := k.admin.Upload(ctx, in)
	if err != nil {
		t.Fatalf("업로드: %v", err)
	}
	k.drain(t)
	return u
}

func (k *kenv) sources(t *testing.T, ctx context.Context, q string) []string {
	t.Helper()
	res, err := k.svc.Search(ctx, domain.KnowledgeFilter{Query: q, Limit: 20})
	if err != nil {
		t.Fatalf("검색: %v", err)
	}
	var out []string
	for _, h := range res.Hits {
		out = append(out, h.Source)
	}
	return out
}

func anyHas(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// AC-4708·4716·4717: 역할 AND 부서 범위, 출처의 조항 표지, 공개 범위 변경이 색인 갱신 없이 바로 반영된다.
func TestKnowledgeUploadScopeRoleAndDepartment(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	admin := as(uAdmin)
	u := k.upload(t, admin, domain.KnowledgeUploadInput{Roles: "finance", Departments: "all"})
	src := "사내 규정 「구매 규정」 제5조 (금액 구간별 결재선)"
	if got := k.sources(t, as(uFinance), "금액 구간별 결재선"); !anyHas(got, src) {
		t.Fatalf("재무가 못 찾음: %v", got)
	}
	if got := k.sources(t, as(uStaff), "금액 구간별 결재선"); len(got) != 0 {
		t.Fatalf("역할 밖(직원)이 찾음: %v", got)
	}
	// 범위를 모두에게 넓히면 색인을 다시 만들지 않고도 직원이 찾는다.
	if _, err := k.admin.SetScope(admin, u.ID, "all", "all"); err != nil {
		t.Fatal(err)
	}
	if got := k.sources(t, as(uStaff), "금액 구간별 결재선"); !anyHas(got, src) {
		t.Fatalf("넓힌 뒤 직원이 못 찾음: %v", got)
	}
	// 부서 범위: 구매팀만. 영업팀 직원은 못 찾는다.
	if _, err := k.admin.SetScope(admin, u.ID, "all", "구매팀"); err != nil {
		t.Fatal(err)
	}
	if got := k.sources(t, as(uStaff), "금액 구간별 결재선"); !anyHas(got, src) {
		t.Fatalf("구매팀 직원: %v", got)
	}
	if got := k.sources(t, as(uStaff2), "금액 구간별 결재선"); len(got) != 0 {
		t.Fatalf("다른 부서(영업팀)가 찾음: %v", got)
	}
	// 낡은 사전 거름: DB 값을 직접 좁혀도(색인 세대 그대로) 독립 재확인이 거른다.
	if _, err := e.db.Exec(`UPDATE knowledge_uploads SET departments = '영업팀' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if got := k.sources(t, as(uStaff), "금액 구간별 결재선"); len(got) != 0 {
		t.Fatalf("낡은 색인이 범위를 넘김: %v", got)
	}
	// 잘못된 범위는 거부된다(모르는 역할·부서, 빈 값은 all).
	if _, err := k.admin.SetScope(admin, u.ID, "wizard", "all"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("모르는 역할: %v", err)
	}
	if _, err := k.admin.SetScope(admin, u.ID, "all", "없는부서"); !errors.Is(err, contract.ErrValidation) {
		t.Fatalf("모르는 부서: %v", err)
	}
	if !e.rec.Has("knowledge.scope", audit.TargetKnowledge, audit.ResultSuccess) || !e.rec.Has("knowledge.upload", audit.TargetKnowledge, audit.ResultSuccess) {
		t.Fatal("업로드·범위 감사 없음")
	}
}

// AC-4708·4715: 교체는 새 버전만 검색되고(출처에 v2), 삭제는 바로 사라지며 조각이 지워진다. 직원은 올릴 수 없다.
func TestKnowledgeUploadReplaceAndRemove(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	admin := as(uAdmin)
	if _, err := k.admin.Upload(as(uStaff), domain.KnowledgeUploadInput{Kind: "manual", Title: "x", Filename: "a.md", Content: []byte("## 소제목\n본문 내용")}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원 업로드: %v", err)
	}
	if !e.rec.Has("knowledge.upload", audit.TargetKnowledge, audit.ResultDenied) {
		t.Fatal("거부 감사 없음")
	}
	u := k.upload(t, admin, domain.KnowledgeUploadInput{Kind: "manual", Title: "재고 실사 안내", Filename: "실사.md", Content: []byte("## 준비물\n실사표와 바코드 스캐너를 준비한다")})
	if got := k.sources(t, as(uStaff), "바코드 스캐너"); !anyHas(got, "사내 매뉴얼 「재고 실사 안내」 「준비물」") {
		t.Fatalf("v1: %v", got)
	}
	// 같은 내용·같은 제목은 거부된다.
	if _, err := k.admin.Upload(admin, domain.KnowledgeUploadInput{Kind: "manual", Title: "다른 제목", Filename: "복사.md", Content: []byte("## 준비물\n실사표와 바코드 스캐너를 준비한다")}); err == nil || !strings.Contains(err.Error(), "같은 파일이 이미 올라와 있습니다") {
		t.Fatalf("같은 파일: %v", err)
	}
	if _, err := k.admin.Upload(admin, domain.KnowledgeUploadInput{Kind: "manual", Title: "재고 실사 안내", Filename: "새.md", Content: []byte("## 다른\n다른 내용")}); err == nil || !strings.Contains(err.Error(), "새 버전 올리기") {
		t.Fatalf("같은 제목: %v", err)
	}
	v2, err := k.admin.Replace(admin, u.ID, "실사2.md", []byte("## 준비물\n실사표와 태블릿을 준비한다"))
	if err != nil || v2.Version != 2 {
		t.Fatalf("새 버전: %+v %v", v2, err)
	}
	// 교체 직후(워커 전)에도 이전 버전은 검색에서 빠져 있다.
	if got := k.sources(t, as(uStaff), "바코드 스캐너"); len(got) != 0 {
		t.Fatalf("이전 버전이 검색됨: %v", got)
	}
	k.drain(t)
	if got := k.sources(t, as(uStaff), "태블릿을 준비"); !anyHas(got, "「재고 실사 안내」 v2") {
		t.Fatalf("v2 출처: %v", got)
	}
	// 열려 있는 목록에는 새 버전 하나뿐이고 색인 상태가 보인다.
	list, err := k.admin.Uploads(admin)
	if err != nil || len(list) != 1 || list[0].ID != v2.ID || list[0].State != "ready" {
		t.Fatalf("목록: %+v %v", list, err)
	}
	if err := k.admin.Remove(admin, v2.ID); err != nil {
		t.Fatal(err)
	}
	if got := k.sources(t, as(uStaff), "태블릿을 준비"); len(got) != 0 {
		t.Fatalf("삭제 뒤: %v", got)
	}
	if n := count(t, e.db, `SELECT COUNT(*) FROM knowledge_chunks c JOIN knowledge_docs d ON d.id = c.doc_id WHERE d.source_kind = 'upload'`); n != 0 {
		t.Fatalf("삭제 뒤 조각이 남음: %d", n)
	}
	var size int
	_ = e.db.QueryRow(`SELECT size FROM knowledge_uploads WHERE id = ?`, v2.ID).Scan(&size)
	if size != 0 {
		t.Fatal("삭제된 업로드의 내용이 남음")
	}
	if err := k.admin.Remove(admin, v2.ID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("이미 지운 문서: %v", err)
	}
	if _, err := e.db.Exec(`DELETE FROM knowledge_uploads WHERE id = ?`, v2.ID); err == nil {
		t.Fatal("업로드 행은 지울 수 없어야 한다")
	}
}

// AC-4716·4718: 거부 문구(형식·크기·스캔·깨짐·매크로), 한도, PDF 쪽·조항 출처.
func TestKnowledgeUploadRejectionsAndPDFSources(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	admin := as(uAdmin)
	try := func(name string, data []byte) error {
		_, err := k.admin.Upload(admin, domain.KnowledgeUploadInput{Kind: "manual", Title: "시험 " + name, Filename: name, Content: data})
		return err
	}
	for name, c := range map[string]struct {
		data []byte
		msg  string
	}{
		"a.hwp":    {[]byte("x"), knowledge.MsgFormat},
		"scan.pdf": {kit.BlankPDF(), knowledge.MsgNoText},
		"font.pdf": {kit.KoreanMultiPagePDF([][]string{{"제1조 목적"}}, false), knowledge.MsgGarbled},
		"bad.pdf":  {[]byte("%PDF-1.4 broken"), knowledge.MsgBadPDF},
		"empty.md": {[]byte("  "), knowledge.MsgEmpty},
	} {
		if err := try(name, c.data); err == nil || !errors.Is(err, contract.ErrValidation) || !strings.Contains(err.Error(), c.msg) {
			t.Fatalf("%s: %v (기대 %q)", name, err, c.msg)
		}
	}
	if err := try("big.md", []byte(strings.Repeat("가", knowledge.MaxUploadBytes/3+10))); err == nil || !strings.Contains(err.Error(), "파일이 너무 큽니다") {
		t.Fatalf("크기: %v", err)
	}
	// 한글 PDF: 쪽·조항이 출처에 나온다.
	pdf := kit.KoreanMultiPagePDF([][]string{{"제1조 목적", "이 규정은 구매 절차를 정한다"}, {"제2조 (재고 실사)", "분기마다 재고 실사를 한다"}}, true)
	k.upload(t, admin, domain.KnowledgeUploadInput{Kind: "regulation", Title: "재고 규정", Filename: "재고.pdf", Content: pdf})
	if got := k.sources(t, as(uStaff), "분기마다 재고 실사"); !anyHas(got, "사내 규정 「재고 규정」 2쪽 제2조 (재고 실사)") {
		t.Fatalf("PDF 출처: %v", got)
	}
	// 한도: 법인당 열린 업로드 수.
	old := knowledge.MaxUploadsPerEntity
	knowledge.MaxUploadsPerEntity = 1
	defer func() { knowledge.MaxUploadsPerEntity = old }()
	if err := try("one.md", []byte("## 가\n나다라마")); err == nil || !strings.Contains(err.Error(), "최대 개수") {
		t.Fatalf("한도: %v", err)
	}
}

// AC-4702·4708: 법인. 법인 지정 업로드는 그 법인에서만, 모든 법인 공통(*)은 그룹 범위 관리자만 올리고 어디서나 찾는다. 범위 밖 법인에는 올릴 수 없다.
func TestKnowledgeUploadEntityScope(t *testing.T) {
	e := newEnv(t)
	abc := e.secondEntity(t)
	k := e.knowledge(t)
	// 계열사에서 올린 문서(법인 코드 생략 = 현재 법인).
	k.upload(t, inEntity(uAdmin, abc), domain.KnowledgeUploadInput{Title: "계열사 규정", Content: []byte("## 제1조 (대상)\n계열사 전용 구매 기준을 정한다")})
	if got := k.sources(t, inEntity(uStaff, abc), "계열사 전용 구매 기준"); !anyHas(got, "계열사 규정") {
		t.Fatalf("계열사에서 못 찾음: %v", got)
	}
	if got := k.sources(t, inEntity(uStaff, contract.DefaultEntityID), "계열사 전용 구매 기준"); len(got) != 0 {
		t.Fatalf("기본 법인에서 찾음: %v", got)
	}
	// 모든 법인 공통: 그룹 범위가 없으면 거부.
	limited := users[uAdmin]
	limited.Channel = contract.ChannelWeb
	limited.EntityPerms = map[contract.Permission]contract.EntitySet{contract.PermKnowledgeManage: contract.NewEntitySet(contract.DefaultEntityID)}
	lctx := contract.WithActiveEntity(contract.WithPrincipal(context.Background(), limited), contract.ActiveEntity{ID: contract.DefaultEntityID})
	if _, err := k.admin.Upload(lctx, domain.KnowledgeUploadInput{Kind: "manual", Title: "공통", Entity: "*", Filename: "a.md", Content: []byte("## 가\n나다라마")}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("그룹 범위 없이 공통 업로드: %v", err)
	}
	if _, err := k.admin.Upload(lctx, domain.KnowledgeUploadInput{Kind: "manual", Title: "계열사에 몰래", Entity: "ABC", Filename: "a.md", Content: []byte("## 가\n나다라마")}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("범위 밖 법인에 업로드: %v", err)
	}
	group := users[uAdmin]
	group.Channel = contract.ChannelWeb
	group.EntityPerms = map[contract.Permission]contract.EntitySet{contract.PermKnowledgeManage: {All: true}, contract.PermKnowledgeSearch: {All: true}}
	gctx := contract.WithActiveEntity(contract.WithPrincipal(context.Background(), group), contract.ActiveEntity{ID: contract.DefaultEntityID})
	k.upload(t, gctx, domain.KnowledgeUploadInput{Kind: "manual", Title: "그룹 공통 안내", Entity: "*", Filename: "g.md", Content: []byte("## 안내\n전 계열사 공통으로 지키는 보안 수칙")})
	for _, ent := range []int64{contract.DefaultEntityID, abc} {
		if got := k.sources(t, inEntity(uStaff, ent), "전 계열사 공통으로 지키는 보안 수칙"); !anyHas(got, "그룹 공통 안내") {
			t.Fatalf("공통 문서가 법인 %d 에서 안 보임: %v", ent, got)
		}
	}
	// 원문 보기·내려받기: 법인·범위 밖은 같은 404.
	var ownID int64
	_ = e.db.QueryRow(`SELECT id FROM knowledge_uploads WHERE title = '계열사 규정'`).Scan(&ownID)
	if _, err := k.svc.Open(inEntity(uStaff, contract.DefaultEntityID), ownID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("다른 법인 원문 보기: %v", err)
	}
	if _, err := k.svc.File(inEntity(uStaff, contract.DefaultEntityID), ownID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("다른 법인 내려받기: %v", err)
	}
	v, err := k.svc.Open(inEntity(uStaff, abc), ownID)
	if err != nil || v.Title != "계열사 규정" || len(v.Sections) == 0 || !strings.Contains(v.Sections[0].Text, "계열사 전용") {
		t.Fatalf("원문 보기: %+v %v", v, err)
	}
	f, err := k.svc.File(inEntity(uStaff, abc), ownID)
	if err != nil || f.Filename != "구매규정.md" || !strings.HasPrefix(f.ContentType, "text/markdown") || !strings.Contains(string(f.Data), "계열사 전용") {
		t.Fatalf("내려받기: %+v %v", f.Filename, err)
	}
	if !e.rec.Has("knowledge.open", audit.TargetKnowledge, audit.ResultSuccess) || !e.rec.Has("knowledge.download", audit.TargetKnowledge, audit.ResultSuccess) {
		t.Fatal("원문 보기·내려받기 감사 없음")
	}
	// 지운 문서는 열 수 없다.
	if err := k.admin.Remove(inEntity(uAdmin, abc), ownID); err != nil {
		t.Fatal(err)
	}
	if _, err := k.svc.Open(inEntity(uStaff, abc), ownID); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("지운 문서 보기: %v", err)
	}
}

// 다시 색인은 업로드도 멱등으로 다시 만든다.
func TestKnowledgeReindexIncludesUploads(t *testing.T) {
	e := newEnv(t)
	k := e.knowledge(t)
	admin := as(uAdmin)
	k.upload(t, admin, domain.KnowledgeUploadInput{})
	if n := count(t, e.db, `SELECT COUNT(*) FROM knowledge_chunks c JOIN knowledge_docs d ON d.id = c.doc_id WHERE d.source_kind = 'upload'`); n < 2 {
		t.Fatalf("조항 조각: %d", n)
	}
	done, skipped, err := k.admin.Reindex(admin, "", "upload")
	if err != nil || done != 0 || skipped != 1 {
		t.Fatalf("멱등: %d %d %v", done, skipped, err)
	}
	if _, err := e.db.Exec(`DELETE FROM knowledge_chunks WHERE doc_id IN (SELECT id FROM knowledge_docs WHERE source_kind = 'upload')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`UPDATE knowledge_docs SET content_hash = 'x' WHERE source_kind = 'upload'`); err != nil {
		t.Fatal(err)
	}
	if done, _, err = k.admin.Reindex(admin, "", ""); err != nil || done < 1 {
		t.Fatalf("복구: %d %v", done, err)
	}
	if got := k.sources(t, as(uStaff), "금액 구간별 결재선"); !anyHas(got, "제5조") {
		t.Fatalf("복구 뒤 검색: %v", got)
	}
}
