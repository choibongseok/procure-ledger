package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/cardimport"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 법인카드(B32 ③, 3.3·4절).
//   - 카드번호는 끝 4자리만 다룬다. CSV 는 코드가 읽는다(internal/cardimport, LLM 없음). 계정과목은 사람이 소명에서 고른다.
//   - 카드 내역 상태 전이는 cardTransitions 표 한 곳이다(표에 없는 전이는 전부 거부).
//   - 본인 범위: 소지자는 본인 내역만, 조직장은 조직 범위(B29 대결 포함)의 소명됨 건만 확인한다. 타인 내역은 404 다.
//   - 자동 개인 사용 처리·자동 전표는 없다(돈이 걸린 판정은 사람이 한다).

type cardEvent string

const (
	cardEvSubmit          cardEvent = "submit"
	cardEvConfirm         cardEvent = "confirm"
	cardEvConfirmPersonal cardEvent = "confirm_personal"
	cardEvReturn          cardEvent = "return"
	cardEvWithdraw        cardEvent = "withdraw"
	cardEvReverse         cardEvent = "reverse" // 비용 전표 역분개 승인
	cardEvRefundRequest   cardEvent = "refund_request"
	cardEvRefundDone      cardEvent = "refund_done"
)

type cardKey struct {
	from string
	ev   cardEvent
}

// cardTransitions 는 카드 내역 상태 전이 표다(명세 4절).
var cardTransitions = map[cardKey]string{
	{domain.CardTxnUnreconciled, cardEvSubmit}:        domain.CardTxnSubmitted,
	{domain.CardTxnNeedsFix, cardEvSubmit}:            domain.CardTxnSubmitted,
	{domain.CardTxnSubmitted, cardEvConfirm}:          domain.CardTxnConfirmed,
	{domain.CardTxnSubmitted, cardEvConfirmPersonal}:  domain.CardTxnRefundRequested,
	{domain.CardTxnSubmitted, cardEvReturn}:           domain.CardTxnNeedsFix,
	{domain.CardTxnSubmitted, cardEvWithdraw}:         domain.CardTxnUnreconciled,
	{domain.CardTxnConfirmed, cardEvReverse}:          domain.CardTxnNeedsFix,
	{domain.CardTxnUnreconciled, cardEvRefundRequest}: domain.CardTxnRefundRequested,
	{domain.CardTxnNeedsFix, cardEvRefundRequest}:     domain.CardTxnRefundRequested,
	{domain.CardTxnRefundRequested, cardEvRefundDone}: domain.CardTxnRefunded,
}

func cardTransition(from string, ev cardEvent) (string, error) {
	to, ok := cardTransitions[cardKey{from, ev}]
	if !ok {
		return "", fmt.Errorf("%w: %s 상태의 카드 내역은 이 작업을 할 수 없습니다", contract.ErrInvalidState, domain.CardTxnStatusLabels[from])
	}
	return to, nil
}

const (
	maxCardRows        = 10000
	maxConfirmBatch    = 50
	delayedAfterDays   = 30
	defaultExplainDays = 7
)

// CardService 는 domain.CardService 의 구현이다.
type CardService struct {
	Base
	Repo        repository.SQLCards
	Ledger      repository.SQLLedger
	Parser      domain.CardParser
	Delegations repository.SQLDelegations
}

var _ domain.CardService = (*CardService)(nil)
var _ domain.CardReader = (*CardService)(nil)

func NewCardService(b Base) *CardService {
	return &CardService{Base: b, Parser: cardimport.Parser{}}
}

// ---- 본인 범위·조직 범위 ----

func dayDiff(a, b string) int { // a − b 일수
	ta, _ := time.Parse("2006-01-02", a)
	tb, _ := time.Parse("2006-01-02", b)
	return int(ta.Sub(tb).Hours() / 24)
}

// holderUnits 는 소지자의 소속 조직(주소속·겸직) 번호다. 조직에 등록되지 않았으면 비어 있다.
func (b Base) holderUnits(ctx context.Context, holderID int64) []int64 {
	if b.Org == nil || holderID == 0 {
		return nil
	}
	per, err := b.Org.Person(ctx, holderID, contract.DayOf(b.now()))
	if err != nil {
		return nil
	}
	out := []int64{per.Primary.Unit.ID}
	for _, c := range per.Concurrent {
		out = append(out, c.Unit.ID)
	}
	return out
}

