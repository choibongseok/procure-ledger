// Package importflow 는 기초 데이터 가져오기(B35,)의 공통 흐름이다().
// 양식 내려받기 → 미리보기(저장·변경 없음) → 확인 후 반영 → 이력. 대상별 도메인 반영(품목·거래처·기초 재고 등)은 erp 가
// domain.ImportTargetHandler 로 붙인다. 이 패키지는 어떤 도메인 표도 직접 쓰지 않는다.
//
// 불변식
//   - 파일은 보관하지 않는다(D8). 반영은 같은 파일을 반영 토큰과 함께 다시 받아 읽고, 미리보기와 판정이 같을 때만 한다.
//   - 반영은 한 트랜잭션이다: 대상 반영·묶음 상태 전이·감사 한 건이 함께 성공하거나 함께 취소된다.
//   - 반영 토큰은 일회용이고 해시만 저장한다. 묶음 상태는 DB 트리거도 끝 상태를 고정한다.
package importflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/importx"
)

// DefaultPreviewTTL 은 app_settings 의 import.preview_ttl_hours 가 없거나 잘못됐을 때 쓰는 기본값이다(명세 6절, 24시간).
const DefaultPreviewTTL = 24 * time.Hour

const tsLayout = "2006-01-02T15:04:05Z"

// Options 는 서비스 조립 인자다. Parser·Templates 가 비면 internal/importx 의 구현을 쓴다.
type Options struct {
	DB         contract.Tx
	Txs        contract.TxManager
	Audit      audit.Recorder
	Parser     domain.ImportFileParser
	Templates  domain.ImportTemplates
	Now        func() time.Time
	PreviewTTL time.Duration
}

// Service 는 domain.ImportService 구현이다.
type Service struct {
	o        Options
	mu       sync.Mutex
	handlers map[string]domain.ImportTargetHandler
	busy     map[string]bool
}

var _ domain.ImportService = (*Service)(nil)

// New 는 서비스를 만든다.
func New(o Options) *Service {
	if o.Parser == nil {
		o.Parser = importx.Parser{}
	}
	if o.Templates == nil {
		o.Templates = importx.Templates{}
	}
	return &Service{o: o, handlers: map[string]domain.ImportTargetHandler{}, busy: map[string]bool{}}
}

func (s *Service) now() time.Time {
	if s.o.Now != nil {
		return s.o.Now().UTC()
	}
	return time.Now().UTC()
}

// keyRules 는 같은 행인지 판정하는 기준 문장이다(명세 3.3·3.5.3). 미리보기 위에 그대로 보인다.
var keyRules = map[string]string{
	domain.ImportAccount:        "계정과목은 코드가 같으면 같은 계정으로 봅니다.",
	domain.ImportWarehouse:      "창고는 코드가 같으면(대소문자 구분 없이) 같은 창고로 봅니다.",
	domain.ImportVendor:         "거래처는 사업자번호가 같으면 같은 거래처로 봅니다. 사업자번호가 없으면 이름이 정확히 같을 때만 같은 거래처로 봅니다.",
	domain.ImportItem:           "품목은 SKU 가 같으면(대소문자 구분 없이) 같은 품목으로 봅니다.",
	domain.ImportBudget:         "부서 예산은 부서와 적용 월이 같으면 같은 예산으로 봅니다.",
	domain.ImportOpeningStock:   "기초 재고는 품목 SKU·창고 코드·로트 번호가 같으면 같은 재고로 봅니다.",
	domain.ImportOpeningPayable: "미지급 잔액은 거래처와 문서 번호가 같으면 같은 항목으로 봅니다.",
}

// moneyTargets 는 금액이 이력에 남는 대상이다. 금액은 원장 조회·재고 평가 조회 권한이 없으면 가린다(명세 5절).
var moneyTargets = map[string]bool{domain.ImportBudget: true, domain.ImportOpeningStock: true, domain.ImportOpeningPayable: true}

// RegisterTarget 은 대상별 반영을 붙인다. 같은 대상에 다시 붙이면 바꾼다(시험에서 가짜로 교체할 수 있게).
func (s *Service) RegisterTarget(h domain.ImportTargetHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[h.Target()] = h
}

func (s *Service) handler(target string) (domain.ImportTargetHandler, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handlers[target]
	if !ok {
		return nil, fmt.Errorf("%w: 이 대상(%s)의 가져오기 기능은 아직 연결되지 않았습니다.", contract.ErrInvalidState, domain.ImportTargetLabels[target])
	}
	return h, nil
}

