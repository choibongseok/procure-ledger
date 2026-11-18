package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// HashRecord 는 레코드의 체인 해시다. 직전 해시(PrevHash)와 시퀀스·시각·행위자·대상·결과·diff·사유를
// JSON 배열로 직렬화해 SHA-256 을 취한다(필드 경계가 모호해지지 않는다).
// 실제 기록기(internal/audit)와 가짜(contract/fake)가 같은 함수를 쓴다.
func HashRecord(rec Record) string {
	diff := rec.Diff
	if diff == nil {
		diff = []DiffItem{}
	}
	diffJSON, _ := json.Marshal(diff)
	parts := []any{
		rec.PrevHash, rec.Seq, rec.At.UTC().UnixNano(), rec.ActorID, string(rec.ActorRole),
		string(rec.Channel), rec.Action, string(rec.TargetType), rec.TargetID, string(rec.Result),
		string(diffJSON), rec.Detail,
	}
	// 법인(B44): 0 이 아닐 때만 배열 끝에 덧붙인다. 그래서 이관 전 레코드와 법인과 무관한 사건의 해시는 이전과 바이트 단위로 같고,
	// 법인이 있는 사건은 법인 값을 바꾸면(2→3, 2→0) 검증이 그 지점을 짚는다.
	if rec.EntityID != 0 {
		parts = append(parts, rec.EntityID)
	}
	payload, _ := json.Marshal(parts)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// EntityOf 는 기록이 남길 법인 번호다(B44). 명시값(양수)이 있으면 그대로, NoEntity 면 0(법인과 무관), 0 이면 요청 context 의 현재 법인이다.
// 요청 밖에서 도는 주체(전송 작업 등)는 법인을 명시하거나 context 에 실어야 한다(안 그러면 0 이라 법인 범위 감사 담당에게 보이지 않는다).
func EntityOf(ctx context.Context, e Entry) int64 {
	switch {
	case e.EntityID == NoEntity:
		return 0
	case e.EntityID > 0:
		return e.EntityID
	}
	return contract.EntityFrom(ctx)
}
