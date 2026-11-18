package privacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 조회 기록 읽기(명세 5.5·6절). 읽기도 감사 privacy.log_read 로 남는다(기록을 보는 일도 기록된다). 조건 값은 감사에 남기지 않고 어떤 조건을 썼는지(이름만)와 건수만 남긴다.

// Filter 는 조회 조건이다. 영값은 무시한다.
type Filter struct {
	From, To    time.Time
	ActorID     int64
	ActorName   string // 열람자 이름 일부
	Category    string
	Screen      string
	SubjectName string // 정보주체(사람) 이름 일부
	ExportOnly  bool
	AnomalyOnly bool
	Before      int64 // 이 번호보다 오래된 줄만(커서)
	Limit       int   // 1~500, 기본 100
}

// Entry 는 조회 기록 한 줄이다.
type Entry struct {
	ID           int64    `json:"id"`
	At           string   `json:"at"`
	ActorID      int64    `json:"actorId"`
	ActorName    string   `json:"actorName"`
	ActorUnit    string   `json:"actorUnit"`
	ActorRole    string   `json:"actorRole"`
	Channel      string   `json:"channel"`
	Category     string   `json:"category"`
	Screen       string   `json:"screen"`
	Dataset      string   `json:"dataset,omitempty"`
	IsExport     bool     `json:"isExport"`
	Fields       []string `json:"fields"`
	SubjectCount int      `json:"subjectCount"`
	Exposure     string   `json:"exposure"`
	OffHours     bool     `json:"offHours"`
}

// OffHours 는 한국 시간으로 주말이거나 09~18시 밖인지다.
func OffHours(at time.Time) bool {
	k := at.In(kst)
	if k.Weekday() == time.Saturday || k.Weekday() == time.Sunday {
		return true
	}
	return k.Hour() < 9 || k.Hour() >= 18
}

func parseAt(s string) time.Time {
	t, err := time.Parse(atLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (r *Recorder) authorizeRead(ctx context.Context, p contract.Principal) error {
	_, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermPrivacyLogRead)
	return err
}

// recordRead 는 감사 privacy.log_read 를 남긴다(별도 짧은 트랜잭션, 실패하면 조회 결과를 주지 않는다).
func (r *Recorder) recordRead(ctx context.Context, p contract.Principal, what string, conds []string, rows int) error {
	p.Channel = contract.ChannelWeb
	return r.txs.WithTx(ctx, func(tx contract.Tx) error {
		return r.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "privacy.log_read", TargetType: audit.TargetPrivacyLog, TargetID: what, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "filters", New: strings.Join(conds, ", ")}, {Key: "rows", New: strconv.Itoa(rows)}}})
	})
}

const unitSQL = `(SELECT us.name FROM org_person_spans ps JOIN org_unit_spans us ON us.unit_id = ps.primary_unit_id AND us.valid_from <= ? AND (us.valid_to IS NULL OR ? < us.valid_to)
	WHERE ps.user_id = %s AND ps.left_flag = 0 AND ps.valid_from <= ? AND (ps.valid_to IS NULL OR ? < ps.valid_to) LIMIT 1)`

func (r *Recorder) today() string { return r.now().In(kst).Format("2006-01-02") }

// List 는 조회 기록을 최신순으로 돌려준다(privacy:log_read).
func (r *Recorder) List(ctx context.Context, p contract.Principal, f Filter) ([]Entry, error) {
	return r.query(ctx, p, f, 500, true)
}

// ExportEntries 는 내보내기용 조회 기록이다(최대 max 건, privacy:log_read). 건수만 세는 호출(record=false)은 감사를 남기지 않고,
// 행을 만드는 호출(record=true)은 조회 기록을 읽은 것이므로 감사 privacy.log_read 를 남긴다(내보내기 감사 export.run 은 핸들러가 따로 남긴다).
func (r *Recorder) ExportEntries(ctx context.Context, p contract.Principal, f Filter, max int, record bool) ([]Entry, error) {
	f.Limit = max
	return r.query(ctx, p, f, max, record)
}

