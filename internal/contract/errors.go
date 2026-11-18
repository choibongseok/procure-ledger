package contract

import "errors"

// 계약 공통 오류. 핸들러·MCP 도구는 errors.Is 로 상태 코드·도구 오류로 옮긴다.
var (
	ErrUnauthenticated = errors.New("인증이 필요합니다")
	ErrForbidden       = errors.New("권한이 없습니다")
	// ErrSetupRequired 는 제한 세션(비밀번호 변경·2단계 등록 전)이 허용되지 않는 경로를 부를 때다. 403 으로 나간다.
	ErrSetupRequired   = errors.New("먼저 비밀번호 변경 또는 2단계 인증 등록을 마쳐야 합니다")
	ErrNotFound        = errors.New("대상을 찾을 수 없습니다")
	ErrInvalidState    = errors.New("현재 상태에서는 할 수 없는 동작입니다")
	ErrValidation      = errors.New("입력값이 올바르지 않습니다")
	ErrSelfApproval    = errors.New("본인이 올린 요청은 결재할 수 없습니다")
	ErrOverReceipt     = errors.New("발주 수량을 넘는 입고는 할 수 없습니다")
	ErrQuotaExceeded   = errors.New("일일 사용 한도를 넘었습니다")
	ErrModelNotAllowed = errors.New("허용되지 않은 모델입니다")
	ErrAuditFailed     = errors.New("감사 기록에 실패해 요청을 취소했습니다")
)
