package repository

import "strings"

// ViewScope 는 구매요청(과 그 발주·입고)을 볼 수 있는 범위다(B21 3.5). 구매요청 열람 조건은 이 파일의 Cond 한 곳에만 있다.
// 목록·단건·결재함·홈 요약·발주·입고 조회가 모두 이 조건을 쓴다. 서비스가 호출자에게서 만들어 저장소에 넘긴다.
//
// 보이는 경우(하나라도 맞으면):
//   - All: purchase:read_all 역할(finance·admin·auditor)
//   - 요청자 본인(임시저장 건은 본인과 All 만 본다)
//   - 건의 귀속 부서(department_id)가 호출자의 부서와 같다(같은 부서 동료, 호환 투영)
//   - 건의 귀속 조직(owner_unit_id)이 호출자가 조직장인 조직 범위(하위 포함, 겸직 조직장·직무대행 포함) 안
//   - 호출자가 그 건 현재 차수의 결재선 단계의 현재 결재자(재지정 반영)이거나, 본인 자격으로 이미 결재한 사람(위임으로 대신 결재한 사람은 위임이 유효한 동안만 별도 경로)
//
// 위임 수임자의 열람은 위임이 유효한 동안만이라 별도 경로(위임 저장소 CanView)에서 판정한다.
type ViewScope struct {
	All     bool // purchase:read_all — 구매 품의서 전체
	AllDocs bool // document:read_all — 그 밖의 양식 문서 전체(특권 열람, 감사 기록 대상)
	UserID  int64
	DeptID  int64
	UnitIDs []int64 // HeadedScope: 조직장인 조직과 하위 조직 번호
	// ReceiveUnits 는 내가 접수 권한이 있는 수신처(조직장·직무대행·수신 담당자인 조직)다. 결재 완료 문서의 수신 건이 있는 수신처 담당자도 그 문서를 본다(B23 단계 2).
	ReceiveUnits []int64
	// SeeDrafts 는 임시저장 건도 부서·조직 경로로 보는 내부 조회용이다(같은 부서 재주문 초안 중복 확인). 화면·API 조회에는 쓰지 않는다.
	SeeDrafts bool
	// FinancePool 은 재무 담당자다: 재무 풀 단계가 지금 차례이거나 이미 승인된 양식 문서를 본다(재무 단계는 담당자가 개인으로 배정되지 않아 위 결재선 조건으로는 보이지 않는다).
	FinancePool bool
	// ActingDocIDs 는 지금 유효한 위임(대결)으로 처리할 단계가 있거나 대결로 이미 처리한 진행 중 문서 번호다(B29 AC-811). 서비스가 같은 판정(canView)으로 걸러 채운다.
	ActingDocIDs []int64
	// LeaveGuard 는 전체 열람 권한(purchase:read_all·document:read_all)자에게도 휴가 신청서(LEAVE)만은 관계(신청자·결재선·직속 조직장·인사 담당)로 제한한다(B50 AC-5040).
	// 서비스의 viewScope 가 전체 열람 권한자에게 채운다. 내부 조회용 ScopeAll 에는 없다.
	LeaveGuard bool
	// HRLeaveEntity 는 인사 담당(attendance:hr)이면 0 이 아니다: 그 법인의 휴가 신청서를 본다(전사가 아니라 현재 법인).
	HRLeaveEntity int64
}

const leaveFormSQL = "(SELECT id FROM approval_forms WHERE code = 'LEAVE')"

// Unrestricted 는 어떤 문서도 거르지 않는 범위다(내부 조회 ScopeAll). 휴가 신청서 보호가 켜진 전체 열람자는 해당하지 않는다.
func (v ViewScope) Unrestricted() bool { return v.All && v.AllDocs && !v.LeaveGuard }

// LeaveCond 는 전체 열람자가 쓰는 조건이다: 휴가 신청서가 아니면 통과, 휴가 신청서면 관계 범위일 때만 통과한다.
func (v ViewScope) LeaveCond() (string, []any) {
	base, args := v.relCond()
	return "(COALESCE(p.form_id,0) <> " + leaveFormSQL + " OR " + base + ")", args
}

