// Package service 는 erp 업무 서비스(구매요청·결재·재고·입고)다.
// 웹 핸들러와 MCP 도구가 같은 메서드를 호출한다. HTTP 요청 객체에 의존하지 않는다.
// 권한은 contract.Authorize 로 진입에서 검사하고, 부서 격리는 서비스 검사와 저장소 조건절 두 겹으로 한다.
// 쓰기는 비즈니스 변경과 감사 기록을 한 트랜잭션에서 하며, 기록이 실패하면 전체가 롤백된다.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// Base 는 세 서비스가 공유하는 의존성이다.
type Base struct {
	DB     contract.Tx        // 읽기(트랜잭션 밖)
	Txs    contract.TxManager // 쓰기 트랜잭션
	Audit  audit.Recorder
	Masker contract.Masker
	Org    contract.OrgDirectory // 조직 조회(B21 결재선·열람 범위). nil 이면 결재선을 만들 수 없다
	// Privacy 는 개인정보 원문 열람 기록기다(B41). 원문이 응답에 실리는 경로가 응답 전에 부른다. nil 이면 기록 없이 통과한다(서버 조립은 항상 꽂는다).
	Privacy privacy.Noter
	// SoD 는 권한 분리 판정기다(B41 ③). 거래 판정 지점이 같은 트랜잭션 안에서 부른다. nil 이면 코드가 이미 막던 규칙(locked)만 내장 판정으로 막는다(sodCheck).
	SoD sod.Evaluator
	Now func() time.Time
	// Modules 는 업무 모듈 켜기 상태다(B50). nil 이면 켜진 것으로 본다(시험·직접 호출). 서버 조립이 internal/module.Service 를 꽂는다.
	Modules ModuleGate
	Attach  AttachLimits // 첨부 상한(B23). 비어 있으면 기본값을 쓴다. 기동 때 config 가 채운다
	// Deliver 는 앱 안 알림 한 건을 외부 채널(메일) 발송 건으로 큐에 올린다(B36, platform 의 notify.Service.Enqueue). nil 이면 앱 안 알림만 만든다.
	// 같은 트랜잭션에서 불리지만 오류는 알림 생성과 업무를 막지 않는다(notify 가 삼킨다).
	Deliver func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error
}

func (b Base) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

// authorize 는 권한을 검사하고, 로그인한 사용자의 거부는 감사 로그에 남긴다.
func (b Base) authorize(ctx context.Context, perm contract.Permission, action string, tt audit.TargetType) (contract.Principal, error) {
	p, err := contract.Authorize(ctx, perm)
	if err != nil {
		if errors.Is(err, contract.ErrForbidden) {
			b.recordDenied(ctx, p, action, tt, 0, string(perm))
		}
		return p, err
	}
	return p, nil
}

// recordDenied 는 거부 사실을 별도 트랜잭션으로 남긴다(요청은 이미 실패했으므로 기록 오류는 삼킨다).
func (b Base) recordDenied(ctx context.Context, p contract.Principal, action string, tt audit.TargetType, id int64, detail string) {
	_ = b.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return b.Audit.Record(ctx, tx, audit.Entry{
			Actor: p, Action: action, TargetType: tt, TargetID: fmt.Sprint(id), Result: audit.ResultDenied, Detail: detail,
		})
	})
}

// write 는 fn 을 한 트랜잭션에서 실행한다. 권한·자기 결재 거부는 롤백 뒤 감사 로그에 남긴다.
func (b Base) write(ctx context.Context, p contract.Principal, action string, tt audit.TargetType, targetID int64, fn func(tx contract.Tx) error) error {
	holder, _ := ctx.Value(orgFactsKey{}).(*orgFacts)
	var err error
	for attempt := 0; ; attempt++ {
		err = b.Txs.WithTx(ctx, func(tx contract.Tx) error {
			// 트랜잭션 밖에서 미리 읽은 조직 판정(prefetchOrgFacts)이 그 뒤 조직·권한 변경으로 낡았으면 거부한다.
			if holder != nil && holder.ver != "" {
				if ver, verr := (repository.SQLLines{}).OrgVersion(ctx, tx); verr != nil {
					return verr
				} else if ver != holder.ver {
					return errOrgChanged
				}
			}
			return fn(tx)
		})
		// 낡은 판정이면 사실을 다시 읽어 두 번까지 재시도한다(무관한 변경에 곧바로 거부하지 않는다).
		if holder != nil && errors.Is(err, errOrgChanged) && attempt < 2 {
			nf, lerr := b.loadOrgFacts(ctx)
			if lerr != nil {
				err = lerr
				break
			}
			*holder = nf
			continue
		}
		break
	}
	// SoD 차단(B41): 롤백된 뒤 차단 시도를 롤백 밖에 기록한다(위반 행·감사 sod.blocked·알림). 같은 사실의 일반 거부 기록은 중복이라 남기지 않는다.
	_, sodBlocked := sod.IsBlocked(err)
	if sodBlocked && b.SoD != nil {
		sod.NoteBlocked(ctx, b.SoD, err)
		return err
	}
	var closed *repository.MonthClosedError
	switch {
	case errors.As(err, &closed):
		// 마감된 월에 쓰려던 시도(B27): 서버 검사와 DB 트리거 어느 쪽이 막았든 같은 거부 기록을 한 줄 남긴다.
		b.recordDenied(ctx, p, action, tt, targetID, "마감된 월 "+closed.Month)
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, contract.ErrSelfApproval):
		b.recordDenied(ctx, p, action, tt, targetID, err.Error())
	case errors.Is(err, errLineUnavailable):
		// 결재선을 만들 수 없어 상신이 거부된 사실도 감사 로그에 남긴다(화면 버튼 비활성과 별개로 API 직접 호출도 같다).
		b.recordDenied(ctx, p, action, tt, targetID, err.Error())
	}
	return err
}