// lock 은 같은 대상의 가져오기를 직렬화한다(명세 3.10, E12). 이미 진행 중이면 기다리지 않고 거부한다.
func (s *Service) lock(target string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[target] {
		return nil, fmt.Errorf("%w: 다른 가져오기가 진행 중입니다. 잠시 뒤 다시 시도하세요.", contract.ErrInvalidState)
	}
	s.busy[target] = true
	return func() { s.mu.Lock(); delete(s.busy, target); s.mu.Unlock() }, nil
}

func validTarget(target string) error {
	if _, ok := domain.ImportTargetLabels[target]; !ok {
		return fmt.Errorf("%w: 알 수 없는 가져오기 대상입니다.", contract.ErrValidation)
	}
	return nil
}

// authorizeTarget 은 대상별 권한을 확인한다(마스터 import:master, 금액성 import:balance).
func (s *Service) authorizeTarget(ctx context.Context, target string) (contract.Principal, error) {
	if err := validTarget(target); err != nil {
		return contract.Principal{}, err
	}
	return contract.Authorize(ctx, domain.ImportPerm(target))
}

func canSeeAmounts(p contract.Principal) bool {
	return p.Can(contract.PermLedgerRead) && p.Can(contract.PermInventoryValuation)
}

// Targets 는 화면의 대상 카드다. 가져오기 권한(마스터·금액성·조회) 중 하나라도 있어야 본다.
func (s *Service) Targets(ctx context.Context) ([]domain.ImportTargetStatus, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return nil, contract.ErrUnauthenticated
	}
	if !p.Can(contract.PermImportMaster) && !p.Can(contract.PermImportBalance) && !p.Can(contract.PermImportRead) {
		return nil, contract.ErrForbidden
	}
	out := make([]domain.ImportTargetStatus, 0, len(domain.ImportTargets))
	for _, t := range domain.ImportTargets {
		st := domain.ImportTargetStatus{Target: t, Name: domain.ImportTargetLabels[t], NeedsDate: domain.ImportNeedsBaseDate(t), KeyRule: keyRules[t], Columns: columnGuide(t)}
		st.Allowed = p.Can(domain.ImportPerm(t))
		s.mu.Lock()
		_, st.Ready = s.handlers[t]
		s.mu.Unlock()
		switch {
		case !st.Allowed:
			st.Reason = "이 대상을 가져올 권한이 없습니다."
		case !st.Ready:
			st.Reason = "이 대상의 반영 기능이 아직 연결되지 않았습니다."
		}
		var no, counts, appliedAt string
		err := s.o.DB.QueryRowContext(ctx, `SELECT no, counts_json, applied_at FROM import_batches WHERE `+contract.SQLEntity(ctx, "import_batches")+` AND target = ? AND status = 'applied' ORDER BY id DESC LIMIT 1`, t).Scan(&no, &counts, &appliedAt)
		if err == nil {
			var c domain.ImportCounts
			_ = json.Unmarshal([]byte(counts), &c)
			st.LastBatchNo, st.LastAdded, st.LastChanged, st.LastAt = no, c.Add, c.Chg, appliedAt
		}
		out = append(out, st)
	}
	return out, nil
}

func columnGuide(target string) []domain.ImportColumnGuide {
	t, ok := importx.Targets[target]
	if !ok {
		return nil
	}
	out := make([]domain.ImportColumnGuide, 0, len(t.Columns))
	for _, c := range t.Columns {
		out = append(out, domain.ImportColumnGuide{Label: c.Label, Required: c.Required, Help: c.Help, Example: c.Example})
	}
	return out
}

// Template 은 대상의 양식 파일이다. 양식 내려받기는 감사를 남기지 않는다(조회와 같은 원칙).
func (s *Service) Template(ctx context.Context, target string) (domain.ImportFile, error) {
	if _, err := s.authorizeTarget(ctx, target); err != nil {
		return domain.ImportFile{}, err
	}
	data, err := s.o.Templates.Template(target)
	if err != nil {
		return domain.ImportFile{}, err
	}
	return domain.ImportFile{Name: export.FileName(domain.ImportTargetLabels[target]+" 가져오기 양식", s.now(), "xlsx"), Data: data}, nil
}

func newToken() (token, hash string, err error) {
	var b [32]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, tokenHash(token), nil
}

