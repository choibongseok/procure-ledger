// Package privreport 는 월별 점검 보고서(B41 ④, 9절)다.
// 권한 분리 위반·예외와 개인정보 조회 기록을 한 달 단위로 모아 5개 시트의 XLSX 로 만든다. 파일은 서버에 저장하지 않고 요청할 때 만든다(D13).
// 정보주체 이름은 파일에 넣지 않는다(열람자 이름·부서만). 매월 첫 영업일에는 알림만 보낸다(Notifier).
package privreport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

const (
	maxRows  = 50000 // 시트 하나의 행 상한(넘으면 달을 좁힐 수 없으므로 거부)
	bigCount = 100   // 한 번에 이만큼 넘게 본 요청을 따로 적는다
	bigLimit = 200
)

var kst = time.FixedZone("KST", 9*3600)

// Service 는 보고서 생성기다.
type Service struct {
	db      *sql.DB
	txs     contract.TxManager
	audit   audit.Recorder
	priv    *privacy.Recorder
	now     func() time.Time
	limiter *export.Limiter
	// BusinessDay 는 영업일 판정(B34 달력)이다. nil 이면 토·일만 쉬는 날로 본다. 월 알림이 첫 영업일을 찾을 때 쓴다.
	businessDay func(ctx context.Context, q contract.Tx, day string) (bool, error)
	deliver     func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error
}

// Options 는 Service 구성이다.
type Options struct {
	DB    *sql.DB
	Txs   contract.TxManager
	Audit audit.Recorder
	Priv  *privacy.Recorder
	Now   func() time.Time
}

// New 는 Service 를 만든다.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil || o.Priv == nil {
		return nil, errors.New("privreport: DB·Txs·Audit·Priv 가 필요합니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{db: o.DB, txs: o.Txs, audit: o.Audit, priv: o.Priv, now: o.Now, limiter: export.NewLimiter(o.Now)}, nil
}

// SetBusinessDay 는 영업일 판정을 꽂는다(영업일 달력은 업무 서비스에 있어 조립 때 나중에 붙인다).
func (s *Service) SetBusinessDay(f func(ctx context.Context, q contract.Tx, day string) (bool, error)) {
	s.businessDay = f
}

// SetDeliver 는 알림의 메일·웹훅 발송 건 생성기를 꽂는다.
func (s *Service) SetDeliver(f func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error) {
	s.deliver = f
}

func verr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

// Result 는 생성한 파일과 이름·건수다.
type Result struct {
	Body     []byte
	FileName string
	Rows     int // 모든 시트의 데이터 행 합
	Month    string
}

// LastMonth 는 now 의 직전 달(KST) "YYYY-MM" 이다.
func LastMonth(now time.Time) string {
	t := now.In(kst)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, kst).AddDate(0, -1, 0).Format("2006-01")
}

// monthRange 는 달의 [시작, 다음 달 시작) 을 UTC 고정 폭 문자열로 돌려준다.
func monthRange(month string) (lo, hi string, err error) {
	t, perr := time.ParseInLocation("2006-01", month, kst)
	if perr != nil {
		return "", "", verr("월 형식은 YYYY-MM 이어야 합니다")
	}
	const layout = "2006-01-02T15:04:05.000000000Z"
	return t.UTC().Format(layout), t.AddDate(0, 1, 0).UTC().Format(layout), nil
}

var categoryLabel = map[string]string{"contact": "연락처", "account": "계좌", "card": "카드 내역", "employee_no": "사번", "attendance": "근태·휴가 사유"}
var resultLabel = map[string]string{"allowed": "경고(통과)", "blocked": "차단", "excepted": "예외 통과"}
var subjectLabel = map[string]string{"payment": "지급", "journal": "전표", "purchase_request": "구매요청", "card_txn": "카드 내역", "user": "사용자", "role_grant": "역할 부여", "role_change_request": "역할 변경 요청"}
var exStatusLabel = map[string]string{"requested": "승인 대기", "active": "사용 가능", "used": "사용함", "rejected": "반려", "expired": "만료", "revoked": "회수"}

func ruleTitle(code string) string {
	if r, ok := sod.RuleByCode(code); ok {
		return r.Title
	}
	return code
}

func t(label string) export.Column  { return export.Column{Label: label, Kind: export.KindText} }
func n(label string) export.Column  { return export.Column{Label: label, Kind: export.KindInt} }
func dt(label string) export.Column { return export.Column{Label: label, Kind: export.KindDateTime} }
func b(label string) export.Column  { return export.Column{Label: label, Kind: export.KindBool} }

