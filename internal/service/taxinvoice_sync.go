package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 수신 동기화의 서비스 쪽(B43 2단계, 명세 6.1). 대행사 수신 목록을 읽는 작업(internal/taxasp 의 Syncer)은 domain.TaxSyncStore 로만 DB 를 건드린다.
//   - 승인번호가 키다: 새 번호는 accepted 로 삽입, 같은 내용은 건너뜀, 다른 내용은 충돌로 기록만 하고 기존 행을 덮어쓰지 않는다(D11).
//   - 형식 오류 건·공급받는자가 우리 회사가 아닌 건은 그 건만 세고 건너뛴다(동기화 전체를 멈추지 않는다).
//   - 동기화 진행 상태는 app_settings 의 taxinvoice.sync_* 키에 둔다(대행사 설정 저장소 tax_asp_settings 와 독립).

var (
	_ domain.TaxSyncStore   = (*TaxInvoiceService)(nil)
	_ domain.TaxSyncControl = (*TaxInvoiceService)(nil)
)

// 진행 상태는 tax_sync_state(0070, 행 1개)에 둔다: 커서·읽는 창(last_from~last_to)·마지막 실행 시각·결과 코드·실패 시각.
// 마지막으로 끝까지 읽은 날과 마지막 건수는 그 표에 칸이 없어 app_settings 의 taxinvoice.sync_* 키에 둔다(비밀 아님).

const syncKeyPrefix = "taxinvoice.sync_"

// syncKey 는 법인별 동기화 설정 키다(B44): 기본 법인은 이전 키 그대로, 다른 법인은 taxinvoice.sync_e<번호>_ 접두다.
func syncKey(ctx context.Context, key string) string {
	if id := contract.WriteEntityID(ctx); id != contract.DefaultEntityID {
		return fmt.Sprintf("%se%d_%s", syncKeyPrefix, id, key)
	}
	return syncKeyPrefix + key
}

func (s *TaxInvoiceService) setting(ctx context.Context, q contract.Tx, key string) string {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, syncKey(ctx, key)).Scan(&v); err != nil {
		return ""
	}
	return v
}

func (s *TaxInvoiceService) putSetting(ctx context.Context, q contract.Tx, key, value string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO app_settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, syncKey(ctx, key), value)
	return err
}

// SyncState 는 동기화 진행 상태다.
func (s *TaxInvoiceService) SyncState(ctx context.Context) (domain.TaxSyncState, error) {
	return s.readSyncState(ctx, s.DB), nil
}

func (s *TaxInvoiceService) readSyncState(ctx context.Context, q contract.Tx) domain.TaxSyncState {
	var st domain.TaxSyncState
	var runAt string
	_ = q.QueryRowContext(ctx, `SELECT cursor, last_from, last_to, last_run_at, last_result FROM tax_sync_state WHERE entity_id = ?`, contract.WriteEntityID(ctx)).Scan(&st.Cursor, &st.WinFrom, &st.WinTo, &runAt, &st.LastResult)
	st.LastRunAt = parseTaxTime(runAt)
	st.LastDone = s.setting(ctx, q, "last_done")
	_ = json.Unmarshal([]byte(s.setting(ctx, q, "last_counts")), &st.LastCounts)
	return st
}

// SaveSyncState 는 진행 상태(커서·창·마지막으로 끝난 날)를 저장한다. 결과 코드·시각·건수는 FinishSync 가 쓴다.
func (s *TaxInvoiceService) SaveSyncState(ctx context.Context, st domain.TaxSyncState) error {
	return s.Txs.WithTx(ctx, func(tx contract.Tx) error { return s.saveProgress(ctx, tx, st) })
}

func (s *TaxInvoiceService) saveProgress(ctx context.Context, tx contract.Tx, st domain.TaxSyncState) error {
	if err := ensureSyncRow(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tax_sync_state SET cursor = ?, last_from = ?, last_to = ? WHERE entity_id = ?`, st.Cursor, st.WinFrom, st.WinTo, contract.WriteEntityID(ctx)); err != nil {
		return err
	}
	return s.putSetting(ctx, tx, "last_done", st.LastDone)
}

// ensureSyncRow 는 현재 법인의 진행 상태 행이 없으면 만든다(법인마다 한 행).
func ensureSyncRow(ctx context.Context, tx contract.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO tax_sync_state (entity_id) VALUES (?) ON CONFLICT (entity_id) DO NOTHING`, contract.WriteEntityID(ctx))
	return err
}

