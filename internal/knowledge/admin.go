package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// Admin 은 domain.KnowledgeAdminService 의 구현이다(색인 현황·다시 색인·양식별 검색 포함). 권한 knowledge:manage.
type Admin struct {
	Txs    contract.TxManager
	DB     contract.Tx
	Audit  audit.Recorder
	Worker *Worker
	repo   repository.SQLKnowledge
}

var _ domain.KnowledgeAdminService = (*Admin)(nil)

func (a *Admin) authorize(ctx context.Context, action string) (contract.Principal, error) {
	p, err := contract.Authorize(ctx, contract.PermKnowledgeManage)
	if err == nil {
		return p, nil
	}
	if p.UserID != 0 {
		_ = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
			return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetKnowledge, TargetID: "0", Result: audit.ResultDenied})
		})
	}
	return p, err
}

// Status 는 색인 현황이다(제목 없음).
func (a *Admin) Status(ctx context.Context) (domain.KnowledgeStatus, error) {
	if _, err := a.authorize(ctx, "knowledge.status"); err != nil {
		return domain.KnowledgeStatus{}, err
	}
	return a.repo.Status(ctx, a.DB)
}

// Reindex 는 범위(법인 코드·종류, 빈 값은 전체)의 색인을 다시 만든다. 웹 화면 버튼(knowledge:manage)이 부른다.
func (a *Admin) Reindex(ctx context.Context, entityCode, kind string) (int, int, error) {
	p, err := a.authorize(ctx, "knowledge.reindex")
	if err != nil {
		return 0, 0, err
	}
	return a.reindex(ctx, p, entityCode, kind)
}

// ReindexSystem 은 서버 호스트 명령(erp-admin)이 쓰는 같은 구현이다(권한 판정 없이 시스템 행위자로 감사에 남는다).
func (a *Admin) ReindexSystem(ctx context.Context, actor contract.Principal, entityCode, kind string) (int, int, error) {
	return a.reindex(ctx, actor, entityCode, kind)
}

func (a *Admin) reindex(ctx context.Context, p contract.Principal, entityCode, kind string) (done, skipped int, err error) {
	if kind != "" && kind != repository.KSrcApproval && kind != repository.KSrcUpload && kind != repository.KSrcAttachment {
		return 0, 0, fmt.Errorf("%w: 종류는 approval_doc·attachment·upload 중에서 고르세요", contract.ErrValidation)
	}
	kinds := []string{repository.KSrcApproval, repository.KSrcAttachment, repository.KSrcUpload}
	if kind != "" {
		kinds = []string{kind}
	}
	for _, k := range kinds {
		ids, err := a.targets(ctx, entityCode, k)
		if err != nil {
			return done, skipped, err
		}
		for _, id := range ids {
			var changed bool
			if err := a.Txs.WithTx(ctx, func(tx contract.Tx) error {
				var c bool
				var e error
				switch k {
				case repository.KSrcUpload:
					c, e = a.Worker.upsertUpload(ctx, tx, id)
				case repository.KSrcAttachment:
					c, e = a.Worker.upsertAttachment(ctx, tx, id)
				default:
					c, e = a.Worker.upsertApproval(ctx, tx, id)
				}
				changed = c
				if c && e == nil {
					e = a.repo.BumpGeneration(ctx, tx, a.Worker.now())
				}
				return e
			}); err != nil {
				return done, skipped, fmt.Errorf("%s %d 를 색인하지 못했습니다: %w", k, id, err)
			}
			if changed {
				done++
			} else {
				skipped++
			}
		}
	}
	scope := entityCode
	if scope == "" {
		scope = "전체"
	}
	err = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "knowledge.reindex", TargetType: audit.TargetKnowledge, TargetID: "reindex", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "scope", New: scope}, {Key: "done", New: strconv.Itoa(done)}, {Key: "skipped", New: strconv.Itoa(skipped)}}})
	})
	return done, skipped, err
}

// targets 는 다시 색인이 볼 출처 번호다: 대상인 것(완료 문서·열려 있는 업로드)과, 색인에는 있는데 더는 대상이 아닌 것(한 번 보고 뺀다).
func (a *Admin) targets(ctx context.Context, entityCode, kind string) ([]int64, error) {
	var entityID int64
	if entityCode != "" {
		err := a.DB.QueryRowContext(ctx, `SELECT id FROM entities WHERE code = ?`, entityCode).Scan(&entityID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: 모르는 법인 코드입니다", contract.ErrValidation)
		}
		if err != nil {
			return nil, err
		}
	}
	var ids []int64
	err := a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var live []int64
		switch kind {
		case repository.KSrcUpload:
			rows, e := a.repo.ListUploads(ctx, tx, entityID)
			if e != nil {
				return e
			}
			for _, r := range rows {
				live = append(live, r.ID)
			}
		case repository.KSrcAttachment:
			var e error
			if live, e = a.repo.CompletedAttachmentIDs(ctx, tx, entityID); e != nil {
				return e
			}
		default:
			var e error
			if live, _, e = a.repo.CompletedDocIDs(ctx, tx, entityID); e != nil {
				return e
			}
		}
		indexed, e := a.repo.IndexedSourceIDs(ctx, tx, kind, entityID)
		if e != nil {
			return e
		}
		seen := map[int64]bool{}
		for _, id := range append(live, indexed...) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return nil
	})
	return ids, err
}

// CountTargets 는 다시 색인이 볼 문서 수다(erp-admin knowledge-reindex --dry-run, 모든 종류).
func (a *Admin) CountTargets(ctx context.Context, entityCode string) (int, error) {
	n := 0
	for _, k := range []string{repository.KSrcApproval, repository.KSrcAttachment, repository.KSrcUpload} {
		ids, err := a.targets(ctx, entityCode, k)
		if err != nil {
			return 0, err
		}
		n += len(ids)
	}
	return n, nil
}

// SetFormSearchable 은 양식별 "내용 검색에 포함" 을 바꾼다. 기존 문서는 조회 때 바로 반영되고(Openable 이 양식 값을 본다) 색인은 다음 다시 색인에서 정리된다.
func (a *Admin) SetFormSearchable(ctx context.Context, formCode string, searchable bool) error {
	p, err := a.authorize(ctx, "knowledge.form_searchable")
	if err != nil {
		return err
	}
	if repository.NeverSearchableForm(formCode) {
		return invalid("이 양식은 내용 검색에 포함할 수 없습니다(민감한 정보가 들어 있습니다)")
	}
	return a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		old, formID, err := a.repo.SetFormSearchable(ctx, tx, formCode, searchable)
		if err != nil {
			return err
		}
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "knowledge.form_searchable", TargetType: audit.TargetKnowledge, TargetID: strconv.FormatInt(formID, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "form_code", New: formCode}, {Key: "searchable", Old: strconv.FormatBool(old), New: strconv.FormatBool(searchable)}}})
	})
}
