package export

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 기본 상한과 허용 범위(명세 3.5, 환경변수 PL_EXPORT_MAX_ROWS).
const (
	DefaultMaxRows = 10000
	MinMaxRows     = 100
	MaxMaxRows     = 100000
)

// Source 는 한 데이터셋의 내보내기 원천이다. 소유 영역이 구현하고 **화면 목록이 부르는 같은 서비스 함수**를 필터 그대로,
// 쪽 나누기 없이 부른다(열람 범위·부서 격리·마스킹은 서비스 안에서 적용된다). 저장소를 직접 부르거나 전체 범위 조회를 쓰지 않는다.
type Source interface {
	Dataset() Dataset
	// Filters 는 요청의 조건을 감사·정보 시트에 남길 항목으로 바꾼다. 자유 입력(검색어·이름)은 Free 로 표시한다.
	Filters(q url.Values) []FilterItem
	// Count 는 조건에 맞는 행 수다(파일을 만들기 전 상한 확인용). 전체를 세기 비싼 원천은 max+1 까지만 세고 more 를 참으로 돌려줄 수 있다
	// ("N건을 넘는다" 문구가 된다). 조건이 올바르지 않으면 contract.ErrValidation 을 돌려준다.
	Count(ctx context.Context, q url.Values, max int) (n int, more bool, err error)
	// Rows 는 행이다. raw 가 거짓이면 **호출자가 mask:bypass 권한자여도** 민감 열을 가린 값으로 준다.
	// raw 가 참이어도 원문을 주는지는 서비스의 마스킹 규칙이 정한다(핸들러가 권한을 이미 다시 판정했다).
	// max 보다 많은 행을 만들 상황이면 max+1 행까지만 읽어도 된다(핸들러가 상한 초과를 판정한다).
	Rows(ctx context.Context, q url.Values, raw bool, max int) ([][]Cell, error)
}

// ScopedDataset 은 법인 범위(entity 매개변수)에 따라 열이 달라지는 원천이 선택으로 구현한다. 구현하지 않으면 Dataset() 하나가 항상 쓰인다.
type ScopedDataset interface {
	DatasetFor(ctx context.Context) Dataset
}

// Env 는 내보내기 핸들러가 쓰는 공용 의존성이다.
type Env struct {
	Txs     contract.TxManager
	Audit   audit.Recorder
	MaxRows int // 1회 상한(PL_EXPORT_MAX_ROWS). 0 이면 DefaultMaxRows
	Limiter *Limiter
	Now     func() time.Time
	// Privacy 는 원문 포함 내보내기를 개인정보 조회 기록에 남긴다(B41). 원문 포함 요청은 Privacy 가 없거나 데이터 세트가 Privacy 명세를
	// 선언하지 않았으면 기록할 수 없으므로 거부한다(fail-closed).
	Privacy privacy.Noter
}

// SubjectSource 는 원문 포함으로 내보낼 행들의 정보주체를 알려 주는 선택 구현이다(Source 가 함께 구현한다). 구현하지 않으면
// 정보주체를 식별하지 못한 건수(행 수)로만 기록한다.
type SubjectSource interface {
	Subjects(ctx context.Context, q url.Values, max int) ([]privacy.Subject, error)
}

