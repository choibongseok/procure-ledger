package contract

import (
	"strings"
	"testing"
)

// AC-62·139: MCP 도구에는 결재자 재지정·알림을 쓰는 도구가 없다(관리자 조치라서 AI 연결로 열지 않는다).
// 도구 표(MCPToolPermissions)가 도구의 유일한 목록이므로 이 표에서 판정한다. 결재선 미리보기 도구(B22 who_approves)는 읽기라 허용된다.
func TestMCPToolsExposeNoReassignOrNotificationWrites(t *testing.T) {
	for tool, perm := range MCPToolPermissions {
		lower := strings.ToLower(tool)
		if strings.Contains(lower, "reassign") || strings.Contains(lower, "notification") || strings.Contains(lower, "notify") {
			t.Errorf("MCP 도구 %q 는 재지정·알림 쓰기 도구처럼 보인다", tool)
		}
		if perm == PermApprovalReassign || perm == PermNotificationRead {
			t.Errorf("MCP 도구 %q 가 재지정·알림 권한(%s)으로 선언됐다", tool, perm)
		}
	}
	// 재지정 권한은 관리자만 갖는다(AC-123).
	for role, perms := range RolePermissions {
		has := false
		for _, p := range perms {
			if p == PermApprovalReassign {
				has = true
			}
		}
		if has != (role == RoleAdmin) {
			t.Errorf("approval:reassign 은 admin 만 가져야 한다: 역할 %s 보유=%v", role, has)
		}
	}
}
