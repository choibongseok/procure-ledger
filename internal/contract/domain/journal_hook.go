package domain

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B31 3.3: 증빙 판정 확정 → 자동 매입 전표. 규칙·금액 계산의 정본은 3.3 이고 구현은 erp(전표 서비스)다.
// 호출은 docverify.Service.Confirm 의 확정 트랜잭션 안(확정 갱신 뒤)에서 한 줄로 한다. 훅이 nil 이면(전표 기능 이전 구성) 호출하지 않는다.

// JournalOutcome 은 훅 결과다. 확정 응답에는 싣지 않는다(확정 결과·응답 형식은 변하지 않는다).
type JournalOutcome struct {
	Created   bool   // 임시 전표를 만들었다(JournalNo 가 채워진다)
	JournalNo string // JV-연도-번호
	Queued    bool   // 전표 대기에 올렸다(Reason 이 사유)
	Reason    string // 대기 사유(한국어 한 문장). 일치로 확정되지 않았거나 이미 전표가 있어 증빙만 연결했으면 빈 값
	Attached  bool   // 같은 입고의 기존 전표에 증빙만 연결했다(D8)
}

// PurchaseJournalHook 은 확정된 판정에서 자동 전표(또는 전표 대기 행)를 만든다.
//
// 계약
//   - tx 는 호출자(확정)의 트랜잭션이다. 훅은 이 tx 로만 읽고 쓴다(별도 연결·별도 트랜잭션 금지).
//   - v.ConfirmedOutcome 이 match 가 아니면 전표를 만들지 않고 전표 대기(불일치 확정·보류 확정)에 올린다. 미확정 판정은 호출하지 않는다.
//   - 전표 생성이 규칙 누락·계정 중지·거래처 없음·금액 불일치로 실패하면 **전표 대기 행을 쓰고 오류 없이** 돌려준다(확정은 막지 않는다, B31 3.3.7).
//     대기 행조차 쓰지 못하는 오류만 error 로 돌려주고, 그러면 호출자는 확정 전체를 취소한다.
//   - 금액·계정·거래처는 LLM 출력이나 문서 본문이 아니라 입고 줄·발주 줄·규칙 표에서만 온다.
//   - 같은 입고에는 취소되지 않은 매입 전표가 한 건이다. 이미 있으면 증빙만 연결한다(Attached).
//   - 전표 번호·감사는 훅이 같은 트랜잭션에서 남긴다(행위자는 확정한 재무 사용자, detail 에 auto=true).
type PurchaseJournalHook interface {
	OnVerdictConfirmed(ctx context.Context, tx contract.Tx, v Verdict) (JournalOutcome, error)
}