// confirmScope 는 p 가 소명을 확인할 수 있는 조직 범위다: 본인이 조직장인 조직과 하위, 그리고 지금 유효한 대결(B29)·위임으로 맡은 위임자의 범위.
func (b Base) confirmScope(ctx context.Context, p contract.Principal) (map[int64]bool, error) {
	out := map[int64]bool{}
	if b.Org == nil {
		return out, nil
	}
	day := contract.DayOf(b.now())
	add := func(uid int64) error {
		ids, err := b.Org.HeadedScope(ctx, uid, day)
		if err != nil && !errors.Is(err, contract.ErrNotInOrg) && !errors.Is(err, contract.ErrNotFound) {
			return err
		}
		for _, id := range ids {
			out[id] = true
		}
		return nil
	}
	if err := add(p.UserID); err != nil {
		return nil, err
	}
	dels, err := repository.SQLDelegations{}.ActiveTo(ctx, b.DB, p.UserID, b.now())
	if err != nil {
		return nil, err
	}
	for _, d := range dels {
		if err := add(d.DelegatorID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// canConfirm 은 p 가 그 소지자의 소명을 확인할 수 있는지다(본인 소명은 확인할 수 없다: 조직장 본인의 소명은 위 조직장이 확인한다).
func (b Base) canConfirm(ctx context.Context, p contract.Principal, holderID int64, scope map[int64]bool) bool {
	if holderID == 0 || holderID == p.UserID {
		return false
	}
	return b.holderInScope(ctx, holderID, scope)
}

// canConfirmScope 는 canConfirm 에서 "본인 소지" 제외만 뺀 조직 범위 판정이다. 소지자 본인의 확인 시도는 범위 안이면 여기를 지나
// 확인 트랜잭션 안의 권한 분리 판정(sod.RuleCardHolderConfirm)이 규칙 모드대로 막거나 경고한다(직접 비교로 숨기지 않고 시도를 남긴다).
func (b Base) canConfirmScope(ctx context.Context, holderID int64, scope map[int64]bool) bool {
	return holderID != 0 && b.holderInScope(ctx, holderID, scope)
}

func (b Base) holderInScope(ctx context.Context, holderID int64, scope map[int64]bool) bool {
	for _, u := range b.holderUnits(ctx, holderID) {
		if scope[u] {
			return true
		}
	}
	return false
}

// cardTxnVisible 은 p 가 카드 내역(과 그 증빙)을 볼 수 있는지다: 소지자 본인, card:read_all, 확인할 조직장.
func (b Base) cardTxnVisible(ctx context.Context, p contract.Principal, txnID int64) bool {
	r, err := repository.SQLCards{}.Txn(ctx, b.DB, txnID)
	if err != nil {
		return false
	}
	if r.HolderID == p.UserID || p.Can(contract.PermCardReadAll) {
		return true
	}
	if p.Can(contract.PermCardConfirm) {
		scope, err := b.confirmScope(ctx, p)
		return err == nil && b.canConfirm(ctx, p, r.HolderID, scope)
	}
	return false
}

// ---- 카드 ----

func (s *CardService) Cards(ctx context.Context) ([]domain.Card, error) {
	p, err := contract.Authorize(ctx, contract.PermCardReadAll)
	if err != nil {
		return nil, err
	}
	_ = p
	out, err := s.Repo.Cards(ctx, s.DB)
	if out == nil {
		out = []domain.Card{}
	}
	return out, err
}

func (s *CardService) checkHolder(ctx context.Context, q contract.Tx, holderID int64) error {
	if holderID == 0 {
		return nil
	}
	_, active, found, err := s.Repo.UserInfo(ctx, q, holderID)
	if err != nil {
		return err
	}
	if !found || !active {
		return validation("소지자를 재직 중인 사용자 중에서 고르세요.")
	}
	return nil
}

// SaveCard 는 카드를 등록(id 0)하거나 별칭을 고친다. 카드사 형식·끝 4자리는 등록 때만 정한다.
func (s *CardService) SaveCard(ctx context.Context, id int64, in domain.CardInput) (domain.Card, error) {
	action := "card.create"
	if id != 0 {
		action = "card.update"
	}
	p, err := s.authorize(ctx, contract.PermCardManage, action, audit.TargetCard)
	if err != nil {
		return domain.Card{}, err
	}
	in.Alias = strings.TrimSpace(in.Alias)
	if c := utf8.RuneCountInString(in.Alias); c < 1 || c > 40 {
		return domain.Card{}, validation("카드 별칭은 1~40자로 입력하세요.")
	}
	var out domain.Card
	err = s.write(ctx, p, action, audit.TargetCard, id, func(tx contract.Tx) error {
		if id == 0 {
			if _, ok := domain.CardFormatLabels[in.Issuer]; !ok {
				return validation("카드사 형식을 고르세요.")
			}
			if len(in.Last4) != 4 || strings.Trim(in.Last4, "0123456789") != "" {
				return validation("카드번호는 끝 4자리 숫자만 입력하세요. 전체 번호는 받지 않습니다.")
			}
			if err := s.checkHolder(ctx, tx, in.HolderID); err != nil {
				return err
			}
			newID, err := s.Repo.InsertCard(ctx, tx, in, s.now())
			if err != nil {
				if dbutil.IsUnique(err) {
					return validation("같은 카드사 형식에 끝 4자리가 같은 카드가 이미 있습니다.")
				}
				return err
			}
			id = newID
			if in.HolderID != 0 {
				if err := s.Repo.AddHolderHistory(ctx, tx, id, in.HolderID, p.UserID, s.kstDay(), s.now()); err != nil {
					return err
				}
			}
		} else if err := s.Repo.UpdateCardAlias(ctx, tx, id, in.Alias); err != nil {
			return err
		}
		if out, err = s.Repo.Card(ctx, tx, id); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetCard, TargetID: out.Alias, Result: audit.ResultSuccess, Detail: "법인카드 저장",
			Diff: []audit.DiffItem{{Key: "alias", New: out.Alias}, {Key: "holder_to", New: out.HolderName}}})
	})
	return out, err
}

// ChangeHolder 는 소지자를 바꾼다. 이후 업로드되는 내역부터 새 소지자이고 기존 내역은 그 시점 소지자를 유지한다.
func (s *CardService) ChangeHolder(ctx context.Context, id, holderID int64) (domain.Card, error) {
	p, err := s.authorize(ctx, contract.PermCardHolderManage, "card.holder_change", audit.TargetCard)
	if err != nil {
		return domain.Card{}, err
	}
	var out domain.Card
	err = s.write(ctx, p, "card.holder_change", audit.TargetCard, id, func(tx contract.Tx) error {
		cur, err := s.Repo.Card(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.checkHolder(ctx, tx, holderID); err != nil {
			return err
		}
		if err := s.Repo.SetCardHolder(ctx, tx, id, holderID); err != nil {
			return err
		}
		if err := s.Repo.AddHolderHistory(ctx, tx, id, holderID, p.UserID, s.kstDay(), s.now()); err != nil {
			return err
		}
		if out, err = s.Repo.Card(ctx, tx, id); err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.holder_change", TargetType: audit.TargetCard, TargetID: out.Alias, Result: audit.ResultSuccess, Detail: "카드 소지자 변경",
			Diff: []audit.DiffItem{{Key: "holder_from", New: cur.HolderName}, {Key: "holder_to", New: out.HolderName}}})
	})
	return out, err
}

