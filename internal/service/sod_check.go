package service

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// legacyBlocked 는 판정기 도입 전에 서비스 코드가 직접 비교로 막던 규칙이다(전표 작성자의 최종 승인, 카드 소지자의 소명 확인). 판정기가 없는 조립에서만 쓰인다:
// 판정기가 있으면 규칙 모드(끔·경고·차단)와 예외가 정한다.
var legacyBlocked = map[string]bool{sod.RuleJVCreateApprove: true, sod.RuleCardHolderConfirm: true}

// 권한 분리(SoD) 판정 호출(B41 ③, 명세 41 8절). 거래 판정 지점은 앞 단계를 한 사람(other)을 **같은 트랜잭션에서 읽어** 이 함수로 넘긴다.
// 업무 서비스는 사람 번호를 직접 비교해 거절하지 않는다: 같은 사람인지·모드·예외는 판정기가 정한다(other 가 행위자와 다를 때 판정기는 통과시킨다).
// 차단이면 *sod.BlockedError 를 돌려주고, 호출자는 그 오류로 트랜잭션을 끝내 롤백한다. Base.write 가 WithTx 뒤에 sod.NoteBlocked 로 롤백 밖에 기록한다.
//
// b.SoD 가 nil 이면(판정기를 꽂지 않은 시험·도구 조립) **locked 규칙과 판정기 도입 전부터 코드가 막던 규칙(legacyBlocked)** 만 내장 판정으로 막고 나머지는 통과시킨다.
// 그래서 locked 규칙(지급 작성자의 지급 처리 등)은 어떤 조립에서도 풀리지 않고, 판정기 없는 조립의 동작은 도입 전과 같다.
func (b Base) sodCheck(ctx context.Context, tx contract.Tx, p contract.Principal, rule string, other int64, ref sod.Ref) (sod.Decision, error) {
	if other == 0 {
		return sod.Decision{Mode: sod.ModeOff}, nil
	}
	c := sod.Check{Rule: rule, Actor: p, Subject: ref, Other: other}
	if b.SoD == nil {
		if r, ok := sod.RuleByCode(rule); ok && (r.Locked || legacyBlocked[rule]) && other == p.UserID && p.UserID != 0 {
			return sod.Decision{Mode: sod.ModeBlock, Violation: true}, &sod.BlockedError{Check: c}
		}
		return sod.Decision{Mode: sod.ModeOff}, nil
	}
	return b.SoD.Evaluate(ctx, tx, c)
}