// ScopeAll 은 제한 없는 범위다(내부 조회·권한자).
var ScopeAll = ViewScope{All: true, AllDocs: true}

// Cond 는 구매요청 표(별칭 p)에 붙일 조건 조각과 인자다. All 이면 항상 참이다.
func (v ViewScope) Cond() (string, []any) {
	if v.All && v.AllDocs {
		if v.LeaveGuard {
			return v.LeaveCond()
		}
		return "1 = 1", nil
	}
	return v.relCond()
}

// relCond 는 관계(본인·부서·조직장·결재선·참조·수신·위임·재무 풀·인사 담당)로 여는 조건이다.
func (v ViewScope) relCond() (string, []any) {
	// 임시저장 건은 요청자 본인(과 All)만 본다. 상신 이후의 건만 부서·조직장·결재선 경로로 열린다(명세 3.5).
	// 부서 동료 열람은 양식이 허용한 문서(dept_visible)에만 적용한다. 구매 품의서는 항상 1 이다.
	parts := []string{"(p.dept_visible = 1 AND p.department_id = ?)"}
	args := []any{v.DeptID}
	if len(v.UnitIDs) > 0 {
		ph := make([]string, len(v.UnitIDs))
		for i, id := range v.UnitIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		parts = append(parts, "p.owner_unit_id IN ("+strings.Join(ph, ",")+")")
	}
	parts = append(parts,
		"EXISTS (SELECT 1 FROM approval_step_current c WHERE c.pr_id = p.id AND c.round = p.round AND c.user_id = ?)",
		"EXISTS (SELECT 1 FROM approval_steps s WHERE s.pr_id = p.id AND s.approver_id = ? AND s.on_behalf_of IS NULL)",
		// 참조 대상은 상신 이후 현재 차수의 문서를 본다(임시저장 제외는 아래 draft 조건이 한다).
		"EXISTS (SELECT 1 FROM document_recipients r WHERE r.doc_id = p.id AND r.round = p.round AND r.kind = 'cc' AND r.target_type = 'user' AND r.target_id = ?)")
	args = append(args, v.UserID, v.UserID, v.UserID)
	if len(v.ReceiveUnits) > 0 {
		ph := make([]string, len(v.ReceiveUnits))
		for i, id := range v.ReceiveUnits {
			ph[i] = "?"
			args = append(args, id)
		}
		parts = append(parts, "EXISTS (SELECT 1 FROM document_receipts rc WHERE rc.doc_id = p.id AND rc.unit_id IN ("+strings.Join(ph, ",")+"))")
	}
	if len(v.ActingDocIDs) > 0 {
		ph := make([]string, len(v.ActingDocIDs))
		for i, id := range v.ActingDocIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		parts = append(parts, "p.id IN ("+strings.Join(ph, ",")+")")
	}
	if v.HRLeaveEntity != 0 {
		parts = append(parts, "EXISTS (SELECT 1 FROM leave_requests lr WHERE lr.doc_id = p.id AND lr.entity_id = ?)")
		args = append(args, v.HRLeaveEntity)
	}
	if v.FinancePool {
		parts = append(parts, "EXISTS (SELECT 1 FROM approval_steps s WHERE s.pr_id = p.id AND s.round = p.round AND s.step_kind = 'finance' AND s.status IN ('pending','approved'))")
	}
	draft := "p.status <> 'draft'"
	if v.SeeDrafts {
		draft = "1 = 1"
	}
	rel := "(" + strings.Join(parts, " OR ") + ")"
	// 구매 품의서 전체 열람자(finance)는 구매 품의서를 임시저장까지 다 보고, 양식 문서는 관계 문서만 본다.
	all := ""
	if v.All {
		all = "COALESCE(p.form_id, 1) = 1 OR "
	}
	return "(p.requester_id = ? OR " + all + "(" + draft + " AND " + rel + "))", append([]any{v.UserID}, args...)
}
