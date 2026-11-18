// Package sod 는 권한 분리(SoD) 규칙 판정이다(B41 ③). 8절.
//
// 거래 판정 지점(지급·전표·입고·카드·역할 부여 서비스)은 같은 사람이 서로 견제해야 할 두 단계를 모두 하려 할 때 **같은 트랜잭션 안에서**
// Evaluator.Evaluate 를 부른다. 판정은 규칙의 모드(끔·경고·차단)와 유효한 예외를 보고 정한다. 업무 서비스는 이 호출 밖에서 사람·역할을 직접 비교하지 않는다.
package sod

import (
	"context"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 모드.
const (
	ModeOff   = "off"
	ModeWarn  = "warn"
	ModeBlock = "block"
)

// 규칙 종류.
const (
	KindTxn  = "txn"  // 같은 거래에서 두 단계를 모두 수행
	KindHold = "hold" // 두 권한을 함께 보유
)

// 규칙 코드(명세 8.1). 새 규칙은 Rules 에 한 줄과 여기에 상수를 더한다.
const (
	RulePayCreateSettle    = "pay.create_settle"    // 지급 요청 작성 + 지급 처리(locked block)
	RulePayCreateApprove   = "pay.create_approve"   // 지급 요청 작성 + 최종 승인
	RulePayApproveSettle   = "pay.approve_settle"   // 지급 요청 승인 + 지급 처리
	RuleJVCreateApprove    = "jv.create_approve"    // 전표 작성 + 최종 승인
	RulePORequestReceive   = "po.request_receive"   // 구매요청 기안 + 입고 확인
	RuleCardHolderConfirm  = "card_holder_confirm"  // 카드 소지자 소명 + 소명 확인
	RuleRoleSelfGrant      = "role_self_grant"      // 역할 부여 대상이 부여자 본인
	RuleRoleRequestApprove = "role_request_approve" // 위험 역할 요청 + 승인(locked block)
	RuleTaxCreateIssue     = "tax.create_issue"     // 매출 세금계산서 작성 + 발행(B43)
	RuleAICostRuleConfirm  = "aicost.rule_confirm"  // AI 비용 배부 규칙 저장 + 월 확정(B49)
	RuleAICostConfirmPost  = "aicost.confirm_post"  // AI 비용 배부 확정 + 그 전표 승인(B49)
)

// Rule 은 코드에 정의된 규칙 하나다. 모드만 화면에서 바꾼다.
type Rule struct {
	Code    string
	Kind    string
	Title   string
	StepA   string // 앞 단계(한국어)
	StepB   string // 뒤 단계
	Default string // 초기 모드
	Locked  bool   // 코드가 이미 막는 규칙이라 모드를 낮출 수 없고 예외도 만들 수 없다
	// hold 규칙의 두 권한(Kind==KindHold 일 때)
	PermA, PermB contract.Permission
}

// Rules 는 규칙 표다(명세 8.1, D10). hold 규칙은 B33 access.SeparationRules 6개와 같은 쌍을 옮긴 것이다.
var Rules = []Rule{
	{Code: RulePayCreateSettle, Kind: KindTxn, Title: "지급 요청 작성자의 지급 처리", StepA: "지급 요청 작성", StepB: "지급 처리", Default: ModeBlock, Locked: true},
	{Code: RulePayCreateApprove, Kind: KindTxn, Title: "지급 요청 작성자의 최종 승인", StepA: "지급 요청 작성", StepB: "지급 요청 최종 승인", Default: ModeWarn},
	{Code: RulePayApproveSettle, Kind: KindTxn, Title: "지급 승인자의 지급 처리", StepA: "지급 요청 승인", StepB: "지급 처리", Default: ModeBlock},
	{Code: RuleJVCreateApprove, Kind: KindTxn, Title: "전표 작성자의 최종 승인", StepA: "전표 작성", StepB: "전표 최종 승인", Default: ModeWarn},
	{Code: RulePORequestReceive, Kind: KindTxn, Title: "구매요청 기안자의 입고 확인", StepA: "구매요청 기안", StepB: "입고 확인", Default: ModeWarn},
	{Code: RuleCardHolderConfirm, Kind: KindTxn, Title: "카드 소지자의 소명 확인", StepA: "카드 소명", StepB: "소명 확인", Default: ModeBlock},
	{Code: RuleRoleSelfGrant, Kind: KindTxn, Title: "자기 자신에게 역할 부여", StepA: "역할 부여", StepB: "부여 대상(본인)", Default: ModeBlock},
	{Code: RuleRoleRequestApprove, Kind: KindTxn, Title: "위험 역할 요청자의 승인", StepA: "위험 역할 부여 요청", StepB: "승인", Default: ModeBlock, Locked: true},
	{Code: RuleTaxCreateIssue, Kind: KindTxn, Title: "세금계산서 작성자의 발행", StepA: "세금계산서 작성", StepB: "세금계산서 발행", Default: ModeWarn},
	{Code: RuleAICostRuleConfirm, Kind: KindTxn, Title: "배부 규칙 작성자의 월 확정", StepA: "배부 규칙 저장", StepB: "월 배부 확정", Default: ModeWarn},
	{Code: RuleAICostConfirmPost, Kind: KindTxn, Title: "배부 확정자의 전표 승인", StepA: "월 배부 확정", StepB: "비용 전표 승인", Default: ModeBlock},
	{Code: "hold.ledger_payment", Kind: KindHold, Title: "전표 입력 + 지급 권한 보유", StepA: "전표 입력", StepB: "지급", Default: ModeWarn, PermA: contract.PermLedgerWrite, PermB: contract.PermPaymentWrite},
	{Code: "hold.payment_audit", Kind: KindHold, Title: "지급 + 감사 검증 권한 보유", StepA: "지급", StepB: "감사 검증", Default: ModeWarn, PermA: contract.PermPaymentWrite, PermB: contract.PermAuditVerify},
	{Code: "hold.role_audit", Kind: KindHold, Title: "역할 관리 + 감사 검증 권한 보유", StepA: "역할 관리", StepB: "감사 검증", Default: ModeWarn, PermA: contract.PermRoleManage, PermB: contract.PermAuditVerify},
	{Code: "hold.card_manage_confirm", Kind: KindHold, Title: "카드 관리 + 소명 확인 권한 보유", StepA: "카드 관리", StepB: "소명 확인", Default: ModeWarn, PermA: contract.PermCardManage, PermB: contract.PermCardConfirm},
	{Code: "hold.inventory_close", Kind: KindHold, Title: "재고 관리 + 월 마감 권한 보유", StepA: "재고 관리", StepB: "월 마감", Default: ModeWarn, PermA: contract.PermInventoryManage, PermB: contract.PermInventoryClose},
	{Code: "hold.purchase_decide", Kind: KindHold, Title: "구매 요청 + 결재 권한 보유", StepA: "구매 요청", StepB: "결재", Default: ModeWarn, PermA: contract.PermPurchaseCreate, PermB: contract.PermApprovalDecide},
	{Code: "hold.bypass_logread", Kind: KindHold, Title: "원문 열람 + 조회 기록 조회 권한 보유", StepA: "원문 열람", StepB: "조회 기록 조회", Default: ModeWarn, PermA: contract.PermMaskBypass, PermB: contract.PermPrivacyLogRead},
	{Code: "hold.api_manage_ledger", Kind: KindHold, Title: "연동 토큰 관리 + 전표 입력 권한 보유", StepA: "연동 토큰 관리", StepB: "전표 입력", Default: ModeWarn, PermA: contract.PermAPIManage, PermB: contract.PermLedgerWrite},
	{Code: "hold.taxasp_issue", Kind: KindHold, Title: "대행사 설정 + 세금계산서 발행 권한 보유", StepA: "대행사 설정", StepB: "세금계산서 발행", Default: ModeWarn, PermA: contract.PermTaxASPManage, PermB: contract.PermTaxInvoiceIssue},
	{Code: "hold.sod_manage_approve", Kind: KindHold, Title: "규칙·예외 요청 + 예외 승인 권한 보유", StepA: "예외 요청", StepB: "예외 승인", Default: ModeWarn, PermA: contract.PermSoDManage, PermB: contract.PermSoDExceptionApprove},
}

// RuleByCode 는 규칙을 찾는다.
func RuleByCode(code string) (Rule, bool) {
	for _, r := range Rules {
		if r.Code == code {
			return r, true
		}
	}
	return Rule{}, false
}

// Ref 는 판정 대상 거래다(사람이 읽는 번호 포함).
type Ref struct {
	Type string // payment | journal | purchase_request | card_txn | role_grant | role_change_request ...
	ID   int64
	No   string // 지급 번호·전표 번호 등(없으면 "")
}

// Check 는 판정 요청이다. Other 는 앞 단계를 한 사람이고, 0 이면 판정 대상이 아니다(통과).
type Check struct {
	Rule    string
	Actor   contract.Principal
	Subject Ref
	Other   int64
}

// Decision 은 판정 결과다. 차단이면 Evaluate 가 오류(*BlockedError)를 함께 돌려준다.
type Decision struct {
	Mode      string // 적용된 모드(off 면 규칙이 꺼짐 또는 같은 사람이 아님)
	Violation bool   // 같은 사람이 두 단계를 모두 함(경고·예외 포함)
	Excepted  bool   // 유효한 예외로 통과
}

// BlockedError 는 차단 규칙에 걸린 시도다. contract.ErrForbidden 계열이며 롤백한 뒤 RecordBlocked 로 롤백 밖에 남긴다.
type BlockedError struct{ Check Check }

func (e *BlockedError) Error() string {
	r, _ := RuleByCode(e.Check.Rule)
	if r.Kind == KindHold {
		return fmt.Sprintf("이 변경으로 사용자가 %s 와(과) %s 권한을 함께 갖게 되어 차단되었습니다.", r.StepA, r.StepB)
	}
	return fmt.Sprintf("같은 사람이 %s 와(과) %s 을(를) 모두 할 수 없습니다. 예외 승인이 필요하면 관리자에게 요청하세요.", r.StepA, r.StepB)
}
func (e *BlockedError) Is(target error) bool { return target == contract.ErrForbidden }

// IsBlocked 는 오류가 SoD 차단인지, 맞으면 그 시도를 돌려준다.
func IsBlocked(err error) (Check, bool) {
	var b *BlockedError
	if errors.As(err, &b) {
		return b.Check, true
	}
	return Check{}, false
}

// Evaluator 는 판정 지점이 받는 인터페이스다. 구현은 *Engine, 시험은 Allow.
type Evaluator interface {
	// Evaluate 는 같은 트랜잭션 안에서 부른다. 모드 off → 통과. warn → 같은 트랜잭션에 위반(allowed)·감사 sod.violation·알림을 쓰고 통과.
	// block → 유효한 예외가 있으면 통과(excepted, 예외 소진), 없으면 *BlockedError 를 돌려준다(호출자가 그 오류로 WithTx 를 끝내 롤백).
	Evaluate(ctx context.Context, tx contract.Tx, c Check) (Decision, error)
	// RecordBlocked 는 차단된 시도를 롤백 밖의 별도 트랜잭션에 남긴다(sod_violations blocked + 감사 sod.blocked(거부) + 알림). 실패는 로그만 남긴다.
	RecordBlocked(ctx context.Context, c Check)
}

// NoteBlocked 는 서비스 쓰기 래퍼가 WithTx 가 끝난 뒤 부르는 도우미다: err 가 SoD 차단이면 RecordBlocked 한다.
func NoteBlocked(ctx context.Context, ev Evaluator, err error) {
	if ev == nil || err == nil {
		return
	}
	if c, ok := IsBlocked(err); ok {
		ev.RecordBlocked(ctx, c)
	}
}

type allowAll struct{}

func (allowAll) Evaluate(context.Context, contract.Tx, Check) (Decision, error) {
	return Decision{Mode: ModeOff}, nil
}
func (allowAll) RecordBlocked(context.Context, Check) {}

// Allow 는 아무것도 막거나 기록하지 않는 Evaluator 다. 규칙 저장소가 없는 시험 환경에서만 쓴다(운영 조립은 항상 *Engine).
var Allow Evaluator = allowAll{}