// HolderHistory 는 카드의 소지자 변경 이력이다(card:read_all).
func (s *CardService) HolderHistory(ctx context.Context, id int64) ([]domain.CardHolderChange, error) {
	if _, err := contract.Authorize(ctx, contract.PermCardReadAll); err != nil {
		return nil, err
	}
	if _, err := s.Repo.Card(ctx, s.DB, id); err != nil {
		return nil, err
	}
	out, err := s.Repo.HolderHistory(ctx, s.DB, id)
	if out == nil {
		out = []domain.CardHolderChange{}
	}
	return out, err
}

func (s *CardService) SetCardActive(ctx context.Context, id int64, active bool) (domain.Card, error) {
	p, err := s.authorize(ctx, contract.PermCardManage, "card.update", audit.TargetCard)
	if err != nil {
		return domain.Card{}, err
	}
	var out domain.Card
	err = s.write(ctx, p, "card.update", audit.TargetCard, id, func(tx contract.Tx) error {
		if _, err := s.Repo.Card(ctx, tx, id); err != nil {
			return err
		}
		if err := s.Repo.SetCardActive(ctx, tx, id, active); err != nil {
			return err
		}
		out, err = s.Repo.Card(ctx, tx, id)
		if err != nil {
			return err
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.update", TargetType: audit.TargetCard, TargetID: out.Alias, Result: audit.ResultSuccess, Detail: "법인카드 사용 여부 변경",
			Diff: []audit.DiffItem{{Key: "active", New: fmt.Sprint(active)}}})
	})
	return out, err
}

// ---- 업로드 ----

type classified struct {
	preview domain.CardImportPreview
	res     domain.CardParseResult
	rows    []classifiedRow
	hash    string
}

type classifiedRow struct {
	row  domain.CardRow
	card domain.Card
	key  string
}