// NewEnv 는 상한·속도 제한기를 갖춘 Env 를 만든다.
func NewEnv(txs contract.TxManager, rec audit.Recorder, maxRows int) *Env {
	if maxRows <= 0 {
		maxRows = DefaultMaxRows
	}
	return &Env{Txs: txs, Audit: rec, MaxRows: maxRows, Limiter: NewLimiter(nil), Now: time.Now}
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// ParseMaxRows 는 환경변수 값(문자열)을 상한으로 읽는다. 비어 있으면 기본값, 숫자가 아니거나 범위 밖이면 -1 이다(조립 단계가 기동을 거부한다).
func ParseMaxRows(s string) int {
	if s == "" {
		return DefaultMaxRows
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < MinMaxRows || n > MaxMaxRows {
		return -1
	}
	return n
}

// Register 는 `GET <path>?format=csv|xlsx&<화면 필터>&raw=1` 라우트를 등록한다. **권한이 필수 인자**라 선언 없는 내보내기 라우트는
// 존재할 수 없고 권한 선언 누락 검사가 그대로 걸린다. 핸들러 안에 역할 분기는 없다(원문 포함만 mask:bypass 를 한 번 더 판정한다).
func Register(r contract.Registrar, path string, perm contract.Permission, env *Env, src Source) {
	r.HandleFunc("GET", path, perm, env.handler(src, perm))
}

const (
	msgForbidden = "이 자료를 내보낼 권한이 없습니다. 필요하면 관리자에게 권한을 요청하세요."
	msgFormat    = "내보내기 형식은 엑셀(XLSX) 또는 CSV 만 고를 수 있습니다."
	msgFilter    = "조건이 올바르지 않습니다. 화면의 조건을 확인한 뒤 다시 내보내세요."
	msgFail      = "파일을 만들지 못했습니다. 조건을 좁혀 다시 시도하고, 계속되면 관리자에게 알려 주세요."
	msgAuditFail = "파일을 만들지 못했습니다. 잠시 뒤 다시 시도하세요."
)

func limitMessage(n int, more bool, max int) string {
	if more { // 건수를 끝까지 세지 않은 원천: 실제 건수를 모르므로 상한만 알린다
		return fmt.Sprintf("조건에 맞는 자료가 한 번에 내보낼 수 있는 상한(%s건)을 넘습니다. 기간이나 조건을 좁혀 다시 내보내세요.", comma(max))
	}
	return fmt.Sprintf("조건에 맞는 자료가 %s건이라 한 번에 내보낼 수 없습니다(최대 %s건). 기간이나 조건을 좁혀 다시 내보내세요.", comma(n), comma(max))
}

func comma(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (e *Env) handler(src Source, perm contract.Permission) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ds := src.Dataset()
		p, ok := contract.PrincipalFrom(r.Context())
		if !ok {
			http.Error(w, contract.ErrUnauthenticated.Error(), http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		format := q.Get("format")
		if format != "csv" && format != "xlsx" {
			http.Error(w, msgFormat, http.StatusBadRequest)
			return
		}
		raw := q.Get("raw") == "1"
		items := src.Filters(q)
		// 법인 필터(B44 12절): 선택 매개변수 entity(법인 코드, "*" 는 전체). 호출자가 이 권한을 가진 법인 범위 안에서만 고를 수 있고, 파일 정보 시트에 법인이 적힌다.
		ectx, label, status, emsg := e.entityContext(r.Context(), p, perm, q.Get("entity"))
		if label != "" {
			items = append(items, Free("법인", label))
		}
		filters := FilterText(items)
		if status == 0 {
			r = r.WithContext(ectx)
			// 법인 필터에 따라 열이 달라지는 원천(재고 평가의 전체 모드는 법인 열이 붙는다)은 법인 context 로 파일 정의를 다시 받는다.
			if sd, ok := src.(ScopedDataset); ok {
				ds = sd.DatasetFor(r.Context())
			}
		}

		entry := func(result audit.Result, rows int, detail string, extra ...audit.DiffItem) audit.Entry {
			// 성공이면 내보낸 건수, 아니면(상한 초과·거부) 조건에 맞는 건수다(파일은 만들어지지 않았다).
			rowsKey := "matched_rows"
			if result == audit.ResultSuccess {
				rowsKey = "exported_rows"
			}
			diff := []audit.DiffItem{
				{Key: "dataset", New: ds.Label}, {Key: "format", New: format}, {Key: rowsKey, New: strconv.Itoa(rows)},
				{Key: "columns", New: strconv.Itoa(len(ds.Columns))}, {Key: "filters", New: filters},
				{Key: "raw_sensitive", New: yesNo(raw && ds.HasSensitive() && result == audit.ResultSuccess)},
			}
			diff = append(diff, extra...)
			return audit.Entry{Actor: p, Action: "export.run", TargetType: audit.TargetExport, TargetID: ds.Code, Result: result, Diff: diff, Detail: detail}
		}
		record := func(en audit.Entry, notes ...privacy.Access) error {
			return e.Txs.WithTx(r.Context(), func(tx contract.Tx) error {
				if err := e.Audit.Record(r.Context(), tx, en); err != nil {
					return err
				}
				for _, a := range notes { // 감사와 같은 트랜잭션: 둘 중 하나라도 실패하면 파일을 보내지 않는다
					if err := e.Privacy.NoteTx(r.Context(), tx, p, a); err != nil {
						return err
					}
				}
				return nil
			})
		}
		deny := func(status int, msg string, en audit.Entry) {
			if err := record(en); err != nil {
				log.Printf("export: 거부 감사 기록 실패: %v", err)
			}
			http.Error(w, msg, status)
		}

		if status != 0 {
			deny(status, emsg, entry(audit.ResultDenied, 0, "법인 범위 밖 또는 알 수 없는 법인"))
			return
		}
		// 허용 형식 밖의 조건 값은 파일을 만들기 전에 거부한다. 감사에는 값 대신 "잘못된 값"만 남는다(FilterText).
		if bad := CheckFilters(items); len(bad) > 0 {
			deny(http.StatusBadRequest, msgFilter, entry(audit.ResultError, 0, "조건 오류: "+strings.Join(bad, ", ")))
			return
		}
		// 원문 포함은 mask:bypass 를 서버가 다시 판정한다(클라이언트 값을 믿지 않는다).
		if raw && ds.HasSensitive() && !p.Can(contract.PermMaskBypass) {
			deny(http.StatusForbidden, msgForbidden, entry(audit.ResultDenied, 0, "원문 포함 권한 없음"))
			return
		}
		if raw && ds.HasSensitive() && (ds.Privacy == nil || (!ds.Privacy.ServiceNotes && e.Privacy == nil)) { // 원문을 기록할 수 없으면 내보내지 않는다
			deny(http.StatusInternalServerError, msgAuditFail, entry(audit.ResultError, 0, "원문 열람 기록 경로 없음"))
			return
		}
		release, err := e.Limiter.Acquire(p.UserID)
		if err != nil {
			deny(http.StatusTooManyRequests, err.Error(), entry(audit.ResultDenied, 0, "속도 제한"))
			return
		}
		defer release()

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		max := e.MaxRows
		if max <= 0 {
			max = DefaultMaxRows
		}
		n, more, err := src.Count(ctx, q, max)
		if err != nil {
			e.sourceError(w, err, deny, entry)
			return
		}
		if n > max || more {
			deny(http.StatusBadRequest, limitMessage(n, more, max), entry(audit.ResultError, n, "상한 초과", audit.DiffItem{Key: "limit", New: strconv.Itoa(max)}))
			return
		}
		rows, err := src.Rows(ctx, q, raw && ds.HasSensitive(), max)
		if err != nil {
			e.sourceError(w, err, deny, entry)
			return
		}
		if len(rows) > max { // 건수 확인 뒤 데이터가 늘어난 경우(E7)
			deny(http.StatusBadRequest, limitMessage(len(rows), false, max), entry(audit.ResultError, len(rows), "상한 초과", audit.DiffItem{Key: "limit", New: strconv.Itoa(max)}))
			return
		}
		at := e.now()
		table := Table{Dataset: ds, Rows: rows}
		var body []byte
		var ctype, ext string
		if format == "csv" {
			body, err = WriteCSV(table)
			ctype, ext = "text/csv; charset=utf-8", "csv"
		} else {
			body, err = WriteXLSX(table, Meta{At: at, By: p.Name, Filters: filters, Raw: raw && ds.HasSensitive()})
			ctype, ext = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"
		}
		if err != nil {
			log.Printf("export: 파일 생성 실패(%s): %v", ds.Code, err)
			deny(http.StatusInternalServerError, msgFail, entry(audit.ResultError, len(rows), "파일 생성 실패"))
			return
		}
		// 감사 기록이 실패하면 파일을 보내지 않는다(쓰기 감사와 같은 원칙). 원문 포함이면 조회 기록도 같은 트랜잭션에서 남긴다.
		var notes []privacy.Access
		if raw && ds.HasSensitive() && !ds.Privacy.ServiceNotes {
			a := privacy.Access{Category: ds.Privacy.Category, Screen: "내보내기 " + ds.Label, Dataset: ds.Code, Export: true, Fields: ds.Privacy.Fields}
			if ss, ok := src.(SubjectSource); ok {
				subs, serr := ss.Subjects(ctx, q, max)
				if serr != nil {
					e.sourceError(w, serr, deny, entry)
					return
				}
				a.Subjects = privacy.ExcludeSelf(p, subs)
				if extra := len(rows) - len(subs); extra > 0 {
					a.UnknownSubjects = extra
				}
			} else {
				a.UnknownSubjects = len(rows)
			}
			notes = append(notes, a)
		}
		if err := record(entry(audit.ResultSuccess, len(rows), ""), notes...); err != nil {
			log.Printf("export: 감사 기록 실패(%s): %v", ds.Code, err)
			http.Error(w, msgAuditFail, http.StatusInternalServerError)
			return
		}
		name := FileName(ds.Label, at, ext)
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="export.%s"; filename*=UTF-8''%s`, ext, url.PathEscape(name)))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}
}

// sourceError 는 Source 오류를 상태 코드와 문구로 옮긴다(알 수 없는 오류는 내용을 숨긴다).
func (e *Env) sourceError(w http.ResponseWriter, err error, deny func(int, string, audit.Entry), entry func(audit.Result, int, string, ...audit.DiffItem) audit.Entry) {
	switch {
	case errors.Is(err, contract.ErrValidation), errors.Is(err, contract.ErrNotFound):
		deny(http.StatusBadRequest, msgFilter, entry(audit.ResultError, 0, "조건 오류"))
	case errors.Is(err, contract.ErrForbidden):
		deny(http.StatusForbidden, msgForbidden, entry(audit.ResultDenied, 0, "권한 없음"))
	case errors.Is(err, context.DeadlineExceeded):
		deny(http.StatusInternalServerError, msgFail, entry(audit.ResultError, 0, "시간 초과"))
	default:
		log.Printf("export: 원천 오류: %v", err)
		deny(http.StatusInternalServerError, msgFail, entry(audit.ResultError, 0, "원천 오류"))
	}
}

func yesNo(b bool) string {
	if b {
		return "예"
	}
	return "아니오"
}

// entityContext 는 요청의 법인 필터를 풀어 원천이 쓸 context 를 만든다. 비어 있으면 요청의 현재 법인을 그대로 쓰고(법인이 둘 이상인 설치에서만 정보 시트에 법인을 적는다),
// "*" 는 이 권한이 그룹 범위로 효력인 사람만, 코드는 이 권한이 그 법인에서 효력인 사람만 쓸 수 있다. status 가 0 이 아니면 거부다.
func (e *Env) entityContext(ctx context.Context, p contract.Principal, perm contract.Permission, raw string) (out context.Context, label string, status int, msg string) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	type ent struct {
		id         int64
		code, name string
		active     bool
	}
	if _, has := contract.ActiveEntityFrom(ctx); code == "" && !has {
		return ctx, "", 0, "" // 현재 법인이 없는 호출(서버 내부 작업·시험)은 법인을 따지지 않는다
	}
	var all []ent
	err := e.Txs.WithTx(ctx, func(tx contract.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, code, name, active FROM entities ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x ent
			var a int
			if err := rows.Scan(&x.id, &x.code, &x.name, &a); err != nil {
				return err
			}
			x.active = a == 1
			all = append(all, x)
		}
		return rows.Err()
	})
	if err != nil {
		log.Printf("export: 법인 조회 실패: %v", err)
		return ctx, "", http.StatusInternalServerError, msgFail
	}
	active := 0
	for _, x := range all {
		if x.active {
			active++
		}
	}
	switch code {
	case "":
		if a, ok := contract.ActiveEntityFrom(ctx); ok && active > 1 {
			if a.All {
				return ctx, "전체 법인", 0, ""
			}
			for _, x := range all {
				if x.id == a.ID {
					return ctx, x.name, 0, ""
				}
			}
		}
		return ctx, "", 0, ""
	case "*":
		if !p.EntitiesFor(perm).All {
			return ctx, "", http.StatusForbidden, msgForbidden
		}
		return contract.WithActiveEntity(ctx, contract.ActiveEntity{All: true}), "전체 법인", 0, ""
	}
	for _, x := range all {
		if x.code == code {
			if !p.EntitiesFor(perm).Has(x.id) {
				return ctx, "", http.StatusForbidden, msgForbidden
			}
			return contract.WithActiveEntity(ctx, contract.ActiveEntity{ID: x.id}), x.name, 0, ""
		}
	}
	return ctx, "", http.StatusBadRequest, msgFilter
}
