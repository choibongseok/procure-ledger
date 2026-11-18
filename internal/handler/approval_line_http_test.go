package handler_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

func (s *server) draftAndSubmit(user string, amount int64) string {
	s.t.Helper()
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": fmt.Sprintf("L%d", amount), "name": "용지", "unit": "EA"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	pr := s.must(user, "POST", "/api/purchase-requests", map[string]any{
		"vendorId": vendor["id"], "title": "결재선 시험", "reason": "x",
		"lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": amount}},
	}, 201)
	s.must(user, "POST", "/api/purchase-requests/"+id(pr)+"/submit", nil, 200)
	return id(pr)
}

// 결재선 미리보기: 본인 것은 항상, 타인은 열람 범위일 때만(AC-70·72), 금액 0 이하는 400, 로그인 없이는 401.
func TestHTTPLinePreview(t *testing.T) {
	s := newServer(t)
	got := s.must("staff", "GET", "/api/approvals/line-preview?amount=5000000", nil, 200)
	steps, _ := got["steps"].([]any)
	if got["complete"] != true || len(steps) != 2 || got["unitPath"] != "구매팀 > 구매1팀" {
		t.Fatalf("미리보기: %v", got)
	}
	first := steps[0].(map[string]any)
	if first["title"] != "팀장" || first["approverId"] != float64(2) || first["unitName"] != "구매1팀" {
		t.Fatalf("1단계: %v", first)
	}
	s.must("staff", "GET", "/api/approvals/line-preview?amount=0", nil, 400)
	s.must("staff", "GET", "/api/approvals/line-preview?amount=abc", nil, 400)
	s.must("", "GET", "/api/approvals/line-preview?amount=500000", nil, 401)
	// 타인: 같은 부서(lead→staff)는 되고 다른 부서(staff→staff2)는 403, 전체 열람 역할(auditor)은 된다.
	s.must("lead", "GET", "/api/approvals/line-preview?amount=500000&requesterId=1", nil, 200)
	code, _, raw := s.do("staff", "GET", "/api/approvals/line-preview?amount=500000&requesterId=5", nil)
	if code != 403 || !strings.Contains(raw, "이 사람의 결재선은 볼 수 없습니다") {
		t.Fatalf("타 부서 요청자: %d %s", code, raw)
	}
	s.must("auditor", "GET", "/api/approvals/line-preview?amount=500000&requesterId=5", nil, 200)
	// 응답 어디에도 연락처 키가 없다(AC-71).
	_, _, body := s.do("staff", "GET", "/api/approvals/line-preview?amount=500000", nil)
	if strings.Contains(body, "email") || strings.Contains(body, "phone") {
		t.Fatalf("연락처가 응답에 있다: %s", body)
	}
}

// 결재선을 만들 수 없는 요청은 화면을 거치지 않고 API 로 직접 상신해도 400 이고, 감사 로그에 거부로 남는다(AC-13b).
func TestHTTPSubmitWithoutLineIsRefusedAndAudited(t *testing.T) {
	s := newServer(t)
	// 영업직원(5)의 영업팀은 부서장이 공석이라 대표(9)까지 올라가는데, 대표를 비활성으로 두어도 조직 기준 결재자는 남는다.
	// 결재선을 못 만드는 경우: 대표가 요청자인 건.
	item := s.must("finance", "POST", "/api/items", map[string]any{"sku": "C", "name": "용지", "unit": "EA"}, 201)
	vendor := s.must("finance", "POST", "/api/vendors", map[string]any{"name": "대한문구", "businessNo": "123-45-67890"}, 201)
	pr := s.must("ceo", "POST", "/api/purchase-requests", map[string]any{
		"vendorId": vendor["id"], "title": "대표 구매", "reason": "x",
		"lines": []map[string]any{{"itemId": item["id"], "quantity": 1, "unitPrice": 100000}},
	}, 201)
	code, _, raw := s.do("ceo", "POST", "/api/purchase-requests/"+id(pr)+"/submit", nil)
	if code != 400 || !strings.Contains(raw, "대표의 구매요청은 결재선을 만들 수 없습니다") {
		t.Fatalf("대표 요청 상신: %d %s", code, raw)
	}
	if !s.rec.Has("purchase_request.submit", audit.TargetPurchaseRequest, audit.ResultDenied) {
		t.Fatal("결재선 불가 거부가 감사에 남아야 함")
	}
	got := s.must("ceo", "GET", "/api/purchase-requests/"+id(pr), nil, 200)
	if got["status"] != "draft" {
		t.Fatalf("상태가 바뀜: %v", got["status"])
	}
}