// classify 는 CSV 를 정규화하고 카드 매칭·중복·오류를 센다(저장하지 않는다).
func (s *CardService) classify(ctx context.Context, q contract.Tx, format string, data []byte) (classified, error) {
	var c classified
	if len(data) == 0 {
		return c, validation("파일이 비어 있습니다.")
	}
	res, err := s.Parser.Parse(data, format)
	if err != nil {
		return c, err
	}
	if res.TooManyError {
		return c, validation("오류 행이 20%%를 넘어 카드사 형식이 맞지 않는 것으로 보입니다. 형식을 다시 고르세요.")
	}
	if res.TotalRows > maxCardRows {
		return c, validation("한 번에 %d행까지 올릴 수 있습니다. 나누어 올리세요.", maxCardRows)
	}
	sum := sha256.Sum256(data)
	c.hash = hex.EncodeToString(sum[:])
	c.res = res
	c.preview = domain.CardImportPreview{Format: res.Format, FormatLabel: domain.CardFormatLabels[res.Format], Total: res.TotalRows, ErrorRows: append([]domain.CardRowError(nil), res.Errors...)}
	var keys []string
	cards := map[string]domain.Card{}
	unreg := map[string]int{}
	for _, r := range res.Rows {
		card, ok := cards[r.Last4]
		if !ok {
			cd, found, err := s.Repo.CardByLast4(ctx, q, res.Format, r.Last4)
			if err != nil {
				return c, err
			}
			if !found || !cd.Active {
				unreg[r.Last4]++
				c.preview.ErrorRows = append(c.preview.ErrorRows, domain.CardRowError{RowNo: r.RowNo, Reason: fmt.Sprintf("등록되지 않았거나 사용 중지된 카드(…%s)입니다.", r.Last4)})
				continue
			}
			cards[r.Last4] = cd
			card = cd
		}
		k := domain.CardDedupKey(card.ID, r)
		keys = append(keys, k)
		c.rows = append(c.rows, classifiedRow{row: r, card: card, key: k})
	}
	for last4, n := range unreg {
		c.preview.Unregistered = append(c.preview.Unregistered, fmt.Sprintf("…%s %d건", last4, n))
	}
	existing, err := s.Repo.ExistingKeys(ctx, q, keys)
	if err != nil {
		return c, err
	}
	seen := map[string]bool{}
	byApproval := map[string]int64{}
	kept := c.rows[:0:0]
	for _, r := range c.rows {
		if existing[r.key] || seen[r.key] {
			c.preview.Duplicate++
			continue
		}
		seen[r.key] = true
		if r.row.ApprovalNo != "" {
			ak := fmt.Sprintf("%d|%s|%s", r.card.ID, r.row.ApprovalNo, r.row.Kind)
			if prev, ok := byApproval[ak]; ok && prev != r.row.Amount {
				c.preview.Warnings = append(c.preview.Warnings, "금액이 다른 같은 승인번호가 있습니다. 정정 재전송인지 확인하세요.")
			}
			byApproval[ak] = r.row.Amount
		}
		c.preview.New++
		c.preview.TotalAmount += r.row.Amount
		kept = append(kept, r)
	}
	c.rows = kept
	c.preview.Errors = len(c.preview.ErrorRows)
	c.preview.Warnings = uniqueStrings(c.preview.Warnings)
	return c, nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// PreviewImport 는 업로드 미리보기다(저장하지 않는다): 신규·중복·오류 건수와 합계, 등록이 필요한 카드.
func (s *CardService) PreviewImport(ctx context.Context, format string, data []byte) (domain.CardImportPreview, error) {
	if _, err := contract.Authorize(ctx, contract.PermCardImport); err != nil {
		return domain.CardImportPreview{}, err
	}
	c, err := s.classify(ctx, s.DB, format, data)
	return c.preview, err
}

// ConfirmImport 는 같은 파일을 다시 읽어 저장한다. 같은 행은 한 번만 들어가고(UNIQUE), 취소 행은 같은 카드·승인번호의 승인 행과 짝을 맺어 둘 다 소명 불필요가 된다.
func (s *CardService) ConfirmImport(ctx context.Context, format string, data []byte) (domain.CardImportResult, error) {
	p, err := s.authorize(ctx, contract.PermCardImport, "card.import", audit.TargetCard)
	if err != nil {
		return domain.CardImportResult{}, err
	}
	var out domain.CardImportResult
	err = s.write(ctx, p, "card.import", audit.TargetCard, 0, func(tx contract.Tx) error {
		c, err := s.classify(ctx, tx, format, data)
		if err != nil {
			return err
		}
		now := s.now()
		importID, err := s.Repo.InsertImport(ctx, tx, c.res.Format, c.hash, c.preview.Total, c.preview.New, c.preview.Duplicate, c.preview.Errors, p.UserID, now)
		if err != nil {
			return err
		}
		explainDays := s.Repo.SettingInt(ctx, tx, "card.explain_days", defaultExplainDays)
		today := s.kstDay()
		for _, r := range c.rows {
			used := r.row.UsedAt.In(contract.KST).Format("2006-01-02")
			ut, _ := time.Parse("2006-01-02", used)
			dueDay := ut.AddDate(0, 0, explainDays).Format("2006-01-02")
			if dueDay < today { // 늦게 올린 내역은 업로드일부터 센다(소지자가 책임지지 않게)
				td, _ := time.Parse("2006-01-02", today)
				dueDay = td.AddDate(0, 0, explainDays).Format("2006-01-02")
			}
			if dueDay, err = s.nextBusinessDay(ctx, tx, dueDay); err != nil { // 기한 마지막 날이 휴일(주말·공휴일)이면 다음 영업일로 연장한다(B34)
				return err
			}
			status := domain.CardTxnUnreconciled
			var pairID int64
			var pairStatus string
			if r.row.Kind == domain.CardKindCancel {
				pairID, pairStatus, err = s.Repo.PairCandidate(ctx, tx, r.card.ID, r.row.ApprovalNo, r.row.Amount, 0)
				if err != nil {
					return err
				}
				status = domain.CardTxnOrphanCancel
				if pairID != 0 && (pairStatus == domain.CardTxnUnreconciled || pairStatus == domain.CardTxnNeedsFix) {
					status = domain.CardTxnNotRequired
				}
			}
			id, err := s.Repo.InsertTxn(ctx, tx, repository.TxnInsert{CardID: r.card.ID, HolderID: r.card.HolderID, ImportID: importID, UsedAt: r.row.UsedAt, Merchant: r.row.Merchant, Amount: r.row.Amount,
				Supply: r.row.Supply, VAT: r.row.VAT, Tip: r.row.Tip, ApprovalNo: r.row.ApprovalNo, Kind: r.row.Kind, DedupKey: r.key, DueDate: dueDay, Status: status, Now: now})
			if err != nil {
				return err
			}
			if r.row.Kind == domain.CardKindCancel && pairID != 0 {
				if err := s.Repo.SetPair(ctx, tx, id, pairID); err != nil {
					return err
				}
				if err := s.Repo.SetPair(ctx, tx, pairID, id); err != nil {
					return err
				}
				if status == domain.CardTxnNotRequired {
					if err := s.Repo.SetTxnStatus(ctx, tx, pairID, domain.CardTxnNotRequired); err != nil {
						return err
					}
				}
			}
			if r.row.Kind == domain.CardKindApprove {
				// 승인 행이 취소 행보다 늦게 들어온 경우의 짝 맺기
				cid, err := s.Repo.CancelCandidate(ctx, tx, r.card.ID, r.row.ApprovalNo, r.row.Amount, id)
				if err != nil {
					return err
				}
				if cid != 0 {
					for _, pair := range [][2]int64{{id, cid}, {cid, id}} {
						if err := s.Repo.SetPair(ctx, tx, pair[0], pair[1]); err != nil {
							return err
						}
					}
					for _, sid := range []int64{id, cid} {
						if err := s.Repo.SetTxnStatus(ctx, tx, sid, domain.CardTxnNotRequired); err != nil {
							return err
						}
					}
				}
			}
		}
		out = domain.CardImportResult{ImportID: importID, Total: c.preview.Total, New: c.preview.New, Duplicate: c.preview.Duplicate, Errors: c.preview.Errors}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "card.import", TargetType: audit.TargetCard, TargetID: fmt.Sprintf("업로드 %d", importID), Result: audit.ResultSuccess, Detail: "카드 내역 업로드",
			Diff: []audit.DiffItem{{Key: "format", New: domain.CardFormatLabels[c.res.Format]}, {Key: "file_hash", New: c.hash[:16]}, {Key: "rows_total", New: fmt.Sprint(c.preview.Total)},
				{Key: "rows_new", New: fmt.Sprint(c.preview.New)}, {Key: "rows_dup", New: fmt.Sprint(c.preview.Duplicate)}, {Key: "rows_error", New: fmt.Sprint(c.preview.Errors)}}})
	})
	return out, err
}