func parseAt(s string) time.Time {
	v, _ := time.Parse("2006-01-02T15:04:05.000000000Z", s)
	return v
}

// Build 는 한 달의 보고서를 만든다. 호출자(핸들러)가 권한(sod:read + privacy:log_read)과 감사를 맡는다.
func (s *Service) Build(ctx context.Context, p contract.Principal, month string) (Result, error) {
	lo, hi, err := monthRange(month)
	if err != nil {
		return Result{}, err
	}
	if cur := s.now().In(kst).Format("2006-01"); month > cur {
		return Result{}, verr("아직 오지 않은 달입니다")
	}
	viol, err := s.violations(ctx, lo, hi)
	if err != nil {
		return Result{}, err
	}
	exc, err := s.exceptions(ctx, lo, hi)
	if err != nil {
		return Result{}, err
	}
	logSum, logRows, err := s.logSummary(ctx, lo, hi)
	if err != nil {
		return Result{}, err
	}
	holds, err := s.holds(ctx)
	if err != nil {
		return Result{}, err
	}
	summary := export.Table{Dataset: export.Dataset{Label: "요약", Columns: []export.Column{t("구분"), t("항목"), n("건수")}}, Rows: s.summaryRows(viol.counts, logSum)}
	tables := []export.Table{
		summary,
		{Dataset: export.Dataset{Label: "위반 목록", Columns: []export.Column{dt("시각"), t("규칙"), t("결과"), t("거래 종류"), t("거래 번호"), t("행위자"), t("앞 단계 행위자")}}, Rows: viol.rows},
		{Dataset: export.Dataset{Label: "예외 승인 사유", Columns: []export.Column{t("규칙"), t("거래 종류"), t("거래 번호"), t("예외 대상자"), t("사유"), t("요청자"), t("승인자"), dt("요청 시각"), t("상태"), b("사용함")}}, Rows: exc},
		{Dataset: export.Dataset{Label: "조회 기록 요약", Columns: []export.Column{t("구분"), t("범주"), t("화면"), t("열람자"), t("부서"), dt("시각"), n("요청 건수"), n("정보주체 수")}}, Rows: logRows},
		{Dataset: export.Dataset{Label: "보유 현황(hold)", Columns: []export.Column{t("규칙"), t("권한 조합"), n("보유자 수"), t("보유자")}}, Rows: holds},
	}
	total := 0
	for _, tb := range tables {
		total += len(tb.Rows)
	}
	at := s.now()
	info := [][2]string{
		{"보고서", "월별 점검 보고서(권한 분리·개인정보 조회)"},
		{"대상 월", month},
		{"생성 일시", at.In(kst).Format("2006-01-02 15:04")},
		{"생성자", p.Name},
		{"행 수", fmt.Sprint(total)},
		{"주의", "정보주체 이름은 싣지 않았습니다. 조회 기록은 보관 기간이 지난 달은 비어 있을 수 있습니다."},
	}
	body, err := export.WriteWorkbook(tables, info)
	if err != nil {
		return Result{}, err
	}
	return Result{Body: body, FileName: export.FileName("월별 점검 보고서_"+month, at, "xlsx"), Rows: total, Month: month}, nil
}

type violData struct {
	rows   [][]export.Cell
	counts map[string][3]int // 규칙 → 허용·차단·예외
}

func (s *Service) violations(ctx context.Context, lo, hi string) (violData, error) {
	out := violData{counts: map[string][3]int{}}
	rows, err := s.db.QueryContext(ctx, `SELECT v.at, v.rule_code, v.result, v.subject_type, v.subject_no, v.subject_id, COALESCE(a.name,''), COALESCE(o.name,'')
		FROM sod_violations v LEFT JOIN users a ON a.id = v.actor_id LEFT JOIN users o ON o.id = v.other_id AND v.other_id <> 0
		WHERE v.at >= ? AND v.at < ? ORDER BY v.id LIMIT ?`, lo, hi, maxRows+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var at, rule, result, st, no, actor, other string
		var sid int64
		if err := rows.Scan(&at, &rule, &result, &st, &no, &sid, &actor, &other); err != nil {
			return out, err
		}
		if len(out.rows) >= maxRows {
			return out, verr("위반이 한 달에 %d건을 넘어 보고서를 만들 수 없습니다", maxRows)
		}
		c := out.counts[rule]
		switch result {
		case "allowed":
			c[0]++
		case "blocked":
			c[1]++
		case "excepted":
			c[2]++
		}
		out.counts[rule] = c
		if no == "" {
			no = fmt.Sprintf("#%d", sid)
		}
		out.rows = append(out.rows, []export.Cell{parseAt(at), ruleTitle(rule), resultLabel[result], subjectLabel[st], no, actor, other})
	}
	return out, rows.Err()
}