func tokenHash(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fingerprint 는 판정 전체(행 번호·키·판정·사유·경고·바뀐 칸)의 지문이다. 미리보기와 반영 때 같아야 한다(명세 3.5.5, E9).
func fingerprint(target, baseDate string, vs []domain.ImportRowVerdict) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%d\n", target, baseDate, len(vs))
	enc := json.NewEncoder(h)
	for _, v := range vs {
		_ = enc.Encode(v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// analysis 는 파일 하나를 읽고 판정한 결과다.
type analysis struct {
	parsed   domain.ImportParseResult
	rows     []domain.ImportRow // 오류가 아닌 정규화 행
	verdicts []domain.ImportRowVerdict
	counts   domain.ImportCounts
	summary  []string
	finger   string
}

// analyze 는 파일을 읽어(파서) 대상 어댑터로 판정한다. q 는 읽기 전용 질의 핸들(미리보기는 DB, 반영은 트랜잭션)이다.
func (s *Service) analyze(ctx context.Context, q contract.Tx, h domain.ImportTargetHandler, target, baseDate string, data []byte, batchNo string) (analysis, error) {
	var a analysis
	res, err := s.o.Parser.Parse(target, data, domain.ImportParseOptions{BaseDate: baseDate})
	if err != nil {
		return a, err
	}
	a.parsed, a.rows = res, res.Rows
	plan := domain.ImportPlan{}
	if len(res.Rows) > 0 {
		if plan, err = h.Plan(ctx, q, res.Rows, domain.ImportPlanInput{Target: target, BaseDate: baseDate, BatchNo: batchNo}); err != nil {
			return a, err
		}
		if len(plan.Verdicts) != len(res.Rows) {
			return a, fmt.Errorf("importflow: 대상 어댑터(%s)가 행 %d개에 판정 %d개를 돌려줬습니다", target, len(res.Rows), len(plan.Verdicts))
		}
	}
	a.summary = plan.Summary
	warns := map[int][]string{}
	for _, w := range res.Warnings {
		msg := w.Reason
		if w.Column != "" {
			msg = w.Column + ": " + w.Reason
		}
		warns[w.RowNo] = append(warns[w.RowNo], msg)
	}
	for _, e := range res.Errors {
		a.verdicts = append(a.verdicts, domain.ImportRowVerdict{RowNo: e.RowNo, Verdict: domain.ImportVerdictError, Reason: e.Reason, Warn: strings.Join(warns[e.RowNo], "; ")})
	}
	for i, v := range plan.Verdicts {
		row := res.Rows[i]
		switch v.Verdict {
		case domain.ImportVerdictAdd, domain.ImportVerdictChange, domain.ImportVerdictSame, domain.ImportVerdictError:
		default:
			return a, fmt.Errorf("importflow: 대상 어댑터(%s)가 알 수 없는 판정 %q 을 돌려줬습니다", target, v.Verdict)
		}
		v.RowNo = row.RowNo
		if v.Key == "" {
			v.Key = row.Key
		}
		if extra := strings.Join(warns[row.RowNo], "; "); extra != "" {
			if v.Warn != "" {
				v.Warn += "; "
			}
			v.Warn += extra
		}
		a.verdicts = append(a.verdicts, v)
	}
	sort.SliceStable(a.verdicts, func(i, j int) bool { return a.verdicts[i].RowNo < a.verdicts[j].RowNo })
	a.counts.Total = len(a.verdicts)
	for _, v := range a.verdicts {
		switch v.Verdict {
		case domain.ImportVerdictAdd:
			a.counts.Add++
		case domain.ImportVerdictChange:
			a.counts.Chg++
		case domain.ImportVerdictSame:
			a.counts.Same++
		case domain.ImportVerdictError:
			a.counts.Err++
		}
		if v.Warn != "" {
			a.counts.Warn++
		}
	}
	a.finger = fingerprint(target, baseDate, a.verdicts)
	return a, nil
}

// rank 는 행 표의 정렬 순서다: 오류 → 경고 → 변경 → 추가 → 동일, 같으면 행 번호.
func rank(v domain.ImportRowVerdict) int {
	switch {
	case v.Verdict == domain.ImportVerdictError:
		return 0
	case v.Warn != "":
		return 1
	case v.Verdict == domain.ImportVerdictChange:
		return 2
	case v.Verdict == domain.ImportVerdictAdd:
		return 3
	}
	return 4
}

func sortForDisplay(vs []domain.ImportRowVerdict) []domain.ImportRowVerdict {
	out := append([]domain.ImportRowVerdict(nil), vs...)
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].RowNo < out[j].RowNo
	})
	return out
}

func validBaseDate(target, baseDate string, today string) (string, error) {
	if !domain.ImportNeedsBaseDate(target) {
		return "", nil
	}
	if baseDate == "" {
		return "", fmt.Errorf("%w: 기준일을 입력하세요.", contract.ErrValidation)
	}
	if _, err := time.Parse("2006-01-02", baseDate); err != nil {
		return "", fmt.Errorf("%w: 기준일은 YYYY-MM-DD 형식으로 입력하세요.", contract.ErrValidation)
	}
	if baseDate > today {
		return "", fmt.Errorf("%w: 기준일은 오늘 이전이어야 합니다.", contract.ErrValidation)
	}
	return baseDate, nil
}

