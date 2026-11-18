// 내보내기(B28) 원천 중 platform 몫: 감사 로그, 조직 구성원. 공통 부품은 internal/export 다.
package handler

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/org"
	"github.com/choibongseok/procure-ledger/internal/privacy"
	"github.com/choibongseok/procure-ledger/web"
)

// RegisterExports 는 platform 소유 목록의 내보내기 라우트를 등록한다(감사 로그, 조직 구성원). 권한은 그 목록을 읽는 권한과 같다.
// 다른 영역의 목록은 각자 export.Register(r, path, perm, d.Export, source) 로 붙는다.
func RegisterExports(r contract.Registrar, d deps.Deps) {
	if d.Export == nil {
		return
	}
	if d.AuditScan != nil && d.AuditReader != nil {
		export.Register(r, "/api/audit-logs/export", contract.PermAuditRead, d.Export, &auditExportSource{h: &adminHandlers{d: d}})
	}
	if svc, ok := d.Org.(*org.Service); ok {
		export.Register(r, "/api/org/people/export", contract.PermOrgRead, d.Export, &orgPeopleSource{svc: svc})
	}
}

// ---- 한국어 라벨(web/src/admin/auditLabels.ts·targetTypes.ts 가 정본, 서버가 임베드한 원문을 읽는다) ----

type labelTables struct {
	action, diffKey, value, target, channel, result map[string]string
}

var labels = sync.OnceValue(func() labelTables {
	src, targets := web.AuditLabelSources()
	return labelTables{
		action: tsMap(src, "ACTION_LABELS"), diffKey: tsMap(src, "DIFF_KEY_LABELS"), value: tsMap(src, "VALUE_LABELS"),
		channel: tsMap(src, "CHANNEL_LABELS"), result: tsMap(src, "RESULT_LABELS"), target: tsMap(targets, "TARGET_TYPE_LABELS"),
	}
})

var tsPair = regexp.MustCompile(`(?:"([^"\n]+)"|([A-Za-z_][A-Za-z0-9_]*))\s*:\s*("(?:[^"\\\n]|\\.)*")`)

// tsMap 은 `NAME ... = { key: "값", ... };` 블록의 문자열 쌍을 읽는다.
func tsMap(src, name string) map[string]string {
	out := map[string]string{}
	i := strings.Index(src, name+":")
	if i < 0 {
		i = strings.Index(src, name+" ")
	}
	if i < 0 {
		return out
	}
	open := strings.Index(src[i:], "= {")
	if open < 0 {
		return out
	}
	start := i + open + 2
	end := strings.Index(src[start:], "};")
	if end < 0 {
		return out
	}
	for _, m := range tsPair.FindAllStringSubmatch(src[start:start+end], -1) {
		k := m[1]
		if k == "" {
			k = m[2]
		}
		if v, err := strconv.Unquote(m[3]); err == nil {
			out[k] = v
		}
	}
	return out
}

func (l labelTables) or(m map[string]string, k, fallback string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return fallback
}

