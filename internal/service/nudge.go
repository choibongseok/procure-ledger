package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 결재 독촉(B29 ④, 5절).
//   - 기안자 본인만, 진행 중 문서의 지연된 단계(단계 시작 뒤 24시간, 달력 시간)에 대해서만 한다. 단계 시작은 B28 의 stepStart 한 곳이다.
//   - 받는 사람은 그 단계의 미처리 현재 처리자(배정자 + 유효한 대결자, 병렬 합의는 미처리자)이며 후보 계산(approversOf) 한 곳이 정한다.
//   - 같은 문서·차수·단계는 KST 달력일 하루 1회(UNIQUE 가 한 번 더 막는다).

// nudgeDelay 는 지연 기준이다(상수, 결정 D7).
const nudgeDelay = 24 * time.Hour

// NudgeService 는 domain.NudgeService 의 구현이다.
type NudgeService struct {
	Base
	Repo repository.SQLNudges
	Rpt  repository.SQLReport
}

var _ domain.NudgeService = (*NudgeService)(nil)

func NewNudgeService(b Base) *NudgeService { return &NudgeService{Base: b} }

func (s *NudgeService) elig() eligibility {
	return eligibility{approvals: repository.SQLApprovals{}, delegations: repository.SQLDelegations{}, purchases: repository.SQLPurchases{}, org: s.Org}
}

type delayedInfo struct {
	doc        repository.NudgeDoc
	seq        int
	title      string
	waiting    time.Duration
	recipients []int64
	names      []string
	started    bool // 단계 시작을 알 수 있는가
}

// currentStep 은 문서의 지금 차례 단계(가장 앞선 단계 번호의 대기 중 단계)와 경과 시간·받을 사람을 계산한다. ok 가 거짓이면 대기 중인 단계가 없다.
func (s *NudgeService) currentStep(ctx context.Context, tx contract.Tx, doc repository.NudgeDoc, now time.Time) (delayedInfo, bool, error) {
	info := delayedInfo{doc: doc}
	refs, err := repository.SQLApprovals{}.OpenSteps(ctx, tx, doc.ID, doc.Round)
	if err != nil {
		return info, false, err
	}
	stage := -1
	var cur []repository.StepRef
	for _, r := range refs {
		if r.Status != domain.StepPending {
			continue
		}
		if stage == -1 || r.Stage < stage {
			stage, cur = r.Stage, nil
		}
		if r.Stage == stage {
			cur = append(cur, r)
		}
	}
	if len(cur) == 0 {
		return info, false, nil
	}
	info.seq = cur[0].Seq
	info.title = stepTitleOf(s.Repo.StepTitle(ctx, tx, doc.ID, doc.Round, cur[0].Seq), string(cur[0].Kind))
	rt, err := s.Rpt.RoundData(ctx, tx, []int64{doc.ID})
	if err != nil {
		return info, false, err
	}
	if start, ok := stepStart(rt, doc.ID, doc.Round, stage); ok {
		info.started, info.waiting = true, now.Sub(start)
	}
	seen := map[int64]bool{}
	for _, r := range cur {
		ids, err := s.elig().approversOf(ctx, tx, r, now, 0, true)
		if err != nil {
			return info, false, err
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				info.recipients = append(info.recipients, id)
				info.names = append(info.names, s.Repo.UserName(ctx, tx, id))
			}
		}
	}
	return info, true, nil
}

func nudgeDay(t time.Time) string { return string(contract.DayOf(t)) }

