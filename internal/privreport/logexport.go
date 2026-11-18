package privreport

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 조회 기록 목록 내보내기(명세 41 5.5): `GET /api/admin/privacy/log/export?format=csv|xlsx&<화면 조건>`.
// 권한은 조회 기록 열람(privacy:log_read)과 같고, 내보내기 공통 부품(상한·속도 제한·감사 export.run)이 그대로 적용된다.
// 값(연락처·계좌 원문·검색어)은 조회 기록에 없으므로 파일에도 없다. 정보주체는 이름 없이 인원수만 싣는다.

var fieldLabel = map[string]string{"email": "이메일", "phone": "휴대전화", "account_no": "계좌번호", "business_no": "사업자번호", "card_txn": "카드 내역", "employee_no": "사번", "leave_reason": "휴가 사유", "absence_reason": "휴직 사유", "proof_file": "증빙 파일"}
var channelLabel = map[string]string{"web": "웹", "mcp": "AI 도구", "cli": "서버 명령", "scim": "계정 동기화", "llmgw": "AI 호출", "api": "API 키"}

// RegisterExport 는 조회 기록 목록 내보내기 라우트를 등록한다.
func (s *Service) RegisterExport(reg contract.Registrar, env *export.Env) {
	export.Register(reg, "/api/admin/privacy/log/export", contract.PermPrivacyLogRead, env, &logSource{priv: s.priv})
}

type logSource struct{ priv *privacy.Recorder }

var logColumns = []export.Column{
	{Label: "시각", Kind: export.KindDateTime}, {Label: "열람자"}, {Label: "열람자 부서"}, {Label: "열람자 역할"}, {Label: "경로"}, {Label: "범주"}, {Label: "화면"},
	{Label: "항목"}, {Label: "내보내기", Kind: export.KindBool}, {Label: "정보주체 수", Kind: export.KindInt}, {Label: "업무시간 밖", Kind: export.KindBool}, {Label: "이상 표시"},
}

func (logSource) Dataset() export.Dataset {
	return export.Dataset{Code: "privacy_log", Label: "개인정보 조회 기록", Columns: logColumns}
}

func (logSource) Filters(q url.Values) []export.FilterItem {
	return []export.FilterItem{
		export.Time("기간 시작", q.Get("from")), export.Time("기간 종료", q.Get("to")),
		export.ID("열람자 번호", q.Get("actorId")), export.Free("열람자 이름", q.Get("actor")),
		export.Enum("범주", q.Get("category"), categoryLabel), export.Free("화면", q.Get("screen")), export.Free("정보주체 이름", q.Get("subject")),
		export.Bool("내보내기만", q.Get("export")), export.Bool("AI 도구 이상만", q.Get("anomaly")),
	}
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", s, kst)
}

func (logSource) filter(q url.Values) (privacy.Filter, error) {
	var f privacy.Filter
	var err error
	if f.From, err = parseTime(q.Get("from")); err != nil {
		return f, contract.ErrValidation
	}
	if f.To, err = parseTime(q.Get("to")); err != nil {
		return f, contract.ErrValidation
	}
	f.ActorID, _ = strconv.ParseInt(q.Get("actorId"), 10, 64)
	f.ActorName, f.Category, f.Screen, f.SubjectName = q.Get("actor"), q.Get("category"), q.Get("screen"), q.Get("subject")
	f.ExportOnly, f.AnomalyOnly = q.Get("export") == "1", q.Get("anomaly") == "1"
	return f, nil
}

func (s *logSource) entries(ctx context.Context, q url.Values, max int, record bool) ([]privacy.Entry, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return nil, contract.ErrUnauthenticated
	}
	f, err := s.filter(q)
	if err != nil {
		return nil, err
	}
	return s.priv.ExportEntries(ctx, p, f, max+1, record)
}

func (s *logSource) Count(ctx context.Context, q url.Values, max int) (int, bool, error) {
	es, err := s.entries(ctx, q, max, false)
	if err != nil {
		return 0, false, err
	}
	return len(es), len(es) > max, nil
}

func (s *logSource) Rows(ctx context.Context, q url.Values, _ bool, max int) ([][]export.Cell, error) {
	es, err := s.entries(ctx, q, max, true) // 파일에 실을 행을 읽는 순간이 조회 기록을 읽는 일이다(감사 privacy.log_read)
	if err != nil {
		return nil, err
	}
	rows := make([][]export.Cell, 0, len(es))
	for _, e := range es {
		fields := make([]string, 0, len(e.Fields))
		for _, f := range e.Fields {
			if l, ok := fieldLabel[f]; ok {
				f = l
			}
			fields = append(fields, f)
		}
		ch := channelLabel[e.Channel]
		if ch == "" {
			ch = e.Channel
		}
		anomaly := ""
		if e.Exposure == "mcp_anomaly" {
			anomaly = "AI 도구 원문 유출"
		}
		at, _ := time.Parse("2006-01-02T15:04:05.000000000Z", e.At)
		rows = append(rows, []export.Cell{at, e.ActorName, e.ActorUnit, e.ActorRole, ch, categoryLabel[e.Category], e.Screen, strings.Join(fields, ", "), e.IsExport, e.SubjectCount, e.OffHours, anomaly})
	}
	return rows, nil
}