func (s *Service) exceptions(ctx context.Context, lo, hi string) ([][]export.Cell, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT x.rule_code, x.subject_type, x.subject_no, x.subject_id, COALESCE(tu.name,''), x.reason, COALESCE(ru.name,''), COALESCE(au.name,''), x.requested_at, x.status, x.used_at
		FROM sod_exceptions x LEFT JOIN users tu ON tu.id = x.user_id LEFT JOIN users ru ON ru.id = x.requested_by LEFT JOIN users au ON au.id = x.approved_by
		WHERE x.requested_at >= ? AND x.requested_at < ? ORDER BY x.id LIMIT ?`, lo, hi, maxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]export.Cell
	for rows.Next() {
		var rule, st, no, target, reason, req, app, at, status string
		var sid int64
		var used sql.NullString
		if err := rows.Scan(&rule, &st, &no, &sid, &target, &reason, &req, &app, &at, &status, &used); err != nil {
			return nil, err
		}
		if no == "" {
			no = fmt.Sprintf("#%d", sid)
		}
		out = append(out, []export.Cell{ruleTitle(rule), subjectLabel[st], no, target, reason, req, app, parseAt(at), exStatusLabel[status], used.Valid})
	}
	return out, rows.Err()
}

type logSummary struct {
	byCategory map[string]int
	total      int
	exports    int
	offHours   int
	anomalies  int
	top        []actorTop
}

type actorTop struct {
	id       int64
	name     string
	unit     string
	subjects int
}

func (s *Service) logSummary(ctx context.Context, lo, hi string) (logSummary, [][]export.Cell, error) {
	sum := logSummary{byCategory: map[string]int{}}
	rows, err := s.db.QueryContext(ctx, `SELECT l.id, l.at, l.actor_id, COALESCE(u.name,''), l.category, l.screen, l.subject_count, l.is_export, l.exposure
		FROM privacy_access_log l LEFT JOIN users u ON u.id = l.actor_id WHERE l.at >= ? AND l.at < ? ORDER BY l.id LIMIT ?`, lo, hi, maxRows+1)
	if err != nil {
		return sum, nil, err
	}
	type key struct{ cat, screen string }
	type agg struct{ req, subj int }
	groups := map[key]*agg{}
	actors := map[int64]*actorTop{}
	type big struct {
		at, cat, screen, name string
		actor                 int64
		count                 int
	}
	var bigs []big
	count := 0
	for rows.Next() {
		var id, actor int64
		var at, name, cat, screen, exposure string
		var cnt, exp int
		if err := rows.Scan(&id, &at, &actor, &name, &cat, &screen, &cnt, &exp, &exposure); err != nil {
			rows.Close()
			return sum, nil, err
		}
		count++
		if count > maxRows {
			rows.Close()
			return sum, nil, verr("조회 기록이 한 달에 %d건을 넘어 보고서를 만들 수 없습니다", maxRows)
		}
		sum.total++
		sum.byCategory[cat]++
		if exp == 1 {
			sum.exports++
		}
		if exposure == "mcp_anomaly" {
			sum.anomalies++
		}
		if privacy.OffHours(parseAt(at)) {
			sum.offHours++
		}
		g := groups[key{cat, screen}]
		if g == nil {
			g = &agg{}
			groups[key{cat, screen}] = g
		}
		g.req++
		g.subj += cnt
		a := actors[actor]
		if a == nil {
			a = &actorTop{id: actor, name: name}
			actors[actor] = a
		}
		a.subjects += cnt
		if cnt > bigCount {
			bigs = append(bigs, big{at, cat, screen, name, actor, cnt})
		}
	}
	if err := rows.Close(); err != nil {
		return sum, nil, err
	}
	for _, a := range actors {
		sum.top = append(sum.top, *a)
	}
	sort.Slice(sum.top, func(i, j int) bool {
		if sum.top[i].subjects != sum.top[j].subjects {
			return sum.top[i].subjects > sum.top[j].subjects
		}
		return sum.top[i].id < sum.top[j].id
	})
	if len(sum.top) > 5 {
		sum.top = sum.top[:5]
	}
	sort.Slice(bigs, func(i, j int) bool {
		if bigs[i].count != bigs[j].count {
			return bigs[i].count > bigs[j].count
		}
		return bigs[i].at < bigs[j].at
	})
	if len(bigs) > bigLimit {
		bigs = bigs[:bigLimit]
	}
	ids := make([]int64, 0, len(sum.top)+len(bigs))
	for _, a := range sum.top {
		ids = append(ids, a.id)
	}
	for _, g := range bigs {
		ids = append(ids, g.actor)
	}
	units, err := s.priv.UnitsOf(ctx, ids)
	if err != nil {
		return sum, nil, err
	}
	for i := range sum.top {
		sum.top[i].unit = units[sum.top[i].id]
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].cat != keys[j].cat {
			return keys[i].cat < keys[j].cat
		}
		return keys[i].screen < keys[j].screen
	})
	var out [][]export.Cell
	for _, k := range keys {
		g := groups[k]
		out = append(out, []export.Cell{"범주·화면별", categoryLabel[k.cat], k.screen, "", "", nil, g.req, g.subj})
	}
	out = append(out, []export.Cell{"업무시간 밖 열람", "", "", "", "", nil, sum.offHours, 0})
	for _, g := range bigs {
		out = append(out, []export.Cell{fmt.Sprintf("한 번에 %d명 초과", bigCount), categoryLabel[g.cat], g.screen, g.name, units[g.actor], parseAt(g.at), 1, g.count})
	}
	return sum, out, nil
}

func (s *Service) summaryRows(counts map[string][3]int, l logSummary) [][]export.Cell {
	var rows [][]export.Cell
	var tot [3]int
	codes := make([]string, 0, len(counts))
	for code, c := range counts {
		codes = append(codes, code)
		for i := range tot {
			tot[i] += c[i]
		}
	}
	rows = append(rows, []export.Cell{"권한 분리 위반(전체)", "경고(통과)", tot[0]}, []export.Cell{"권한 분리 위반(전체)", "차단", tot[1]}, []export.Cell{"권한 분리 위반(전체)", "예외 통과", tot[2]})
	order := map[string]int{}
	for i, r := range sod.Rules {
		order[r.Code] = i
	}
	sort.Slice(codes, func(i, j int) bool { return order[codes[i]] < order[codes[j]] })
	for _, code := range codes {
		c := counts[code]
		rows = append(rows, []export.Cell{"규칙별 위반", ruleTitle(code) + " · 경고(통과)", c[0]}, []export.Cell{"규칙별 위반", ruleTitle(code) + " · 차단", c[1]}, []export.Cell{"규칙별 위반", ruleTitle(code) + " · 예외 통과", c[2]})
	}
	rows = append(rows, []export.Cell{"조회 기록", "전체 요청", l.total}, []export.Cell{"조회 기록", "내보내기", l.exports}, []export.Cell{"조회 기록", "업무시간 밖 열람", l.offHours}, []export.Cell{"조회 기록", "AI 도구 원문 유출 이상", l.anomalies})
	cats := make([]string, 0, len(l.byCategory))
	for c := range l.byCategory {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	for _, c := range cats {
		rows = append(rows, []export.Cell{"조회 기록(범주별)", categoryLabel[c], l.byCategory[c]})
	}
	for i, a := range l.top {
		who := a.name
		if a.unit != "" {
			who += " (" + a.unit + ")"
		}
		rows = append(rows, []export.Cell{fmt.Sprintf("열람 상위 5인(정보주체 수) %d위", i+1), who, a.subjects})
	}
	return rows
}

func (s *Service) holds(ctx context.Context) ([][]export.Cell, error) {
	set, err := sod.Holds(ctx, s.db)
	if err != nil {
		return nil, err
	}
	var out [][]export.Cell
	for _, r := range sod.Rules {
		if r.Kind != sod.KindHold {
			continue
		}
		ids := make([]int64, 0, len(set[r.Code]))
		for id := range set[r.Code] {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		names := make([]string, 0, len(ids))
		for _, id := range ids {
			var nm string
			if err := s.db.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ? AND active = 1`, id).Scan(&nm); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return nil, err
			}
			names = append(names, nm)
		}
		out = append(out, []export.Cell{r.Title, string(r.PermA) + " + " + string(r.PermB), len(names), strings.Join(names, ", ")})
	}
	return out, nil
}