// SyncEntities 는 대행사 연동이 켜져 있는 법인 번호들이다(자동 동기화가 법인마다 한 번씩 돈다).
func (s *TaxInvoiceService) SyncEntities(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT entity_id FROM tax_asp_settings WHERE active = 1 ORDER BY entity_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CompanyBizNo 는 우리 회사 사업자번호(숫자만)다.
func (s *TaxInvoiceService) CompanyBizNo(ctx context.Context) (string, error) {
	p, err := s.companyParty(ctx, s.DB)
	return p.BizNo, err
}

// AuthorizeSync 는 수동 동기화 실행 권한(taxinvoice:write)을 확인한다.
func (s *TaxInvoiceService) AuthorizeSync(ctx context.Context) error {
	_, err := s.authorize(ctx, contract.PermTaxInvoiceWrite, "taxinvoice.sync", audit.TargetTaxInvoice)
	return err
}

// SyncStatus 는 마지막 동기화 상태다(taxinvoice:read).
func (s *TaxInvoiceService) SyncStatus(ctx context.Context) (domain.TaxSyncState, error) {
	if _, err := contract.Authorize(ctx, contract.PermTaxInvoiceRead); err != nil {
		return domain.TaxSyncState{}, err
	}
	return s.readSyncState(ctx, s.DB), nil
}

func syncActor(ctx context.Context) contract.Principal {
	if p, ok := contract.PrincipalFrom(ctx); ok && p.UserID != 0 {
		return p
	}
	return taxSystemActor
}

// validateReceived 는 수신 건의 형식을 검사한다. 사유 문장은 값을 담지 않는다.
func validateReceived(it domain.TaxReceivedItem) string {
	if !reApproval.MatchString(strings.TrimSpace(it.ApprovalNo)) {
		return "승인번호 형식"
	}
	if !validTaxType(it.TaxType) {
		return "과세 유형"
	}
	if !validTaxDay(it.IssueDate) {
		return "작성일 형식"
	}
	sup := normParty(it.Supplier)
	if len(sup.BizNo) != 10 || sup.Name == "" {
		return "공급자 사업자번호·상호"
	}
	if it.AmendReason == "" && (it.SupplyTotal <= 0 || it.TaxTotal < 0) {
		return "금액"
	}
	if it.AmendReason != "" {
		if _, ok := domain.AmendReasonLabels[it.AmendReason]; !ok {
			return "수정 사유"
		}
	}
	if it.TaxType != domain.TaxTaxable && it.TaxTotal != 0 {
		return "세액"
	}
	if len(it.Lines) > 0 {
		var sum int64
		for _, l := range it.Lines {
			sum += l.Supply
		}
		if sum != it.SupplyTotal {
			return "품목 합계"
		}
	}
	return ""
}

// ApplyReceived 는 수신 건들을 한 트랜잭션에서 반영한다.
func (s *TaxInvoiceService) ApplyReceived(ctx context.Context, items []domain.TaxReceivedItem) (domain.TaxSyncCounts, error) {
	var c domain.TaxSyncCounts
	actor := syncActor(ctx)
	err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		c = domain.TaxSyncCounts{}
		company, err := s.companyParty(ctx, tx)
		if err != nil {
			return err
		}
		if len(company.BizNo) != 10 {
			return fmt.Errorf("%w: 회사 정보에 사업자번호가 없어 수신 세금계산서를 가져올 수 없습니다", contract.ErrInvalidState)
		}
		owner, err := s.syncOwner(ctx, tx, actor)
		if err != nil {
			return err
		}
		for _, it := range items {
			if validateReceived(it) != "" {
				c.Invalid++
				continue
			}
			approval := strings.ToUpper(strings.TrimSpace(it.ApprovalNo))
			buyer := normParty(it.Buyer)
			if buyer.BizNo != company.BizNo {
				c.NotOurs++
				continue
			}
			sup := normParty(it.Supplier)
			if oid, _, err := s.Repo.IDByApproval(ctx, tx, approval); err != nil {
				return err
			} else if oid != 0 {
				cur, err := s.Repo.Get(ctx, tx, oid)
				if err != nil {
					return err
				}
				if cur.Supplier.BizNo == sup.BizNo && cur.Buyer.BizNo == buyer.BizNo && cur.IssueDate == it.IssueDate && cur.SupplyTotal == it.SupplyTotal && cur.TaxTotal == it.TaxTotal {
					c.Same++
					continue
				}
				c.Conflict++
				if err := s.record(ctx, tx, actor, "taxinvoice.sync_conflict", audit.TargetTaxInvoice, oid, audit.DiffItem{Key: "approval_no", New: approval}); err != nil {
					return err
				}
				continue
			}
			v := domain.TaxInvoice{Direction: domain.TaxPurchase, Source: domain.TaxSourceASP, Status: domain.TaxAccepted, ApprovalNo: approval, TaxType: it.TaxType, IssueDate: it.IssueDate,
				Supplier: sup, Buyer: buyer, SupplyTotal: it.SupplyTotal, TaxTotal: it.TaxTotal, Total: it.SupplyTotal + it.TaxTotal, ChargeType: domain.TaxClaim, AmendReason: it.AmendReason,
				Remark: strings.TrimSpace(it.Remark), AcceptedAt: fmtRFC(s.now())}
			if it.ChargeType == domain.TaxReceipt {
				v.ChargeType = domain.TaxReceipt
			}
			if utf8Len(v.Remark) > 200 {
				v.Remark = string([]rune(v.Remark)[:200])
			}
			if it.AmendOfApprovalNo != "" {
				if oid, _, err := s.Repo.IDByApproval(ctx, tx, strings.ToUpper(strings.TrimSpace(it.AmendOfApprovalNo))); err != nil {
					return err
				} else {
					v.AmendOfID = oid
				}
			}
			for i, l := range it.Lines {
				v.Lines = append(v.Lines, domain.TaxLine{Seq: i + 1, Day: l.Day, Name: truncRunes(l.Name, 100), Spec: truncRunes(l.Spec, 50), Qty: l.Qty, UnitPrice: l.UnitPrice, Supply: l.Supply, Tax: l.Tax, Remark: truncRunes(l.Remark, 100)})
			}
			if _, err := s.saveNew(ctx, tx, owner, v, sup); err != nil {
				return err
			}
			c.Inserted++
		}
		return nil
	})
	return c, err
}

