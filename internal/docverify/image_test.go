package docverify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
	"github.com/choibongseok/procure-ledger/internal/docverify"
	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
	"github.com/choibongseok/procure-ledger/internal/llmgw"
)

// 이미지 증빙 경로(mock): 이미지는 마스킹 없이 LLM 으로 나가므로 재무 권한 + 관리자 설정이 필요하다.

func setImageSetting(t *testing.T, e *env, on bool) {
	t.Helper()
	v := "false"
	if on {
		v = "true"
	}
	if _, err := e.db.Exec(`INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		llmgw.SettingImageTransmission, v); err != nil {
		t.Fatal(err)
	}
}

func imageInput(po, rc int64) domain.VerifyInput {
	return domain.VerifyInput{Filename: "scan.png", ContentType: "image/png", Image: []byte(goodDoc), POID: po, ReceiptID: rc}
}

func count(t *testing.T, e *env, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func lastRecord(e *env, action string) (audit.Record, bool) {
	rs := e.rec.Records()
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Action == action {
			return rs[i], true
		}
	}
	return audit.Record{}, false
}

// 설정 기본값은 꺼짐이다(마이그레이션 시드).
func TestImageTransmissionDefaultsOff(t *testing.T) {
	e := newEnv(t)
	if llmgw.ImageTransmissionEnabled(context.Background(), e.db) {
		t.Fatal("기본값은 꺼짐이어야 함")
	}
}

// 재무 외 역할(웹·MCP 가 같은 서비스를 탄다)은 설정이 켜져 있어도 이미지를 올릴 수 없고, 거부가 감사 로그에 남는다.
func TestImageUploadDeniedForNonFinanceRoles(t *testing.T) {
	for _, role := range []contract.Role{contract.RoleStaff, contract.RoleManager, contract.RoleAdmin, contract.RoleAuditor} {
		t.Run(string(role), func(t *testing.T) {
			e := newEnv(t)
			setImageSetting(t, e, true)
			po, rc, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
			_, err := e.svc.Verify(ctxAs(kit.Principal(role)), imageInput(po, rc))
			if !errors.Is(err, contract.ErrForbidden) {
				t.Fatalf("거부(ErrForbidden)여야 함: %v", err)
			}
			if len(e.spy.sent) != 0 {
				t.Fatal("거부인데 LLM 이 호출됨")
			}
			if n := count(t, e, `SELECT COUNT(*) FROM documents`); n != 0 {
				t.Fatalf("거부인데 문서가 저장됨: %d", n)
			}
			// staff 는 document:verify 가 있어 이미지 업로드 거부가, 나머지 역할은 verify 자체 거부가 남는다.
			action := "document.verify"
			if role == contract.RoleStaff {
				action = "document.upload_image"
			}
			if !e.rec.Has(action, audit.TargetDocument, audit.ResultDenied) {
				t.Fatalf("%s 거부 감사 기록이 없음: %+v", action, e.rec.Records())
			}
		})
	}
}

// 직원은 텍스트는 여전히 올릴 수 있다(이미지만 막는다).
func TestStaffStillVerifiesText(t *testing.T) {
	e := newEnv(t)
	v := e.verify(t, staff, boltSpec(), goodDoc)
	if v.Outcome != domain.OutcomeMatch {
		t.Fatalf("텍스트는 판정되어야 함: %s", v.Outcome)
	}
}

// 설정이 꺼져 있으면 재무도 이미지를 올릴 수 없다: LLM 미호출, 한국어 안내, 거부 감사.
func TestImageUploadRejectedWhenSettingOff(t *testing.T) {
	e := newEnv(t)
	po, rc, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	_, err := e.svc.Verify(ctxAs(finance), imageInput(po, rc))
	if !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("거부여야 함: %v", err)
	}
	for _, want := range []string{"이미지", "꺼져", "관리자"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("안내 문구에 %q 가 있어야 함: %v", want, err)
		}
	}
	if len(e.spy.sent) != 0 || count(t, e, `SELECT COUNT(*) FROM llm_usage`) != 0 {
		t.Fatal("설정이 꺼져 있는데 LLM 이 호출되었거나 사용량이 기록됨")
	}
	if count(t, e, `SELECT COUNT(*) FROM documents`) != 0 {
		t.Fatal("거부인데 문서가 저장됨")
	}
	rec, ok := lastRecord(e, llmgw.ActionImageBlocked)
	if !ok || rec.Result != audit.ResultDenied {
		t.Fatalf("llm_image.blocked 거부 감사가 있어야 함: %+v", e.rec.Records())
	}
	if len(rec.Diff) != 2 || rec.Diff[1].Key != "reason" || rec.Diff[1].New != "disabled" {
		t.Fatalf("diff: %+v", rec.Diff)
	}
}

// 게이트웨이도 서비스를 거치지 않는 이미지 요청을 막는다(권한·설정). 호출은 일어나지 않는다.
func TestGatewayBlocksImagesWithoutServiceGuard(t *testing.T) {
	e := newEnv(t)
	req := llm.Request{Purpose: "test", Messages: []llm.Message{{Role: "user", Content: "x"}},
		Images: []llm.Image{{MediaType: "image/png", Data: []byte("img")}}}
	if _, err := e.svc.LLM.Complete(ctxAs(staff), req); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("직원: %v", err)
	}
	if _, err := e.svc.LLM.Complete(ctxAs(finance), req); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("재무·설정 꺼짐: %v", err)
	}
	if len(e.spy.sent) != 0 {
		t.Fatal("게이트웨이가 이미지 요청을 통과시킴")
	}
	setImageSetting(t, e, true)
	if _, err := e.svc.LLM.Complete(ctxAs(finance), req); err != nil {
		t.Fatalf("재무·설정 켜짐은 통과: %v", err)
	}
}

// 설정이 켜져 있고 재무이면: 이미지 업로드 → 게이트웨이 → mock 추출 → 대조·판정 → 사용량·감사 기록.
func TestImageEndToEndWhenEnabled(t *testing.T) {
	e := newEnv(t)
	setImageSetting(t, e, true)
	po, rc, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	v, err := e.svc.Verify(ctxAs(finance), imageInput(po, rc))
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != domain.OutcomeMatch {
		t.Fatalf("일치여야 함: %s %+v", v.Outcome, v.Items)
	}
	// 게이트웨이를 지나 공급자에 이미지 1장이 전달됨
	if len(e.spy.sent) != 1 || len(e.spy.sent[0].Images) != 1 {
		t.Fatalf("이미지 1장이 LLM 요청에 실려야 함: %+v", e.spy.sent)
	}
	// 사용량
	if n := count(t, e, `SELECT COUNT(*) FROM llm_usage WHERE status = 'ok' AND purpose != ''`); n != 1 {
		t.Fatalf("사용량 1건: %d", n)
	}
	// 전송 감사: 별도 동작, 개수·크기·모델만(이미지 바이트는 없음)
	rec, ok := lastRecord(e, llmgw.ActionImageSent)
	if !ok || rec.Result != audit.ResultSuccess || rec.TargetType != audit.TargetLLMCall {
		t.Fatalf("llm_image.sent 감사가 있어야 함: %+v", e.rec.Records())
	}
	keys := map[string]string{}
	for _, d := range rec.Diff {
		keys[d.Key] = d.New
	}
	if keys["image_count"] != "1" || keys["image_bytes"] == "" || keys["model"] == "" {
		t.Fatalf("diff 키: %+v", rec.Diff)
	}
	if strings.Contains(rec.Detail, goodDoc) {
		t.Fatal("감사 로그에 이미지 내용이 들어가면 안 됨")
	}
	if !e.rec.Has("llm.complete", audit.TargetLLMCall, audit.ResultSuccess) || !e.rec.Has("document.upload", audit.TargetDocument, audit.ResultSuccess) {
		t.Fatalf("호출·업로드 감사: %+v", e.rec.Records())
	}
	// 이미지 경로는 원문 교차 확인이 없다는 점이 판정 근거에 드러난다
	it, ok := find(v, "grounding")
	if !ok || it.Result != domain.OutcomeHold || !strings.Contains(it.Note, "원문") || !strings.Contains(it.Note, "사람이") {
		t.Fatalf("원문 교차 확인 미적용이 근거에 드러나야 함: %+v", v.Items)
	}
}

// 이미지 경로는 코드의 대조·판정이 그대로 적용된다: 금액이 다르면 불일치.
func TestImageMismatchStillJudgedByCode(t *testing.T) {
	e := newEnv(t)
	setImageSetting(t, e, true)
	po, rc, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	in := imageInput(po, rc)
	in.Image = []byte(strings.Replace(goodDoc, "합계: 7000", "합계: 9000", 1))
	v, err := e.svc.Verify(ctxAs(finance), in)
	if err != nil {
		t.Fatal(err)
	}
	if v.Outcome != domain.OutcomeMismatch {
		t.Fatalf("불일치여야 함: %s %+v", v.Outcome, v.Items)
	}
}

// 텍스트 경로에는 원문 교차 확인 안내 항목이 붙지 않는다.
func TestTextPathHasNoGroundingNotice(t *testing.T) {
	e := newEnv(t)
	v := e.verify(t, staff, boltSpec(), goodDoc)
	if _, ok := find(v, "grounding"); ok {
		t.Fatalf("텍스트 경로에 grounding 항목이 있으면 안 됨: %+v", v.Items)
	}
}

// 전송 감사 기록이 실패하면 이미지를 보내지 않는다(예약도 롤백).
func TestImageNotSentWhenSendAuditFails(t *testing.T) {
	e := newEnv(t)
	setImageSetting(t, e, true)
	po, rc, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	e.rec.FailWith(errors.New("disk"))
	_, err := e.svc.Verify(ctxAs(finance), imageInput(po, rc))
	e.rec.FailWith(nil)
	if !errors.Is(err, contract.ErrAuditFailed) {
		t.Fatalf("감사 실패로 취소되어야 함: %v", err)
	}
	if len(e.spy.sent) != 0 {
		t.Fatal("감사 기록이 실패했는데 이미지가 전송됨")
	}
	if n := count(t, e, `SELECT COUNT(*) FROM llm_usage`); n != 0 {
		t.Fatalf("예약이 롤백되어야 함: %d", n)
	}
	if n := count(t, e, `SELECT COUNT(*) FROM documents`); n != 0 {
		t.Fatalf("문서가 저장되면 안 됨: %d", n)
	}
}

// PDF 는 텍스트로 바뀌어 마스킹 경로를 타므로 이미지 권한·설정과 무관하다.
func TestPDFDoesNotNeedImagePermission(t *testing.T) {
	e := newEnv(t)
	po, _, _ := e.inv.Seed(context.Background(), e.db, boltSpec())
	if _, err := e.svc.Verify(ctxAs(staff), domain.VerifyInput{Filename: "s.pdf", ContentType: docverify.PDFContentType,
		Image: kit.TextPDF([]string{"Statement"}), POID: po}); err != nil {
		t.Fatalf("직원의 PDF 업로드는 허용: %v", err)
	}
}

func TestImageUploadStatus(t *testing.T) {
	e := newEnv(t)
	st := func(p contract.Principal) string {
		s, err := e.svc.ImageUploadStatus(ctxAs(p))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if st(staff) != docverify.ImageForbiddenRole || st(finance) != docverify.ImageDisabled {
		t.Fatalf("초기: staff=%s finance=%s", st(staff), st(finance))
	}
	setImageSetting(t, e, true)
	if st(staff) != docverify.ImageForbiddenRole || st(finance) != docverify.ImageAllowed {
		t.Fatalf("켬: staff=%s finance=%s", st(staff), st(finance))
	}
}