func (s *Service) today() string { return s.now().In(export.KST).Format("2006-01-02") }

// Preview 는 파일을 읽어 판정한다. 어떤 업무 데이터도 바꾸지 않고 감사도 남기지 않는다(명세 3.5.2).
// 반영할 수 있는 결과(오류 0건·변경 있음)에는 일회용 반영 토큰을 준다. 변경이 전혀 없으면 묶음을 만들지 않는다.
func (s *Service) Preview(ctx context.Context, target, baseDate string, data []byte) (domain.ImportPreview, error) {
	p, err := s.authorizeTarget(ctx, target)
	if err != nil {
		return domain.ImportPreview{}, err
	}
	h, err := s.handler(target)
	if err != nil {
		return domain.ImportPreview{}, err
	}
	if baseDate, err = validBaseDate(target, baseDate, s.today()); err != nil {
		return domain.ImportPreview{}, err
	}
	unlock, err := s.lock(target)
	if err != nil {
		return domain.ImportPreview{}, err
	}
	defer unlock()
	if err := s.expireOld(ctx); err != nil {
		return domain.ImportPreview{}, err
	}
	a, err := s.analyze(ctx, s.o.DB, h, target, baseDate, data, "")
	if err != nil {
		return domain.ImportPreview{}, err
	}
	out := domain.ImportPreview{Target: target, BaseDate: baseDate, Counts: a.counts, KeyRule: keyRules[target], Summary: a.summary, FileWarnings: a.parsed.FileWarnings}
	hash := fileHash(data)
	out.SameFileBatch = s.appliedWithHash(ctx, target, hash, baseDate)
	changes := a.counts.Add + a.counts.Chg
	switch {
	case a.counts.Err > 0:
		out.Message = fmt.Sprintf("오류 행 %d건이 있어 반영할 수 없습니다. 오류 파일을 내려받아 고친 뒤 다시 올리세요.", a.counts.Err)
	case changes == 0:
		out.Message = "변경할 내용이 없습니다."
		if out.SameFileBatch != "" {
			out.Message = fmt.Sprintf("이미 반영한 파일입니다(%s). 변경할 내용이 없습니다.", out.SameFileBatch)
		}
	}
	shown := sortForDisplay(a.verdicts)
	if len(shown) > domain.ImportPreviewRows {
		shown, out.RowsTruncated = shown[:domain.ImportPreviewRows], true
	}
	out.Rows = shown
	if a.counts.Err == 0 && changes == 0 { // 전부 동일: 묶음을 만들지 않는다
		return out, nil
	}
	var token, thash string
	if a.counts.Err == 0 {
		if token, thash, err = newToken(); err != nil {
			return domain.ImportPreview{}, err
		}
	}
	now := s.now()
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := s.supersede(ctx, tx, p.UserID, target); err != nil {
			return err
		}
		no, err := nextNo(ctx, tx, now)
		if err != nil {
			return err
		}
		counts, _ := json.Marshal(a.counts)
		sum, _ := json.Marshal(nonNil(a.summary))
		id, err := dbutil.InsertID(ctx, tx, `INSERT INTO import_batches(entity_id, no, target, status, file_sha256, file_size, row_count, template_version, base_date, counts_json, summary_json, plan_sha256, token_hash, created_by, created_at)
			VALUES(`+contract.SQLEntityID(ctx)+`, ?, ?, 'previewed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, no, target, hash, len(data), a.counts.Total, a.parsed.TemplateVersion, baseDate, string(counts), string(sum), a.finger, thash, p.UserID, now.Format(tsLayout))
		if err != nil {
			return err
		}
		for _, v := range a.verdicts {
			ch, _ := json.Marshal(nonNilChanges(v.Changes))
			if _, err := tx.ExecContext(ctx, `INSERT INTO import_rows(batch_id, row_no, key_text, verdict, reason, warn, change_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
				id, v.RowNo, v.Key, v.Verdict, v.Reason, v.Warn, string(ch)); err != nil {
				return err
			}
		}
		out.BatchNo = no
		return nil
	})
	if err != nil {
		return domain.ImportPreview{}, err
	}
	if a.counts.Err == 0 {
		out.Token, out.Applicable = token, true
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilChanges(c []domain.ImportChange) []domain.ImportChange {
	if c == nil {
		return []domain.ImportChange{}
	}
	return c
}

func (s *Service) appliedWithHash(ctx context.Context, target, hash, baseDate string) string {
	var no string
	if err := s.o.DB.QueryRowContext(ctx, `SELECT no FROM import_batches WHERE `+contract.SQLEntity(ctx, "import_batches")+` AND target = ? AND file_sha256 = ? AND base_date = ? AND status = 'applied' ORDER BY id DESC LIMIT 1`, target, hash, baseDate).Scan(&no); err != nil {
		return ""
	}
	return no
}

// nextNo 는 IMP-연도-번호다(KST 연도, 연도마다 1부터).
func nextNo(ctx context.Context, tx contract.Tx, now time.Time) (string, error) {
	year := now.In(export.KST).Format("2006")
	prefix := "IMP-" + year + "-"
	var max int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(substr(no, 10) AS INTEGER)), 0) FROM import_batches WHERE entity_id > 0 AND no LIKE ?`, prefix+"%").Scan(&max); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%04d", prefix, max+1), nil
}

// retire 는 미리보기 묶음을 끝 상태로 보내고 임시 행을 지운다.
func retire(ctx context.Context, tx contract.Tx, id int64, status string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE import_batches SET status = ?, token_hash = '' WHERE `+contract.SQLEntity(ctx, "import_batches")+` AND id = ? AND status = 'previewed'`, status, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM import_rows WHERE batch_id = ?`, id)
	return err
}

func previewedIDs(ctx context.Context, q contract.Tx, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// supersede 는 같은 사람이 같은 대상에 남긴 이전 미리보기를 만료시킨다(새 미리보기가 이전 것을 대신한다).
func (s *Service) supersede(ctx context.Context, tx contract.Tx, userID int64, target string) error {
	ids, err := previewedIDs(ctx, tx, `SELECT id FROM import_batches WHERE `+contract.SQLEntity(ctx, "import_batches")+` AND status = 'previewed' AND created_by = ? AND target = ?`, userID, target)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := retire(ctx, tx, id, domain.ImportExpired); err != nil {
			return err
		}
	}
	return nil
}

// previewTTL 은 미리보기가 반영을 기다려 주는 시간이다. Options.PreviewTTL(시험용)이 있으면 그것을,
// 없으면 app_settings 의 import.preview_ttl_hours(시간 단위 양의 정수)를 매번 읽는다. 값이 없거나 잘못되면 24시간이다.
func (s *Service) previewTTL(ctx context.Context) time.Duration {
	if s.o.PreviewTTL > 0 {
		return s.o.PreviewTTL
	}
	var v string
	if err := s.o.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'import.preview_ttl_hours'`).Scan(&v); err == nil {
		if h, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && h > 0 && h <= 24*365 {
			return time.Duration(h) * time.Hour
		}
	}
	return DefaultPreviewTTL
}