var (
	hexValue = regexp.MustCompile(`(?i)^[0-9a-f]{12,}…?$`)
	isoTime  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$`)
	stepVal  = regexp.MustCompile(`^(\d+)/([a-z_]+)$`)
	readable = regexp.MustCompile(`^[가-힣0-9\s.,()·\-]+$`)
	moneyKey = map[string]bool{"total_amount": true, "amount": true, "unit_price": true, "price": true}
)

func commaInt(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

// diffText 는 변경 한 항목을 화면과 같은 규칙으로 "항목: 이전 → 이후" 글자로 만든다(화면의 표현 일부를 단순화했다).
func (l labelTables) diffText(d audit.DiffItem, tokenLabel string) string {
	val := func(v string) string {
		if v == "" {
			return ""
		}
		switch {
		case d.Key == "sha256" || hexValue.MatchString(v):
			return "기록됨"
		case d.Key == audit.DiffKeyTokenID:
			if tokenLabel != "" {
				return tokenLabel
			}
			return "삭제되었거나 알 수 없는 토큰"
		case d.Key == "duration_ms":
			return v + "ms"
		case isoTime.MatchString(v):
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t.In(export.KST).Format("2006-01-02 15:04")
			}
		case moneyKey[d.Key] && regexp.MustCompile(`^-?\d+$`).MatchString(v):
			return commaInt(v) + "원"
		case d.Key == "step":
			if m := stepVal.FindStringSubmatch(v); m != nil {
				return m[1] + "단계 " + l.or(l.value, m[2], "결재")
			}
		}
		return l.or(l.value, v, v)
	}
	label := l.or(l.diffKey, d.Key, "기타 항목")
	before, after := val(d.Old), val(d.New)
	if before != "" && before != after {
		return label + ": " + before + " → " + after
	}
	return label + ": " + after
}

func (l labelTables) targetText(tt, id string) string {
	name := l.or(l.target, tt, "기타")
	if tt == "session" || tt == "route" || id == "-" || id == "" {
		return name
	}
	if m := regexp.MustCompile(`^(?:pr|po|doc):(\d+)$`).FindStringSubmatch(id); m != nil {
		return name + " #" + m[1]
	}
	if m := regexp.MustCompile(`^(role|department|user):(.+)$`).FindStringSubmatch(id); m != nil {
		kind := map[string]string{"role": "역할", "department": "부서", "user": "사용자"}[m[1]]
		return name + " " + kind + " " + l.or(l.value, m[2], m[2])
	}
	return name + " " + id
}

// ---- 감사 로그 ----

var auditExportColumns = []export.Column{
	{Label: "번호", Kind: export.KindInt}, {Label: "일시", Kind: export.KindDateTime}, {Label: "사용자", Kind: export.KindText},
	{Label: "경로", Kind: export.KindText}, {Label: "동작", Kind: export.KindText}, {Label: "대상", Kind: export.KindText},
	{Label: "결과", Kind: export.KindText}, {Label: "변경 내용", Kind: export.KindText},
}

type auditExportSource struct{ h *adminHandlers }

func (s *auditExportSource) Dataset() export.Dataset {
	return export.Dataset{Code: "audit_logs", Label: "감사 로그", Columns: auditExportColumns}
}

func (s *auditExportSource) Filters(q url.Values) []export.FilterItem {
	l := labels()
	tt := q.Get("targetType")
	return []export.FilterItem{
		export.Free("사용자", q.Get("actorName")),
		export.ID("사용자 번호", q.Get("actorId")),
		export.Code("동작", q.Get("action")),
		export.Free("대상 번호", q.Get("targetId")),
		export.Enum("경로", q.Get("channel"), l.channel),
		export.Enum("대상", tt, l.target),
		export.Enum("결과", q.Get("result"), l.result),
		export.Time("기간 시작", q.Get("from")),
		export.Time("기간 종료", q.Get("to")),
	}
}

// filter 는 화면 목록(listAuditLogs)과 같은 조건 매개변수를 감사 조회 조건으로 바꾼다. 맞는 사용자가 없으면 empty 가 참이다.
func (s *auditExportSource) filter(ctx context.Context, q url.Values) (f audit.Filter, empty bool, err error) {
	f = audit.Filter{
		Action: q.Get("action"), TargetType: audit.TargetType(q.Get("targetType")), TargetID: q.Get("targetId"),
		Result: audit.Result(q.Get("result")), Channel: contract.Channel(q.Get("channel")), Desc: true,
	}
	if f.Entities, err = s.h.auditEntityScope(ctx, q); err != nil {
		return f, false, err
	}
	if v := q.Get("actorId"); v != "" {
		if f.ActorID, err = strconv.ParseInt(v, 10, 64); err != nil {
			return f, false, contract.ErrValidation
		}
	}
	if f.From, err = parseTimeParam(q.Get("from")); err != nil {
		return f, false, contract.ErrValidation
	}
	if f.To, err = parseTimeParam(q.Get("to")); err != nil {
		return f, false, contract.ErrValidation
	}
	if name := strings.TrimSpace(q.Get("actorName")); name != "" {
		ids, err := s.h.actorIDsByName(ctx, name)
		if err != nil {
			return f, false, err
		}
		if len(ids) == 0 {
			return f, true, nil
		}
		f.ActorIDs = ids
	}
	return f, false, nil
}

func (s *auditExportSource) scan(ctx context.Context, f audit.Filter, limit int) ([]audit.Record, error) {
	f.Limit = limit
	var recs []audit.Record
	err := s.h.d.AuditScan.Scan(ctx, f, func(r audit.Record) error {
		recs = append(recs, r)
		return nil
	})
	return recs, err
}

func (s *auditExportSource) Count(ctx context.Context, q url.Values, max int) (int, bool, error) {
	f, empty, err := s.filter(ctx, q)
	if err != nil || empty {
		return 0, false, err
	}
	recs, err := s.scan(ctx, f, max+1)
	if err != nil {
		return 0, false, err
	}
	return len(recs), len(recs) > max, nil
}

func (s *auditExportSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	f, empty, err := s.filter(ctx, q)
	if err != nil || empty {
		return nil, err
	}
	recs, err := s.scan(ctx, f, max+1)
	if err != nil {
		return nil, err
	}
	// 이름 조회는 스캔이 끝난 뒤에 한다(DB 연결이 하나라 스캔 중 다른 쿼리를 하지 않는다).
	userNames, tokenLabels, _ := s.h.displayNames(ctx, recs)
	l := labels()
	rows := make([][]export.Cell, 0, len(recs))
	for _, r := range recs {
		var token string
		for _, d := range r.Diff {
			if d.Key == audit.DiffKeyTokenID {
				if id, err := strconv.ParseInt(d.New, 10, 64); err == nil {
					token = tokenLabels[id]
				}
			}
		}
		var parts []string
		for _, d := range r.Diff {
			parts = append(parts, l.diffText(d, token))
		}
		if det := strings.TrimSpace(r.Detail); det != "" && readable.MatchString(det) && det != l.or(l.action, r.Action, "") {
			parts = append(parts, "비고: "+det)
		}
		rows = append(rows, []export.Cell{
			r.Seq, r.At, auditActor(l, r, userNames[r.ActorID]), auditChannel(l, r, token),
			l.or(l.action, r.Action, "기타 작업"), l.targetText(string(r.TargetType), r.TargetID),
			l.or(l.result, string(r.Result), "기타"), strings.Join(parts, "; "),
		})
	}
	return rows, nil
}

func auditActor(l labelTables, r audit.Record, name string) string {
	if r.ActorID == 0 {
		return "시스템"
	}
	if name == "" {
		name = fmt.Sprintf("사용자 #%d", r.ActorID)
	}
	if role, ok := l.value[string(r.ActorRole)]; ok {
		return name + " (" + role + ")"
	}
	return name
}

func auditChannel(l labelTables, r audit.Record, token string) string {
	if r.ActorID == 0 && r.ActorRole == "system" {
		return "시스템"
	}
	c := l.or(l.channel, string(r.Channel), "기타")
	if r.Channel == contract.ChannelMCP && token != "" {
		return c + " · " + token
	}
	return c
}

// ---- 조직 구성원 ----

var orgPeopleColumns = []export.Column{
	{Label: "이름", Kind: export.KindText}, {Label: "직위", Kind: export.KindText}, {Label: "직책", Kind: export.KindText},
	{Label: "주소속", Kind: export.KindText}, {Label: "겸직", Kind: export.KindText},
	{Label: "이메일", Kind: export.KindText, Sensitive: true}, {Label: "휴대전화", Kind: export.KindText, Sensitive: true},
	{Label: "내선", Kind: export.KindText}, {Label: "상태", Kind: export.KindText},
}

type orgPeopleSource struct{ svc *org.Service }

func (s *orgPeopleSource) Dataset() export.Dataset {
	return export.Dataset{Code: "org_people", Label: "조직 구성원", Columns: orgPeopleColumns,
		Privacy: &export.PrivacySpec{Category: privacy.CatContact, Fields: []string{privacy.FieldEmail, privacy.FieldPhone}, ServiceNotes: true}}
}

func (s *orgPeopleSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{
		export.Date("기준일", q.Get("asOf")),
		export.ID("조직 번호", q.Get("unitId")),
		export.Bool("조직장만", q.Get("headOnly")),
		export.Bool("퇴사자 포함", q.Get("includeLeft")),
		export.Free("검색어", q.Get("q")),
	}
}

func (s *orgPeopleSource) query(q url.Values) (contract.PeopleQuery, error) {
	var unit int64
	if v := q.Get("unitId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return contract.PeopleQuery{}, contract.ErrValidation
		}
		unit = n
	}
	on := func(k string) bool { return q.Get(k) == "1" || q.Get(k) == "true" }
	return contract.PeopleQuery{Text: q.Get("q"), UnitID: unit, HeadOnly: on("headOnly"), IncludeLeft: on("includeLeft"), AsOf: contract.Day(q.Get("asOf"))}, nil
}

func (s *orgPeopleSource) Count(ctx context.Context, q url.Values, _ int) (int, bool, error) {
	pq, err := s.query(q)
	if err != nil {
		return 0, false, err
	}
	p, _ := contract.PrincipalFrom(ctx)
	pq.Limit = 1
	_, total, err := s.svc.ListPeople(ctx, p, pq, 1, true)
	return total, false, err
}

func (s *orgPeopleSource) Rows(ctx context.Context, q url.Values, raw bool, max int) ([][]export.Cell, error) {
	pq, err := s.query(q)
	if err != nil {
		return nil, err
	}
	p, _ := contract.PrincipalFrom(ctx)
	pq.Limit = max + 1
	cards, _, err := s.svc.ListPeople(ctx, p, pq, max+1, !raw) // 원문 포함이 아니면 권한자·본인에게도 연락처를 가린다
	if err != nil {
		return nil, err
	}
	rows := make([][]export.Cell, 0, len(cards))
	for _, c := range cards {
		status := "재직"
		if !c.Active {
			status = "퇴사"
		}
		conc := make([]string, 0, len(c.Concurrent))
		for _, x := range c.Concurrent {
			conc = append(conc, x.Unit.Name)
		}
		rows = append(rows, []export.Cell{
			c.Name, c.Rank, orgTitles(c), contract.OrgPathText(c.Primary.Path), strings.Join(conc, ", "),
			c.Email, c.Phone, c.Extension, status,
		})
	}
	return rows, nil
}

// orgTitles 는 화면의 직책 칸과 같다: 조직장인 조직은 "구매1팀 팀장", 직무대행은 "구매1팀 직무대행".
func orgTitles(c contract.PersonCard) string {
	var t []string
	for _, pl := range append([]contract.OrgPlace{c.Primary}, c.Concurrent...) {
		if pl.IsHead {
			t = append(t, pl.Unit.Name+" "+contract.HeadTitle(pl.Unit.Kind))
		}
	}
	for _, pl := range c.Acting {
		t = append(t, pl.Unit.Name+" 직무대행")
	}
	return strings.Join(t, " · ")
}
