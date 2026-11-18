package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// errLineUnavailable 은 결재선을 만들 수 없어 상신이 거부된 오류의 종류다. lineUnavailable 이 이 값과 contract.ErrValidation 에 해당한다.
var errLineUnavailable = errors.New("결재선을 만들 수 없습니다")

// lineUnavailable 은 결재선 불가 오류다. 문구는 단계별 사유(Problems)를 이은 한국어 문장이라 400 본문으로 그대로 나간다.
type lineUnavailable struct{ problems []string }

func (e lineUnavailable) Error() string { return strings.Join(e.problems, " ") }

func (e lineUnavailable) Is(target error) bool {
	return target == errLineUnavailable || target == contract.ErrValidation
}

// snapshotSteps 는 미리보기 결과에서 저장할 단계(생략된 단계 제외)를 순서대로 만든다.
func snapshotSteps(line domain.LinePreview) []repository.StepSnapshot {
	var out []repository.StepSnapshot
	for _, st := range line.Steps {
		if st.Omitted {
			continue
		}
		out = append(out, repository.StepSnapshot{
			Seq: len(out) + 1, Kind: st.Kind, Role: stepRoleApprove, UnitID: st.UnitID, UnitPath: st.UnitPath, Title: st.Title,
			Acting: st.Acting, RaisedNote: st.RaisedNote, LayerNote: st.LayerNote, Assignee: st.ApproverID, AssigneeName: st.ApproverName,
		})
	}
	return out
}

// lineSummary 는 감사 로그의 `line` 값이다. 예: "1:구매1팀 팀장 구매팀장 / 2:구매팀 부서장 구매부서장 / 3:재무".
func lineSummary(line domain.LinePreview) string {
	var parts []string
	n := 0
	for _, st := range line.Steps {
		if st.Omitted {
			continue
		}
		n++
		if st.Kind == domain.StepFinance {
			parts = append(parts, fmt.Sprintf("%d:%s", n, st.Title))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d:%s %s %s", n, st.UnitName, st.Title, st.ApproverName))
	}
	return strings.Join(parts, " / ")
}

func notesJSON(notes []string) string {
	if notes == nil {
		notes = []string{}
	}
	b, _ := json.Marshal(notes)
	return string(b)
}

// decorate 는 결재 진행 중 건의 아직 끝나지 않은 직책 단계에 "상신 당시와 다른 현재 조직장"(HeadChangedTo)을 채운다(명세 3.4).
// 재지정된 단계와 조직장을 알 수 없는 경우는 비워 둔다. 조직 조회 실패는 표시만 빠뜨리고 오류로 올리지 않는다.
func (b Base) decorate(ctx context.Context, q contract.Tx, pr *domain.PurchaseRequest) error {
	if b.Org == nil || (pr.Status != domain.PRSubmitted && pr.Status != domain.PRInReview) {
		return nil
	}
	day := contract.DayOf(b.now())
	for i := range pr.Steps {
		st := &pr.Steps[i]
		if st.UnitID == 0 || st.AssigneeID == 0 || st.ReassignedFrom != "" || (st.Status != domain.StepPending && st.Status != domain.StepWaiting) {
			continue
		}
		h, err := b.Org.Head(ctx, st.UnitID, day)
		if err != nil {
			continue
		}
		switch {
		case h.Vacant:
			st.HeadChangedTo = "공석"
		case h.UserID != st.AssigneeID:
			u, err := repository.SQLLines{}.User(ctx, q, h.UserID)
			if err == nil {
				st.HeadChangedTo = u.Name
			}
		}
	}
	return nil
}