// expireOld 는 기한이 지난 미리보기를 만료시킨다(별도 작업 없이 미리보기·반영·조회 때 정리한다).
func (s *Service) expireOld(ctx context.Context) error {
	cutoff := s.now().Add(-s.previewTTL(ctx)).Format(tsLayout)
	ids, err := previewedIDs(ctx, s.o.DB, `SELECT id FROM import_batches WHERE `+contract.SQLEntity(ctx, "import_batches")+` AND status = 'previewed' AND created_at < ?`, cutoff)
	if err != nil || len(ids) == 0 {
		return err
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		for _, id := range ids {
			if err := retire(ctx, tx, id, domain.ImportExpired); err != nil {
				return err
			}
		}
		return nil
	})
}

type batchRow struct {
	id        int64
	no        string
	target    string
	status    string
	fileHash  string
	baseDate  string
	finger    string
	tokenHash string
	createdBy int64
	createdAt string
}

func (s *Service) loadBatch(ctx context.Context, q contract.Tx, no string) (batchRow, error) {
	var b batchRow
	err := q.QueryRowContext(ctx, `SELECT id, no, target, status, file_sha256, base_date, plan_sha256, token_hash, created_by, created_at FROM import_batches WHERE `+contract.SQLEntity(ctx, "import_batches")+` AND no = ?`, no).
		Scan(&b.id, &b.no, &b.target, &b.status, &b.fileHash, &b.baseDate, &b.finger, &b.tokenHash, &b.createdBy, &b.createdAt)
	if err != nil {
		return b, fmt.Errorf("%w: 가져오기 묶음을 찾을 수 없습니다.", contract.ErrNotFound)
	}
	return b, nil
}