// syncOwner 는 동기화로 들어온 건의 작성자 칸(created_by, users 참조 필수)에 쓸 사용자다.
//   - 수동 실행이면 실행한 사람이다.
//   - 자동 실행이면 대행사 연동을 마지막으로 켠 사람이다: 감사 taxasp.activate 의 마지막 성공 행위자, 없으면 tax_asp_settings.updated_by.
//     그 사람이 없거나 비활성(퇴사 등)이면 domain.ErrTaxSyncOwner 로 동기화를 멈춘다(무관한 사람이 작성자로 남아 감사·SoD 판정을 왜곡하지 않게).
//
// 감사의 행위자는 그대로 시스템이다. 화면에는 "대행사 연동 담당자 대신 기록" 으로 보인다.
func (s *TaxInvoiceService) syncOwner(ctx context.Context, tx contract.Tx, actor contract.Principal) (contract.Principal, error) {
	if actor.UserID != 0 {
		return actor, nil
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT actor_id FROM audit_logs WHERE action = 'taxasp.activate' AND result = 'success' AND (entity_id = ? OR entity_id = 0) ORDER BY seq DESC LIMIT 1`, contract.WriteEntityID(ctx)).Scan(&id); err != nil || id == 0 {
		var by sql.NullInt64
		_ = tx.QueryRowContext(ctx, `SELECT updated_by FROM tax_asp_settings WHERE entity_id = ?`, contract.WriteEntityID(ctx)).Scan(&by)
		id = by.Int64
	}
	if id == 0 {
		return actor, domain.ErrTaxSyncOwner
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, id).Scan(&active); err != nil || active != 1 {
		return actor, domain.ErrTaxSyncOwner
	}
	actor.UserID = id
	return actor, nil
}

// CheckSyncOwner 는 자동 실행의 작성자(대행사 연동 담당자)가 유효한지 대행사를 부르기 전에 확인한다. 수동 실행이면 항상 통과한다.
func (s *TaxInvoiceService) CheckSyncOwner(ctx context.Context) error {
	_, err := s.syncOwner(ctx, s.DB, syncActor(ctx))
	return err
}

func utf8Len(s string) int { return len([]rune(s)) }

func truncRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n])
	}
	return string(r)
}

// FinishSync 는 한 번의 실행 결과를 남긴다: 진행 상태·결과 코드·건수, 감사 taxinvoice.sync(건수만), 새 건이 있으면 대조 제안 갱신.
func (s *TaxInvoiceService) FinishSync(ctx context.Context, st domain.TaxSyncState, counts domain.TaxSyncCounts, result string) error {
	actor := syncActor(ctx)
	return s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := s.saveProgress(ctx, tx, st); err != nil {
			return err
		}
		cj, _ := json.Marshal(counts)
		now := fmtRFC(s.now())
		if err := s.putSetting(ctx, tx, "last_counts", string(cj)); err != nil {
			return err
		}
		failedAt := ""
		if result == "failed" {
			failedAt = now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tax_sync_state SET last_run_at = ?, last_result = ?, last_error_at = CASE WHEN ? <> '' THEN ? ELSE last_error_at END WHERE entity_id = ?`, now, result, failedAt, failedAt, contract.WriteEntityID(ctx)); err != nil {
			return err
		}
		res := audit.ResultSuccess
		if result == "failed" || result == "no_company" || result == "not_configured" || result == "owner_inactive" {
			res = audit.ResultError
		}
		if err := s.Audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: "taxinvoice.sync", TargetType: audit.TargetTaxInvoice, TargetID: "sync", Result: res, Detail: domain.TaxSyncResultLabels[result],
			Diff: []audit.DiffItem{{Key: "inserted", New: strconv.Itoa(counts.Inserted)}, {Key: "same", New: strconv.Itoa(counts.Same)}, {Key: "conflict", New: strconv.Itoa(counts.Conflict)},
				{Key: "invalid", New: strconv.Itoa(counts.Invalid)}, {Key: "not_ours", New: strconv.Itoa(counts.NotOurs)}}}); err != nil {
			return err
		}
		if counts.Inserted > 0 {
			if _, err := s.proposeAll(ctx, tx); err != nil {
				return err
			}
		}
		// 요약 알림(tax_invoice_sync, 기본 이메일 꺼짐): 새로 가져왔거나 충돌·실패가 있을 때만, 본문에는 건수만 싣는다(AC-4317).
		if counts.Inserted > 0 || counts.Conflict > 0 || result == "failed" || result == "owner_inactive" {
			body := fmt.Sprintf("수신 세금계산서를 새로 %d건 가져왔습니다. 내용이 달라 확인이 필요한 건 %d건.", counts.Inserted, counts.Conflict)
			if result == "failed" {
				body = "수신 세금계산서를 가져오지 못했습니다. 다음 실행에서 다시 시도합니다."
			}
			if result == "owner_inactive" {
				body = "대행사 연동을 켠 담당자가 없어 수신 동기화를 멈췄습니다. 대행사 연동을 담당자가 다시 켜 주세요."
			}
			return s.NotifyPermissionHolders(ctx, tx, contract.PermTaxInvoiceWrite, NotifyTaxInvoiceSync, "수신 세금계산서 동기화", body, "/finance/tax-matches", "tax_invoice", 0, s.now())
		}
		return nil
	})
}

// proposeSystem 은 새 수신 건(수기 등록 등) 뒤에 대조 제안을 갱신한다. 실패는 로그만 남긴다(등록 자체를 되돌리지 않는다: 사용자가 대조 화면에서 다시 만들 수 있다).
func (s *TaxInvoiceService) proposeSystem(ctx context.Context) {
	if err := s.Txs.WithTx(ctx, func(tx contract.Tx) error { _, err := s.proposeAll(ctx, tx); return err }); err != nil {
		log.Printf("taxinvoice: 대조 제안 갱신 실패: %v", err)
	}
}

var _ = time.Second