// assertMonthOpen 은 그 장부일(YYYY-MM-DD)이 속한 달이 마감되지 않았는지 확인하는 유일한 서비스 쪽 판정이다(B27, AC-913).
// 마감되었으면 "마감된 월입니다. 재무팀에 마감 해제를 요청하세요."(400)다. 원장·출고·실사 쓰기가 모두 이것을 부른다.
func (b Base) assertMonthOpen(ctx context.Context, q contract.Tx, day string) error {
	return repository.SQLMonthClose{}.AssertOpen(ctx, q, day)
}

// record 는 성공한 쓰기의 감사 기록이다. 실패하면 호출자는 그 오류로 트랜잭션을 끝내야 한다.
func (b Base) record(ctx context.Context, tx contract.Tx, p contract.Principal, action string, tt audit.TargetType, id int64, diff ...audit.DiffItem) error {
	// 이전·이후 값이 둘 다 비어 있는 칸(예: 입력하지 않은 비고·위치)은 기록하지 않는다(감사 화면에 빈 값이 "null" 로 남지 않게).
	kept := diff[:0:0]
	for _, d := range diff {
		if d.Old == "" && d.New == "" {
			continue
		}
		kept = append(kept, d)
	}
	diff = kept
	return b.Audit.Record(ctx, tx, audit.Entry{
		Actor: p, Action: action, TargetType: tt, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess, Diff: diff,
	})
}

// viewScope 는 저장소 조회에 넘길 열람 범위다(B21 3.5). purchase:read_all 이면 전체이고, 아니면 본인·같은 부서·조직장 범위(겸직·직무대행 포함)다.
func (b Base) viewScope(ctx context.Context, p contract.Principal) (repository.ViewScope, error) {
	allDocs := p.Can(contract.PermDocumentReadAll)
	v := repository.ViewScope{UserID: p.UserID, DeptID: p.DepartmentID, All: p.Can(contract.PermPurchaseReadAll), AllDocs: allDocs, FinancePool: p.Can(contract.PermFinanceStage)}
	// 전체 열람 권한자에게도 휴가 신청서는 관계 범위(신청자·결재선·직속 조직장·인사 담당)만 연다(B50 AC-5040). 인사 담당은 현재 법인의 휴가 신청서를 본다.
	v.LeaveGuard = v.All || v.AllDocs
	if p.Can(contract.PermAttendanceHR) {
		v.HRLeaveEntity = contract.WriteEntityID(ctx)
	}
	// 수신처 접수 담당자(조직장·직무대행·수신 담당자)는 결재 완료 후 그 수신 건의 문서를 본다.
	ru, err := b.receiveUnits(ctx, p.UserID)
	if err != nil {
		return v, err
	}
	v.ReceiveUnits = ru
	if v.ActingDocIDs, err = b.actingDocIDs(ctx, p.UserID); err != nil {
		return v, err
	}
	if b.Org != nil {
		ids, err := b.Org.HeadedScope(ctx, p.UserID, contract.DayOf(b.now()))
		switch {
		case err == nil:
			v.UnitIDs = ids
		case errors.Is(err, contract.ErrNotInOrg), errors.Is(err, contract.ErrNotFound):
		default:
			return v, err
		}
	}
	return v, nil
}

func validation(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

// actingDocIDs 는 위임(대결)으로 열람할 수 있는 진행 중 문서 번호다. 열람 가능 여부는 canView 한 곳이 정하고, 이 함수는 후보를 좁혀 canView 를 건다.
// 문서 상세·검색·목록이 모두 viewScope 를 거치므로 같은 범위를 쓴다(B29 AC-811).
func (b Base) actingDocIDs(ctx context.Context, userID int64) ([]int64, error) {
	e := eligibility{approvals: repository.SQLApprovals{}, delegations: repository.SQLDelegations{}, purchases: repository.SQLPurchases{}, lines: repository.SQLLines{}, org: b.Org}
	now := b.now()
	dels, err := e.delegations.ActiveTo(ctx, b.DB, userID, now)
	if err != nil || len(dels) == 0 {
		return nil, err
	}
	delegators := make([]int64, len(dels))
	for i, d := range dels {
		delegators[i] = d.DelegatorID
	}
	cands, err := repository.SQLDelegations{}.ActingCandidates(ctx, b.DB, userID, delegators)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, id := range cands {
		if ok, err := e.canView(ctx, b.DB, id, userID, now); err != nil {
			return nil, err
		} else if ok {
			out = append(out, id)
		}
	}
	return out, nil
}
