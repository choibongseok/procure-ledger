package org

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B33 재판정 3: 어떤 인사 발령이든 그 결과 대상자의 실효 권한에 위험 권한이 새로 생기면 발령자는 role:manage 가 있어야 한다.
// 입사·재입사·전보·겸직·직위 변경·조직장 보임/해제·정정·조직 CSV 반영이 모두 applyCommands 를 거치므로 이 한 곳의 판정을 쓴다.
// 발령 적용 전후의 실효 권한을 같은 트랜잭션 안에서 날짜별(오늘과 미래 효력일)로 계산해 차집합 ∩ 위험 권한으로 판정한다.
// role:manage 가 있으면 허용하고 감사 상세(gained_risk)에 남긴다(조직·직위 부여 자체는 이미 2인 확인을 거쳤다). 미리보기는 발령자와 무관하게 경고만 보인다.

// RiskGainError 는 role:manage 없이 위험 권한이 새로 생기는 발령을 하려는 거부다(400, contract.ErrValidation).
type RiskGainError struct {
	Perms []string // 새로 생기는 위험 권한(영문 키, 감사용)
}

func (e *RiskGainError) labels() string {
	out := make([]string, len(e.Perms))
	for i, p := range e.Perms {
		out[i] = access.RiskLabel(p)
	}
	return strings.Join(out, ", ")
}

func (e *RiskGainError) Error() string {
	return "위험 권한이 새로 생기는 발령은 역할 관리 권한이 필요합니다: " + e.labels()
}
func (e *RiskGainError) Is(target error) bool { return target == contract.ErrValidation }

// riskNeeded 는 이 명령 묶음이 권한을 바꿀 수 있는지다(조직 신설·이름 변경·정렬·퇴사만이면 계산하지 않는다).
func riskNeeded(cmds []Command) bool {
	for _, c := range cmds {
		switch c.Kind {
		case KindUnitCreate, KindUnitRename, KindUnitSort, KindLeave:
		default:
			return true
		}
	}
	return false
}

type riskState struct {
	days   []contract.Day
	before map[contract.Day]map[int64]map[string]bool
}

// riskBefore 는 발령 적용 전의 위험 권한을 날짜별로 읽는다(오늘과 미래 효력일). 끝나면 캐시를 오늘 기준으로 되돌린다.
func (s *Service) riskBefore(ctx context.Context, tx contract.Tx, cmds []Command, today contract.Day) (*riskState, error) {
	if !riskNeeded(cmds) {
		return nil, nil
	}
	seen := map[contract.Day]bool{today: true}
	days := []contract.Day{today}
	for _, c := range cmds {
		if d := c.Effective; d > today && !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	st := &riskState{days: days, before: map[contract.Day]map[int64]map[string]bool{}}
	for _, d := range days {
		snap, err := s.riskAt(ctx, tx, d)
		if err != nil {
			return nil, err
		}
		st.before[d] = snap
	}
	return st, access.Recompute(ctx, tx, today)
}

func (s *Service) riskAt(ctx context.Context, tx contract.Tx, day contract.Day) (map[int64]map[string]bool, error) {
	if err := access.RecomputeAs(ctx, tx, day, true); err != nil {
		return nil, err
	}
	return access.RiskSnapshot(ctx, tx)
}

// riskAfter 는 발령 적용 뒤의 위험 권한을 같은 날짜들로 읽어 새로 생긴 것(사용자 → 권한)을 돌려주고 캐시를 오늘 기준으로 되돌린다.
func (s *Service) riskAfter(ctx context.Context, tx contract.Tx, st *riskState, today contract.Day) (map[int64][]string, error) {
	if st == nil {
		return nil, nil
	}
	gained := map[int64]map[string]bool{}
	for _, d := range st.days {
		after, err := s.riskAt(ctx, tx, d)
		if err != nil {
			return nil, err
		}
		for uid, perms := range after {
			for p := range perms {
				if !st.before[d][uid][p] {
					if gained[uid] == nil {
						gained[uid] = map[string]bool{}
					}
					gained[uid][p] = true
				}
			}
		}
	}
	if err := access.Recompute(ctx, tx, today); err != nil {
		return nil, err
	}
	out := map[int64][]string{}
	for uid, set := range gained {
		for p := range set {
			out[uid] = append(out[uid], p)
		}
		sort.Strings(out[uid])
	}
	return out, nil
}

// applyRiskGain 은 새로 생긴 위험 권한을 판정한다. 미리보기는 경고를 pv 에 담고, 실제 반영은 role:manage 가 없는 발령자를 거부한다.
// 시스템 발령자(개발용 시드·기동 이관)는 판정하지 않는다. 허용된 경우 대상 사용자의 결과(out)에 GainedRisk 를 남긴다.
func (s *Service) applyRiskGain(ctx context.Context, tx contract.Tx, actor contract.Principal, gained map[int64][]string, outs []Outcome, pv *Preview) error {
	if len(gained) == 0 || actor.UserID == 0 && pv == nil {
		return nil
	}
	var all []string
	seen := map[string]bool{}
	uids := make([]int64, 0, len(gained))
	for uid := range gained {
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	for _, uid := range uids {
		for _, p := range gained[uid] {
			if !seen[p] {
				seen[p] = true
				all = append(all, p)
			}
		}
		if pv != nil {
			ls := make([]string, len(gained[uid]))
			for i, p := range gained[uid] {
				ls[i] = access.RiskLabel(p)
			}
			pv.Warnings = append(pv.Warnings, fmt.Sprintf("이 발령으로 %s 님에게 위험 권한(%s)이 생깁니다. 위험 권한이 새로 생기는 발령은 역할 관리 권한이 있는 사람만 반영할 수 있습니다.", s.userName(ctx, tx, uid), strings.Join(ls, ", ")))
		}
	}
	if pv != nil {
		return nil
	}
	sort.Strings(all)
	if !actor.Can(contract.PermRoleManage) {
		return &RiskGainError{Perms: all}
	}
	// 허용: 감사에 남길 수 있게 대상 사용자의 명령 결과에 붙인다(대상이 없는 묶음은 첫 결과에).
	attached := false
	for i := range outs {
		if p, ok := gained[outs[i].UserID]; ok && outs[i].UserID != 0 {
			outs[i].GainedRisk = append([]string(nil), p...)
			attached = true
		}
	}
	if !attached && len(outs) > 0 {
		outs[0].GainedRisk = all
	}
	return nil
}
