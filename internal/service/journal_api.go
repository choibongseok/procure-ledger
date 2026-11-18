package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 연동 API(B42 3단계, 명세 10.3)의 전표 초안 제출. 일반 Create 와 같은 normalize·월 마감 검사를 거치고 status='draft', kind='general', source_type='api' 로 저장한다.
// 작성자(created_by)는 토큰 발급자 번호다(D11): 이후 승인 때 B41 SoD(jv.create_approve)가 작성자 = 승인자를 판정한다. API 는 초안까지만 만들 수 있다(승인·요청·취소 경로 없음).

var _ domain.APIJournalDrafter = (*JournalService)(nil)

// ExternalRefMax 는 external_ref 의 최대 길이다.
const externalRefMax = 64

// APIRefHash 는 (토큰, external_ref)의 짧은 해시다(감사에는 원문 대신 이것만 남긴다).
func apiRefDigest(tokenID int64, ref string) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("api-journal|%d|%s", tokenID, ref)))
}

// apiSourceID 는 journals.source_id 에 넣는 양의 정수다(같은 토큰·같은 external_ref 는 같은 값).
func apiSourceID(tokenID int64, ref string) int64 {
	d := apiRefDigest(tokenID, ref)
	v := int64(binary.BigEndian.Uint64(d[:8]) >> 1)
	if v == 0 {
		v = 1
	}
	return v
}

// CreateDraftFromAPI 는 전표 초안을 만든다. 같은 토큰의 같은 external_ref 가 이미 있으면 내용이 같을 때 그 전표를 돌려주고(created=false) 다르면 거부한다(충돌).
func (s *JournalService) CreateDraftFromAPI(ctx context.Context, in domain.JournalInput, externalRef string) (domain.Journal, bool, error) {
	const action = "api.journal_draft"
	p, err := s.authorize(ctx, contract.PermAPIJournalDraft, action, audit.TargetJournal)
	if err != nil {
		return domain.Journal{}, false, err
	}
	ref := strings.TrimSpace(externalRef)
	if n := utf8.RuneCountInString(ref); n < 1 || n > externalRefMax {
		return domain.Journal{}, false, validation("external_ref 는 1~%d자여야 합니다.", externalRefMax)
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return domain.Journal{}, false, validation("external_ref 에 제어문자를 쓸 수 없습니다.")
		}
	}
	if in.Kind != "" && in.Kind != domain.JournalGeneral {
		return domain.Journal{}, false, validation("연동 API 로는 일반 전표 초안만 만들 수 있습니다.")
	}
	in.Kind, in.Evidence = domain.JournalGeneral, nil
	srcID := apiSourceID(p.TokenID, ref)
	var out domain.Journal
	created := false
	err = s.write(ctx, p, action, audit.TargetJournal, 0, func(tx contract.Tx) error {
		if err := dbutil.AcquireKeyLock(ctx, tx, fmt.Sprintf("api-journal:%d", srcID)); err != nil {
			return err
		}
		d, err := s.normalize(ctx, tx, in)
		if err != nil {
			return err
		}
		// 연동으로 들어오는 초안은 처음부터 차변 합계 = 대변 합계여야 한다(화면의 일반 임시 저장은 균형 없이도 저장되지만, 외부 시스템이 불균형 전표를 쌓아 두지 않게 한다, 명세 10.3).
		if d.debit != d.credit {
			return balanceError(d.debit, d.credit)
		}
		// 같은 external_ref: 내용이 같으면 멱등(기존 전표), 다르면 충돌.
		var existing string
		switch err := tx.QueryRowContext(ctx, `SELECT no FROM journals WHERE `+contract.SQLEntity(ctx, "journals")+` AND source_type = 'api' AND source_id = ? ORDER BY id LIMIT 1`, srcID).Scan(&existing); {
		case err == nil:
			j, jerr := s.Repo.Journal(ctx, tx, existing)
			if jerr != nil {
				return jerr
			}
			if !sameDraft(j, d) {
				return fmt.Errorf("%w: 같은 external_ref 로 다른 내용의 전표가 이미 있습니다", contract.ErrInvalidState)
			}
			out = j
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if err := s.assertMonthOpen(ctx, tx, d.posting); err != nil {
			return err
		}
		now := s.now()
		no, err := s.Repo.NextJournalNo(ctx, tx, now.In(contract.KST).Year())
		if err != nil {
			return err
		}
		id, err := s.Repo.InsertJournal(ctx, tx, repository.JournalInsert{No: no, Kind: d.kind, PostingDate: d.posting, TradeDate: d.trade, Memo: d.memo,
			SourceType: "api", SourceID: srcID, Total: d.debit, CreatedBy: p.UserID, Now: now})
		if err != nil {
			return err
		}
		if err := s.Repo.ReplaceLines(ctx, tx, id, d.lines); err != nil {
			return err
		}
		if out, err = s.Repo.Journal(ctx, tx, no); err != nil {
			return err
		}
		created = true
		h := apiRefDigest(p.TokenID, ref)
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetJournal, TargetID: no, Result: audit.ResultSuccess, Detail: "연동 API 전표 초안",
			Diff: []audit.DiffItem{{Key: "journal_no", New: no}, {Key: "external_ref_hash", New: hex.EncodeToString(h[:6])}, {Key: "debit_total", New: formatWon(d.debit)}}})
	})
	return out, created, err
}

// sameDraft 는 저장된 전표 초안이 새 입력(정규화된 값)과 같은 내용인지다(전기일·거래일·적요·줄 순서대로 계정·차대·금액·거래처·줄 적요).
func sameDraft(j domain.Journal, d journalDraft) bool {
	if j.PostingDate != d.posting || j.TradeDate != d.trade || j.Memo != d.memo || len(j.Lines) != len(d.lines) {
		return false
	}
	for i, l := range j.Lines {
		n := d.lines[i]
		if l.AccountID != n.AccountID || l.Side != n.Side || l.Amount != n.Amount || l.VendorID != n.VendorID || l.Memo != n.Memo {
			return false
		}
	}
	return true
}