func (r *Recorder) query(ctx context.Context, p contract.Principal, f Filter, limitCap int, record bool) ([]Entry, error) {
	if err := r.authorizeRead(ctx, p); err != nil {
		return nil, err
	}
	if f.Limit < 1 || f.Limit > limitCap {
		f.Limit = 100
	}
	day := r.today()
	unit := strings.Replace(unitSQL, "%s", "l.actor_id", 1)
	where, args := []string{"1 = 1"}, []any{day, day, day, day}
	var conds []string
	add := func(name, cond string, a ...any) {
		conds = append(conds, name)
		where, args = append(where, cond), append(args, a...)
	}
	if !f.From.IsZero() {
		add("기간", "l.at >= ?", f.From.UTC().Format(atLayout))
	}
	if !f.To.IsZero() {
		if len(conds) == 0 || conds[len(conds)-1] != "기간" {
			conds = append(conds, "기간")
		}
		where, args = append(where, "l.at < ?"), append(args, f.To.UTC().Format(atLayout))
	}
	if f.ActorID != 0 {
		add("열람자", "l.actor_id = ?", f.ActorID)
	}
	if f.ActorName != "" {
		add("열람자 이름", "EXISTS (SELECT 1 FROM users au WHERE au.id = l.actor_id AND au.name LIKE ?)", "%"+f.ActorName+"%")
	}
	if f.Category != "" {
		add("범주", "l.category = ?", f.Category)
	}
	if f.Screen != "" {
		add("화면", "l.screen = ?", f.Screen)
	}
	if f.SubjectName != "" {
		add("정보주체 이름", "EXISTS (SELECT 1 FROM privacy_access_subjects s JOIN users su ON su.id = s.subject_id WHERE s.log_id = l.id AND s.subject_type = 'user' AND su.name LIKE ?)", "%"+f.SubjectName+"%")
	}
	if f.ExportOnly {
		add("내보내기만", "l.is_export = 1")
	}
	if f.AnomalyOnly {
		add("MCP 이상", "l.exposure = 'mcp_anomaly'")
	}
	if f.Before > 0 {
		where, args = append(where, "l.id < ?"), append(args, f.Before)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT l.id, l.at, l.actor_id, COALESCE(u.name, ''), `+unit+`, l.actor_role, l.channel, l.category, l.screen, l.dataset, l.is_export, l.fields, l.subject_count, l.exposure
		FROM privacy_access_log l LEFT JOIN users u ON u.id = l.actor_id WHERE `+strings.Join(where, " AND ")+` ORDER BY l.id DESC LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var unitName sql.NullString
		var fields string
		var export int
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.ActorName, &unitName, &e.ActorRole, &e.Channel, &e.Category, &e.Screen, &e.Dataset, &export, &fields, &e.SubjectCount, &e.Exposure); err != nil {
			return nil, err
		}
		e.ActorUnit, e.IsExport = unitName.String, export == 1
		_ = json.Unmarshal([]byte(fields), &e.Fields)
		if e.Fields == nil {
			e.Fields = []string{}
		}
		e.OffHours = OffHours(parseAt(e.At))
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if record {
		if err := r.recordRead(ctx, p, "log", conds, len(out)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SubjectView 는 한 요청의 정보주체 한 줄이다.
type SubjectView struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Unit string `json:"unit,omitempty"`
	Left bool   `json:"left"`
}

// Subjects 는 한 요청의 정보주체 목록(이름·부서)이다. 퇴사자는 Left 로 표시한다. total 은 식별한 정보주체 수다.
func (r *Recorder) Subjects(ctx context.Context, p contract.Principal, logID int64, offset, limit int) ([]SubjectView, int, error) {
	if err := r.authorizeRead(ctx, p); err != nil {
		return nil, 0, err
	}
	if limit < 1 || limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_access_log WHERE id = ?`, logID).Scan(&exists); err != nil {
		return nil, 0, err
	}
	if exists == 0 {
		return nil, 0, contract.ErrNotFound
	}
	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM privacy_access_subjects WHERE log_id = ?`, logID).Scan(&total); err != nil {
		return nil, 0, err
	}
	day := r.today()
	unit := strings.Replace(unitSQL, "%s", "s.subject_id", 1)
	rows, err := r.db.QueryContext(ctx, `SELECT s.subject_type, s.subject_id,
		CASE s.subject_type WHEN 'user' THEN COALESCE((SELECT name FROM users WHERE id = s.subject_id), '')
			WHEN 'vendor' THEN COALESCE((SELECT name FROM vendors WHERE id = s.subject_id), '')
			WHEN 'payment' THEN COALESCE((SELECT no FROM payment_requests WHERE id = s.subject_id), '')
			ELSE '' END,
		CASE s.subject_type WHEN 'user' THEN `+unit+` ELSE NULL END,
		CASE s.subject_type WHEN 'user' THEN COALESCE((SELECT u.active FROM users u WHERE u.id = s.subject_id), 0) ELSE 1 END
		FROM privacy_access_subjects s WHERE s.log_id = ? ORDER BY s.subject_type, s.subject_id LIMIT ? OFFSET ?`, day, day, day, day, logID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []SubjectView{}
	for rows.Next() {
		var v SubjectView
		var unitName sql.NullString
		var active int
		if err := rows.Scan(&v.Type, &v.ID, &v.Name, &unitName, &active); err != nil {
			return nil, 0, err
		}
		v.Unit, v.Left = unitName.String, active == 0
		if v.Name == "" {
			switch v.Type {
			case SubjectCompany:
				v.Name = "회사 자금 계좌"
			case SubjectPayment:
				v.Name = "지급 건"
			case SubjectCounterparty:
				v.Name = "세금계산서 거래 상대방"
			default:
				v.Name = "(삭제된 대상)"
			}
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := r.recordRead(ctx, p, "subjects", []string{"요청 번호"}, len(out)); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Stats 는 최근 days 일의 열람 통계(범주별·열람자별·업무시간 밖·한 번에 많이 본 요청)다.
type Stats struct {
	Days       int         `json:"days"`
	Total      int         `json:"total"`
	OffHours   int         `json:"offHours"`
	Exports    int         `json:"exports"`
	Anomalies  int         `json:"anomalies"`
	ByCategory []CatStat   `json:"byCategory"`
	ByActor    []ActorStat `json:"byActor"`
	Large      []Entry     `json:"large"`
}

type CatStat struct {
	Category string `json:"category"`
	Requests int    `json:"requests"`
	Subjects int    `json:"subjects"`
}

type ActorStat struct {
	ActorID  int64  `json:"actorId"`
	Name     string `json:"name"`
	Unit     string `json:"unit,omitempty"`
	Requests int    `json:"requests"`
	Subjects int    `json:"subjects"`
}

func (r *Recorder) Stats(ctx context.Context, p contract.Principal, days int) (Stats, error) {
	if err := r.authorizeRead(ctx, p); err != nil {
		return Stats{}, err
	}
	if days < 1 || days > 366 {
		days = 30
	}
	st := Stats{Days: days, ByCategory: []CatStat{}, ByActor: []ActorStat{}, Large: []Entry{}}
	from := r.now().UTC().AddDate(0, 0, -days).Format(atLayout)
	rows, err := r.db.QueryContext(ctx, `SELECT id, at, actor_id, category, subject_count, is_export, exposure, screen FROM privacy_access_log WHERE at >= ? ORDER BY id DESC LIMIT 50000`, from)
	if err != nil {
		return st, err
	}
	cat := map[string]*CatStat{}
	act := map[int64]*ActorStat{}
	type big struct {
		id    int64
		count int
	}
	var bigs []big
	for rows.Next() {
		var id, actor int64
		var at, category, exposure, screen string
		var count, export int
		if err := rows.Scan(&id, &at, &actor, &category, &count, &export, &exposure, &screen); err != nil {
			rows.Close()
			return st, err
		}
		st.Total++
		if OffHours(parseAt(at)) {
			st.OffHours++
		}
		if export == 1 {
			st.Exports++
		}
		if exposure == "mcp_anomaly" {
			st.Anomalies++
		}
		c := cat[category]
		if c == nil {
			c = &CatStat{Category: category}
			cat[category] = c
		}
		c.Requests++
		c.Subjects += count
		a := act[actor]
		if a == nil {
			a = &ActorStat{ActorID: actor}
			act[actor] = a
		}
		a.Requests++
		a.Subjects += count
		bigs = append(bigs, big{id, count})
	}
	rows.Close()
	for _, c := range cat {
		st.ByCategory = append(st.ByCategory, *c)
	}
	sort.Slice(st.ByCategory, func(i, j int) bool { return st.ByCategory[i].Category < st.ByCategory[j].Category })
	for _, a := range act {
		st.ByActor = append(st.ByActor, *a)
	}
	sort.Slice(st.ByActor, func(i, j int) bool {
		if st.ByActor[i].Subjects != st.ByActor[j].Subjects {
			return st.ByActor[i].Subjects > st.ByActor[j].Subjects
		}
		return st.ByActor[i].ActorID < st.ByActor[j].ActorID
	})
	if len(st.ByActor) > 10 {
		st.ByActor = st.ByActor[:10]
	}
	day := r.today()
	for i := range st.ByActor {
		var name sql.NullString
		var unit sql.NullString
		_ = r.db.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, st.ByActor[i].ActorID).Scan(&name)
		unitQ := strings.Replace(unitSQL, "%s", "?", 1)
		_ = r.db.QueryRowContext(ctx, `SELECT `+unitQ, day, day, st.ByActor[i].ActorID, day, day).Scan(&unit)
		st.ByActor[i].Name, st.ByActor[i].Unit = name.String, unit.String
	}
	sort.Slice(bigs, func(i, j int) bool {
		if bigs[i].count != bigs[j].count {
			return bigs[i].count > bigs[j].count
		}
		return bigs[i].id > bigs[j].id
	})
	if len(bigs) > 10 {
		bigs = bigs[:10]
	}
	for _, b := range bigs {
		es, err := r.entryByID(ctx, b.id)
		if err != nil {
			return st, err
		}
		st.Large = append(st.Large, es)
	}
	if err := r.recordRead(ctx, p, "stats", []string{"통계"}, st.Total); err != nil {
		return st, err
	}
	return st, nil
}

func (r *Recorder) entryByID(ctx context.Context, id int64) (Entry, error) {
	day := r.today()
	unit := strings.Replace(unitSQL, "%s", "l.actor_id", 1)
	var e Entry
	var unitName sql.NullString
	var fields string
	var export int
	err := r.db.QueryRowContext(ctx, `SELECT l.id, l.at, l.actor_id, COALESCE(u.name, ''), `+unit+`, l.actor_role, l.channel, l.category, l.screen, l.dataset, l.is_export, l.fields, l.subject_count, l.exposure
		FROM privacy_access_log l LEFT JOIN users u ON u.id = l.actor_id WHERE l.id = ?`, day, day, day, day, id).
		Scan(&e.ID, &e.At, &e.ActorID, &e.ActorName, &unitName, &e.ActorRole, &e.Channel, &e.Category, &e.Screen, &e.Dataset, &export, &fields, &e.SubjectCount, &e.Exposure)
	if err != nil {
		return e, err
	}
	e.ActorUnit, e.IsExport = unitName.String, export == 1
	_ = json.Unmarshal([]byte(fields), &e.Fields)
	if e.Fields == nil {
		e.Fields = []string{}
	}
	e.OffHours = OffHours(parseAt(e.At))
	return e, nil
}

// UnitsOf 는 사용자들의 현재 소속(주 조직) 이름이다. 소속이 없으면 항목이 없다(월별 보고서 같은 사람 단위 요약용).
func (r *Recorder) UnitsOf(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	day := r.today()
	q := strings.Replace(unitSQL, "%s", "?", 1)
	for _, id := range ids {
		var name sql.NullString
		if err := r.db.QueryRowContext(ctx, `SELECT `+q, day, day, id, day, day).Scan(&name); err != nil {
			return nil, err
		}
		if name.Valid {
			out[id] = name.String
		}
	}
	return out, nil
}
