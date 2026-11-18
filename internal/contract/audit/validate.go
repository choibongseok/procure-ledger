package audit

import (
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// ValidateEntry 는 기록기가 받아들이는 입력 조건이다. 실제 기록기(internal/audit)와 가짜(contract/fake)가
// 같은 함수를 써서, 가짜에서는 통과하고 실제에서는 조용히 사라지는 기록(빈 TargetID 등)이 없게 한다.
func ValidateEntry(e Entry) error {
	if e.Action == "" || e.TargetID == "" {
		return errors.New("action·targetID 는 필수입니다")
	}
	known := false
	for _, k := range TargetTypes {
		known = known || k == e.TargetType
	}
	if !known {
		return fmt.Errorf("알 수 없는 대상 종류 %q", e.TargetType)
	}
	switch e.Result {
	case ResultSuccess, ResultDenied, ResultError:
	default:
		return fmt.Errorf("알 수 없는 결과 %q", e.Result)
	}
	switch e.Actor.Channel {
	case contract.ChannelWeb, contract.ChannelMCP, contract.ChannelLLMGW, contract.ChannelCLI, contract.ChannelSCIM, contract.ChannelAPI:
	default:
		return fmt.Errorf("알 수 없는 경로 %q", e.Actor.Channel)
	}
	return nil
}