// Apply 는 미리보기한 같은 파일과 반영 토큰으로 반영한다. 파일을 다시 읽어 미리보기와 같은 판정인지 확인한 뒤(E9)
// 한 트랜잭션에서 대상 반영·묶음 상태 전이·감사 한 건을 한다. 어느 하나라도 실패하면 아무것도 반영되지 않는다.
func (s *Service) Apply(ctx context.Context, target, batchNo, token string, data []byte) (domain.ImportBatch, error) {
	p, err := s.authorizeTarget(ctx, target)
	if err != nil {
		return domain.ImportBatch{}, err
	}
	h, err := s.handler(target)
	if err != nil {
		return domain.ImportBatch{}, err
	}
	unlock, err := s.lock(target)
	if err != nil {
		return domain.ImportBatch{}, err
	}
	defer unlock()
	if err := s.expireOld(ctx); err != nil {
		return domain.ImportBatch{}, err
	}
	b, err := s.loadBatch(ctx, s.o.DB, batchNo)
	if err != nil {
		return domain.ImportBatch{}, err
	}
	switch {
	case b.target != target:
		return domain.ImportBatch{}, fmt.Errorf("%w: 가져오기 묶음을 찾을 수 없습니다.", contract.ErrNotFound)
	case b.createdBy != p.UserID:
		return domain.ImportBatch{}, fmt.Errorf("%w: 미리보기를 한 사람만 반영할 수 있습니다.", contract.ErrForbidden)
	case b.status == domain.ImportExpired:
		return domain.ImportBatch{}, fmt.Errorf("%w: 미리보기가 만료되었습니다. 다시 미리보기를 하세요.", contract.ErrInvalidState)
	case b.status != domain.ImportPreviewed:
		return domain.ImportBatch{}, fmt.Errorf("%w: 이미 반영했거나 취소한 가져오기입니다.", contract.ErrInvalidState)
	}
	if token == "" || b.tokenHash == "" || subtle.ConstantTimeCompare([]byte(tokenHash(token)), []byte(b.tokenHash)) != 1 {
		return domain.ImportBatch{}, fmt.Errorf("%w: 반영 확인값이 올바르지 않습니다. 다시 미리보기를 하세요.", contract.ErrValidation)
	}
	if fileHash(data) != b.fileHash {
		return domain.ImportBatch{}, fmt.Errorf("%w: 미리보기한 파일과 다른 파일입니다. 같은 파일을 올려 주세요.", contract.ErrValidation)
	}
	var out domain.ImportBatch
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		a, err := s.analyze(ctx, tx, h, target, b.baseDate, data, b.no)
		if err != nil {
			return err
		}
		if a.finger != b.finger {
			return fmt.Errorf("%w: 미리보기 이후 데이터가 바뀌었습니다. 다시 미리보기를 하세요.", contract.ErrInvalidState)
		}
		if a.counts.Err > 0 {
			return fmt.Errorf("%w: 오류 행 %d건이 있어 반영할 수 없습니다. 오류 파일을 내려받아 고친 뒤 다시 올리세요.", contract.ErrInvalidState, a.counts.Err)
		}
		if a.counts.Add+a.counts.Chg == 0 {
			return fmt.Errorf("%w: 변경할 내용이 없습니다.", contract.ErrInvalidState)
		}
		var rows []domain.ImportRow
		var todo []domain.ImportRowVerdict
		byNo := map[int]domain.ImportRowVerdict{}
		for _, v := range a.verdicts {
			byNo[v.RowNo] = v
		}
		for _, r := range a.rows {
			if v := byNo[r.RowNo]; v.Verdict == domain.ImportVerdictAdd || v.Verdict == domain.ImportVerdictChange {
				rows, todo = append(rows, r), append(todo, v)
			}
		}
		outcome, err := h.Apply(ctx, tx, p, rows, todo, domain.ImportPlanInput{Target: target, BaseDate: b.baseDate, BatchNo: b.no})
		if err != nil {
			return err
		}
		jn, _ := json.Marshal(nonNil(outcome.JournalNos))
		// 토큰은 일회용: 아직 미리보기 상태이고 토큰이 같을 때만 전이한다(동시 반영이 둘 와도 하나만 성공).
		res, err := tx.ExecContext(ctx, `UPDATE import_batches SET status = 'applied', token_hash = '', applied_at = ?, journal_nos_json = ? WHERE `+contract.SQLEntity(ctx, "import_batches")+` AND id = ? AND status = 'previewed' AND token_hash = ?`,
			s.now().Format(tsLayout), string(jn), b.id, b.tokenHash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("%w: 이미 반영했거나 취소한 가져오기입니다.", contract.ErrInvalidState)
		}
		diff := []audit.DiffItem{
			{Key: "import_target", New: domain.ImportTargetLabels[target]},
			{Key: "import_added", New: fmt.Sprint(a.counts.Add)},
			{Key: "import_changed", New: fmt.Sprint(a.counts.Chg)},
			{Key: "import_same", New: fmt.Sprint(a.counts.Same)},
		}
		if b.baseDate != "" {
			diff = append(diff, audit.DiffItem{Key: "base_date", New: b.baseDate})
		}
		if len(outcome.JournalNos) > 0 {
			diff = append(diff, audit.DiffItem{Key: "journal_nos", New: strings.Join(outcome.JournalNos, ", ")})
		}
		if err := s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "import.apply", TargetType: audit.TargetImport, TargetID: b.no, Result: audit.ResultSuccess, Diff: diff}); err != nil {
			return err
		}
		out, err = s.batchSummary(ctx, tx, b.no)
		return err
	})
	if err != nil {
		return domain.ImportBatch{}, err
	}
	return s.maskAmounts(p, out), nil
}