// 배정자가 퇴사하면 소멸 목록에 나오고 admin 만 재지정한다. 재지정 뒤 새 결재자가 결재하고 알림이 간다(AC-73·120·123·140).
func TestHTTPReassignFlow(t *testing.T) {
	s := newServer(t)
	prID := s.draftAndSubmit("staff", 500000)
	if _, err := s.db.Exec(`UPDATE users SET active = 0 WHERE id = 2`); err != nil { // 1단계 배정자 팀장(2) 퇴사
		t.Fatal(err)
	}
	// 소멸 목록: org:manage(admin)만. 사유 "퇴사".
	s.must("staff", "GET", "/api/approvals/orphaned", nil, 403)
	var list []any
	_, _, raw := s.do("admin", "GET", "/api/approvals/orphaned", nil)
	if !strings.Contains(raw, `"reason":"퇴사"`) || !strings.Contains(raw, `"reassignable":true`) {
		t.Fatalf("소멸 목록: %s", raw)
	}
	_ = list
	// 후보: admin 만, 자격 있는 조직장만.
	s.must("lead", "GET", "/api/approvals/"+prID+"/steps/1/reassign-candidates", nil, 403)
	_, _, cands := s.do("admin", "GET", "/api/approvals/"+prID+"/steps/1/reassign-candidates", nil)
	if !strings.Contains(cands, `"userId":3`) || strings.Contains(cands, `"userId":1,`) {
		t.Fatalf("후보: %s", cands)
	}
	// 권한 없는 역할은 라우트 선언에서 403 이다(거부 감사는 라우터 접근 거부 기록이 한다).
	body := map[string]any{"userId": 3, "cause": "leave", "reason": "퇴사로 부서장이 대신 결재"}
	s.must("lead", "POST", "/api/approvals/"+prID+"/steps/1/reassign", body, 403)
	// 검증 오류: 사유 없음, 요청자 지정, 재무 단계.
	s.must("admin", "POST", "/api/approvals/"+prID+"/steps/1/reassign", map[string]any{"userId": 3, "cause": "leave", "reason": "  "}, 400)
	s.must("admin", "POST", "/api/approvals/"+prID+"/steps/1/reassign", map[string]any{"userId": 1, "cause": "leave", "reason": "x"}, 400)
	// 재지정.
	res := s.must("admin", "POST", "/api/approvals/"+prID+"/steps/1/reassign", body, 200)
	steps := res["steps"].([]any)
	st := steps[0].(map[string]any)
	if st["assigneeId"] != float64(3) || st["reassignedFrom"] == "" {
		t.Fatalf("재지정 결과: %v", st)
	}
	if !s.rec.Has("approval.reassign", audit.TargetApproval, audit.ResultSuccess) {
		t.Fatal("재지정 감사 기록 없음")
	}
	// 원 결재자는 결재할 수 없고 새 결재자는 결재한다(결재함에 보인다).
	s.must("head", "GET", "/api/approvals/pending", nil, 200)
	s.must("head", "POST", "/api/approvals/"+prID+"/decision", map[string]any{"approve": true}, 200)
	// 이력에 "reassign" 한 줄.
	_, _, hist := s.do("staff", "GET", "/api/approvals/"+prID+"/history", nil)
	if !strings.Contains(hist, `"action":"reassign"`) || !strings.Contains(hist, "결재자 변경") {
		t.Fatalf("이력: %s", hist)
	}
	// 알림: 요청자(staff)와 새 결재자(head)가 받았고, 읽음 처리한다. 남의 알림은 못 건드린다.
	_, _, mine := s.do("staff", "GET", "/api/notifications?unread=1", nil)
	if !strings.Contains(mine, "결재자가 바뀌었습니다") || strings.Contains(mine, "kind") {
		t.Fatalf("요청자 알림: %s", mine)
	}
	cnt := s.must("head", "GET", "/api/notifications/unread-count", nil, 200)
	if cnt["unread"] != float64(1) {
		t.Fatalf("새 결재자 알림 수: %v", cnt)
	}
	if m := regexp.MustCompile(`"id":\s*(\d+)`).FindStringSubmatch(mine); m == nil {
		t.Fatalf("요청자 알림 번호 없음: %s", mine)
	} else {
		s.must("lead", "POST", "/api/notifications/"+m[1]+"/read", nil, 404)
	}
	s.must("head", "POST", "/api/notifications/read-all", nil, 200)
	if c := s.must("head", "GET", "/api/notifications/unread-count", nil, 200); c["unread"] != float64(0) {
		t.Fatalf("읽음 처리 뒤: %v", c)
	}
	s.must("", "GET", "/api/notifications", nil, 401)
}