func waitText(d time.Duration) string {
	m := int64(d / time.Minute)
	days, hours := m/1440, (m%1440)/60
	switch {
	case days > 0:
		return fmt.Sprintf("%d일 %d시간", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d시간", hours)
	}
	return fmt.Sprintf("%d분", m)
}

// Nudge 는 기안자가 지연된 단계의 현재 처리자에게 독촉 알림을 보낸다.
func (s *NudgeService) Nudge(ctx context.Context, docID int64) (domain.NudgeResult, error) {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "approval.nudge", audit.TargetApprovalDoc)
	if err != nil {
		return domain.NudgeResult{}, err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.NudgeResult{}, err
	}
	var out domain.NudgeResult
	err = s.write(ctx, p, "approval.nudge", audit.TargetApprovalDoc, docID, func(tx contract.Tx) error {
		doc, err := s.Repo.Doc(ctx, tx, docID)
		if err != nil {
			return err
		}
		if doc.RequesterID != p.UserID {
			return fmt.Errorf("%w: 기안자만 독촉할 수 있습니다.", contract.ErrForbidden)
		}
		if doc.Disposed || (doc.Status != string(domain.PRSubmitted) && doc.Status != string(domain.PRInReview)) {
			return fmt.Errorf("%w: 결재 진행 중인 문서만 독촉할 수 있습니다. 화면을 새로 고쳐 현재 상태를 확인하세요.", contract.ErrInvalidState)
		}
		now := s.now()
		info, ok, err := s.currentStep(ctx, tx, doc, now)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: 그 단계는 이미 처리되었습니다. 화면을 새로 고쳐 현재 상태를 확인하세요.", contract.ErrInvalidState)
		}
		if !info.started || info.waiting < nudgeDelay {
			return validation("아직 독촉할 단계가 없습니다. 결재 대기가 24시간을 넘으면 독촉할 수 있습니다.")
		}
		if len(info.recipients) == 0 {
			return fmt.Errorf("%w: 결재자가 없어 독촉할 수 없습니다. 관리자에게 결재자 변경을 요청하세요.", contract.ErrInvalidState)
		}
		day := nudgeDay(now)
		if _, done, err := s.Repo.LastToday(ctx, tx, docID, doc.Round, info.seq, day); err != nil {
			return err
		} else if done {
			return fmt.Errorf("%w: 오늘은 이미 독촉했습니다. 내일 다시 독촉할 수 있습니다.", contract.ErrInvalidState)
		}
		if err := s.Repo.AddNudge(ctx, tx, repository.NudgeInsert{DocID: docID, By: p.UserID, Round: doc.Round, Seq: info.seq, StepTitle: info.title, At: now, Day: day,
			Recipients: len(info.recipients), RecipientNames: strings.Join(info.names, ", ")}); err != nil {
			if errors.Is(err, repository.ErrAlreadyNudged) {
				return fmt.Errorf("%w: 오늘은 이미 독촉했습니다. 내일 다시 독촉할 수 있습니다.", contract.ErrInvalidState)
			}
			return err
		}
		link := "/docs/" + fmt.Sprint(docID)
		if doc.FormKind == "purchase" {
			link = "/approvals/" + fmt.Sprint(docID)
		}
		// 알림 본문에는 문서 제목만 넣는다(민감 필드 값은 넣지 않는다).
		body := fmt.Sprintf("기안자 %s님이 「%s」 결재를 요청했습니다(대기 %s). 지금 처리해 주세요.", doc.RequesterName, doc.Title, waitText(info.waiting))
		for _, id := range info.recipients {
			if err := s.notify(ctx, tx, repository.NotificationInsert{UserID: id, Kind: "approval_nudge", Title: "결재 독촉", Body: body, Link: link, RefType: "purchase_request", RefID: docID, Now: now}); err != nil {
				return err
			}
		}
		out = domain.NudgeResult{StepTitle: info.title, Recipients: len(info.recipients), WaitingMinutes: int64(info.waiting / time.Minute)}
		target := audit.TargetApprovalDoc
		if doc.FormKind == "purchase" {
			target = audit.TargetPurchaseRequest
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "approval.nudge", TargetType: target, TargetID: fmt.Sprint(docID), Result: audit.ResultSuccess, Detail: "결재 독촉",
			Diff: []audit.DiffItem{{Key: "step", New: info.title}, {Key: "nudged_to", New: fmt.Sprint(len(info.recipients))}, {Key: "waiting", New: waitText(info.waiting)}}})
	})
	return out, err
}

// Status 는 기안자의 문서 상세가 보여 줄 독촉 상태와 기록이다. 기안자가 아니면 ErrNotFound(존재를 숨긴다).
func (s *NudgeService) Status(ctx context.Context, docID int64) (domain.NudgeStatus, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftCreate)
	if err != nil {
		return domain.NudgeStatus{}, err
	}
	if ctx, err = s.prefetchOrgFacts(ctx); err != nil {
		return domain.NudgeStatus{}, err
	}
	doc, err := s.Repo.Doc(ctx, s.DB, docID)
	if err != nil {
		return domain.NudgeStatus{}, err
	}
	if doc.RequesterID != p.UserID {
		return domain.NudgeStatus{}, contract.ErrNotFound
	}
	out := domain.NudgeStatus{}
	if out.Records, err = s.Repo.ListByDoc(ctx, s.DB, docID); err != nil {
		return out, err
	}
	if doc.Disposed || (doc.Status != string(domain.PRSubmitted) && doc.Status != string(domain.PRInReview)) {
		return out, nil
	}
	now := s.now()
	info, ok, err := s.currentStep(ctx, s.DB, doc, now)
	if err != nil || !ok || !info.started || info.waiting < nudgeDelay {
		return out, err
	}
	if ok, herr := s.isBusinessDay(ctx, s.DB, nudgeDay(now)); herr == nil {
		out.Holiday = !ok
	}
	out.Delayed, out.StepTitle, out.WaitingMinutes, out.NoApprover = true, info.title, int64(info.waiting/time.Minute), len(info.recipients) == 0
	if at, done, err := s.Repo.LastToday(ctx, s.DB, docID, doc.Round, info.seq, nudgeDay(now)); err != nil {
		return out, err
	} else if done {
		out.DoneToday = &at
	}
	return out, nil
}