// Cancel 은 미리보기를 버린다(요청자만). 임시 행을 지우고 감사를 남긴다.
func (s *Service) Cancel(ctx context.Context, target, batchNo string) error {
	p, err := s.authorizeTarget(ctx, target)
	if err != nil {
		return err
	}
	b, err := s.loadBatch(ctx, s.o.DB, batchNo)
	if err != nil {
		return err
	}
	switch {
	case b.target != target:
		return fmt.Errorf("%w: 가져오기 묶음을 찾을 수 없습니다.", contract.ErrNotFound)
	case b.createdBy != p.UserID:
		return fmt.Errorf("%w: 미리보기를 한 사람만 취소할 수 있습니다.", contract.ErrForbidden)
	case b.status != domain.ImportPreviewed:
		return fmt.Errorf("%w: 이미 반영했거나 끝난 가져오기라 취소할 수 없습니다.", contract.ErrInvalidState)
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := retire(ctx, tx, b.id, domain.ImportCanceled); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "import.cancel", TargetType: audit.TargetImport, TargetID: b.no, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "import_target", New: domain.ImportTargetLabels[target]}}})
	})
}

func (s *Service) batchSummary(ctx context.Context, q contract.Tx, no string) (domain.ImportBatch, error) {
	var b domain.ImportBatch
	var counts, sum, jn string
	err := q.QueryRowContext(ctx, `SELECT b.no, b.target, b.status, b.base_date, b.counts_json, b.summary_json, b.journal_nos_json, COALESCE(u.name, ''), b.created_at, b.applied_at
		FROM import_batches b LEFT JOIN users u ON u.id = b.created_by WHERE `+contract.SQLEntity(ctx, "b")+` AND b.no = ?`, no).
		Scan(&b.No, &b.Target, &b.Status, &b.BaseDate, &counts, &sum, &jn, &b.CreatedBy, &b.CreatedAt, &b.AppliedAt)
	if err != nil {
		return b, fmt.Errorf("%w: 가져오기 묶음을 찾을 수 없습니다.", contract.ErrNotFound)
	}
	b.TargetName = domain.ImportTargetLabels[b.Target]
	_ = json.Unmarshal([]byte(counts), &b.Counts)
	_ = json.Unmarshal([]byte(sum), &b.Summary)
	_ = json.Unmarshal([]byte(jn), &b.JournalNos)
	return b, nil
}

// maskAmounts 는 금액이 담긴 요약(합계 문장)을 원장·평가 조회 권한이 없으면 가린다(건수까지만, 명세 5절).
func (s *Service) maskAmounts(p contract.Principal, b domain.ImportBatch) domain.ImportBatch {
	if moneyTargets[b.Target] && !canSeeAmounts(p) {
		b.Summary = nil
	}
	return b
}

