package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 전결 규정 적용(B23 단계 3, 명세 3.13). 결재선을 만든 뒤 양식·금액에 맞는 규정을 찾아
// 최종 결재 단계(직책 층 F 이상의 첫 결재 단계) 뒤의 흐름 단계를 건너뜀("전결")으로 표시한다.
// 필수 합의 단계가 최종 결재 단계 뒤에 있으면 규정 적용을 포기하고 경고를 남긴다. 결과는 상신 스냅샷에 저장되어 규정이 바뀌어도 진행 중 문서는 그대로다.

// stepLevel 은 직책 단계의 층이다. 층이 없는 단계(재무 풀·기안자가 더한 결재·합의)는 0 이다.
func stepLevel(k domain.StepKind) int {
	switch k {
	case domain.StepTeamLead:
		return 1
	case domain.StepDeptHead:
		return 2
	case domain.StepDivisionHead:
		return 3
	case domain.StepCEO:
		return 4
	}
	return 0
}

func levelTitle(level int) string {
	switch level {
	case 1:
		return "팀장"
	case 2:
		return "부서장"
	case 3:
		return "본부장"
	case 4:
		return "대표"
	}
	return ""
}

// applyAuthority 는 steps 에 전결 표시를 하고 판정 결과를 돌려준다(규정이 없으면 nil). 호출자는 결재선 문제가 없을 때만 부른다.
func applyAuthority(ctx context.Context, q contract.Tx, form repository.FormRow, def domain.FormDefinition, amount int64, steps []domain.LineStep, notes *[]string) (*domain.AuthorityNote, error) {
	if def.Settings.AmountField == "" || amount <= 0 {
		return nil, nil // 금액 필드가 없는 양식은 전결 규정이 없다
	}
	rule, err := (repository.SQLAuthority{}).Find(ctx, q, form.ID, amount)
	if errors.Is(err, contract.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decideAuthority(rule, steps, notes), nil
}

// decideAuthority 는 규정 한 줄을 결재선에 적용한다(순수 함수). 적용할 곳이 없으면 nil 이다.
func decideAuthority(rule domain.AuthorityRule, steps []domain.LineStep, notes *[]string) *domain.AuthorityNote {
	finalIdx := -1
	for i, st := range steps {
		role := st.Role
		if role == "" {
			role = domain.RoleApprove
		}
		if st.Omitted || role != domain.RoleApprove {
			continue
		}
		if lv := stepLevel(st.Kind); lv > 0 && lv >= rule.FinalLevel {
			finalIdx = i
			break
		}
	}
	if finalIdx < 0 {
		return nil // 결재선에 그 층 이상의 결재 단계가 없으면 규정을 적용할 곳이 없다
	}
	note := &domain.AuthorityNote{RuleID: rule.ID, MinAmount: rule.MinAmount, MaxAmount: rule.MaxAmount, Slug: rule.PolicySlug, ClauseNo: rule.ClauseNo}
	var later []int
	for i := finalIdx + 1; i < len(steps); i++ {
		st := steps[i]
		if st.Omitted {
			continue
		}
		if st.Role == domain.RoleAgree && st.Required {
			note.Warning = fmt.Sprintf("필수 합의 단계가 %d번째 최종 결재 단계(%s) 뒤에 있어 전결 규정을 적용하지 않았습니다. 모든 단계를 거쳐 결재합니다.", steps[finalIdx].Order, steps[finalIdx].Title)
			*notes = append(*notes, note.Warning)
			return note
		}
		later = append(later, i)
	}
	note.FinalOrder, note.FinalTitle = steps[finalIdx].Order, steps[finalIdx].Title
	if note.FinalTitle == "" {
		note.FinalTitle = levelTitle(rule.FinalLevel)
	}
	for _, i := range later {
		steps[i].Skipped, steps[i].SkipNote = true, "전결"
		note.SkippedOrders = append(note.SkippedOrders, steps[i].Order)
	}
	return note
}

func authorityJSON(n *domain.AuthorityNote) string {
	if n == nil {
		return ""
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseAuthority(raw string) *domain.AuthorityNote {
	if raw == "" {
		return nil
	}
	var n domain.AuthorityNote
	if json.Unmarshal([]byte(raw), &n) != nil {
		return nil
	}
	return &n
}

// loadAuthority 는 문서의 현재 차수에 저장된 전결 판정 스냅샷을 채운다. 없거나 읽지 못하면 비워 둔다(표시만 빠진다).
func (b Base) loadAuthority(ctx context.Context, q contract.Tx, pr *domain.PurchaseRequest) {
	if raw, err := (repository.SQLAuthority{}).AuthorityJSON(ctx, q, pr.ID, pr.Round); err == nil {
		pr.Authority = parseAuthority(raw)
	}
}