// History 는 묶음 이력이다. 기본은 반영된 것만이고 Status 가 "all" 이면 미리보기·취소·만료도 본다.
func (s *Service) History(ctx context.Context, f domain.ImportFilter) ([]domain.ImportBatch, error) {
	p, err := contract.Authorize(ctx, contract.PermImportRead)
	if err != nil {
		return nil, err
	}
	if err := s.expireOld(ctx); err != nil {
		return nil, err
	}
	if f.Target != "" {
		if err := validTarget(f.Target); err != nil {
			return nil, err
		}
	}
	status := f.Status
	switch status {
	case "":
		status = domain.ImportApplied
	case "all":
		status = ""
	case domain.ImportPreviewed, domain.ImportApplied, domain.ImportExpired, domain.ImportCanceled:
	default:
		return nil, fmt.Errorf("%w: 상태 조건이 올바르지 않습니다.", contract.ErrValidation)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT b.no FROM import_batches b WHERE `+contract.SQLEntity(ctx, "b")+` AND (? = '' OR b.target = ?) AND (? = '' OR b.status = ?) ORDER BY b.id DESC LIMIT ?`,
		f.Target, f.Target, status, status, limit)
	if err != nil {
		return nil, err
	}
	var nos []string
	for rows.Next() {
		var no string
		if err := rows.Scan(&no); err != nil {
			rows.Close()
			return nil, err
		}
		nos = append(nos, no)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.ImportBatch, 0, len(nos))
	for _, no := range nos {
		b, err := s.batchSummary(ctx, s.o.DB, no)
		if err != nil {
			return nil, err
		}
		out = append(out, s.maskAmounts(p, b))
	}
	return out, nil
}

func (s *Service) batchRows(ctx context.Context, no string) ([]domain.ImportRowVerdict, error) {
	rows, err := s.o.DB.QueryContext(ctx, `SELECT r.row_no, r.key_text, r.verdict, r.reason, r.warn, r.change_json FROM import_rows r JOIN import_batches b ON b.id = r.batch_id WHERE `+contract.SQLEntity(ctx, "b")+` AND b.no = ? ORDER BY r.row_no`, no)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ImportRowVerdict
	for rows.Next() {
		var v domain.ImportRowVerdict
		var ch string
		if err := rows.Scan(&v.RowNo, &v.Key, &v.Verdict, &v.Reason, &v.Warn, &ch); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ch), &v.Changes)
		out = append(out, v)
	}
	return out, rows.Err()
}

// Batch 는 묶음 상세다. 행은 오류·경고·변경·추가·동일 순으로 ImportDetailRows 행까지 보이고 전체는 오류 파일로 받는다.
// 금액성 대상의 바뀐 칸(이전→새 값)은 금액 조회 권한이 없으면 가린다.
func (s *Service) Batch(ctx context.Context, no string) (domain.ImportBatchDetail, error) {
	p, err := contract.Authorize(ctx, contract.PermImportRead)
	if err != nil {
		return domain.ImportBatchDetail{}, err
	}
	sum, err := s.batchSummary(ctx, s.o.DB, no)
	if err != nil {
		return domain.ImportBatchDetail{}, err
	}
	sum = s.maskAmounts(p, sum)
	vs, err := s.batchRows(ctx, no)
	if err != nil {
		return domain.ImportBatchDetail{}, err
	}
	vs = sortForDisplay(vs)
	d := domain.ImportBatchDetail{ImportBatch: sum}
	if len(vs) > domain.ImportDetailRows {
		vs, d.Truncated = vs[:domain.ImportDetailRows], true
	}
	if moneyTargets[sum.Target] && !canSeeAmounts(p) {
		for i := range vs {
			vs[i].Changes = nil
		}
	}
	d.Rows = vs
	return d, nil
}

// ErrorFile 은 오류·경고 행을 담은 XLSX 다(원래 행 번호 포함, 수식 접두 규칙은 B28 부품이 막는다).
// 가져오기 조회 권한이 있거나, 미리보기를 한 본인(해당 대상 가져오기 권한)이면 받는다.
func (s *Service) ErrorFile(ctx context.Context, no string) (domain.ImportFile, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return domain.ImportFile{}, contract.ErrUnauthenticated
	}
	b, err := s.loadBatch(ctx, s.o.DB, no)
	if err != nil {
		return domain.ImportFile{}, err
	}
	if !p.Can(contract.PermImportRead) && !(b.createdBy == p.UserID && p.Can(domain.ImportPerm(b.target))) {
		return domain.ImportFile{}, contract.ErrForbidden
	}
	vs, err := s.batchRows(ctx, no)
	if err != nil {
		return domain.ImportFile{}, err
	}
	if b.status == domain.ImportExpired || b.status == domain.ImportCanceled {
		return domain.ImportFile{}, fmt.Errorf("%w: 만료되었거나 취소한 미리보기라 오류 파일이 없습니다.", contract.ErrInvalidState)
	}
	var rows [][]export.Cell
	for _, v := range sortForDisplay(vs) {
		if v.Verdict != domain.ImportVerdictError && v.Warn == "" {
			continue
		}
		label := "경고"
		if v.Verdict == domain.ImportVerdictError {
			label = "오류"
		}
		reason := v.Reason
		if v.Warn != "" {
			if reason != "" {
				reason += " / "
			}
			reason += "경고: " + v.Warn
		}
		rows = append(rows, []export.Cell{int64(v.RowNo), label, v.Key, reason})
	}
	cols := []export.Column{{Label: "행 번호", Kind: export.KindInt}, {Label: "구분", Kind: export.KindText}, {Label: "키", Kind: export.KindText}, {Label: "사유", Kind: export.KindText}}
	now := s.now()
	data, err := export.WriteXLSX(export.Table{Dataset: export.Dataset{Code: "import_errors", Label: "오류 행", Columns: cols}, Rows: rows},
		export.Meta{At: now, By: p.Name, Filters: fmt.Sprintf("가져오기 %s · %s", b.no, domain.ImportTargetLabels[b.target])})
	if err != nil {
		return domain.ImportFile{}, err
	}
	return domain.ImportFile{Name: export.FileName("가져오기 오류 "+b.no, now, "xlsx"), Data: data}, nil
}
