package auth

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 법인 불일치 접근의 감사(B44 AC-4411, 명세 10절). 응답은 그대로 두고(404 는 존재를 숨긴다) 감사 access.denied 만 남긴다.
// 감사 diff 에는 상대 법인의 내부 정보가 없다: 요청한 법인 코드(또는 요청 법인), 대상 종류, 이유뿐이다.
//
// 잡는 경로(명세 10절에 같은 목록이 있다)
//  1. 현재 법인 지정 거부: X-Entity-ID 헤더(고를 수 없는 법인·모르는 코드·권한 없는 "*"), PUT /api/me/entity.
//  2. contract.RequireIn(다른 법인 레코드)의 404.
//  3. 레코드 번호를 경로에 쓰는 상세·상태 변경 라우트(entityRoutes)가 404 를 돌려줬는데 그 레코드가 다른 법인에 실제로 있을 때.
// 목록·집계처럼 저장소 WHERE 로 걸러져 "없음"이 되는 읽기는 다른 법인 레코드를 가리킨 것이 아니므로 기록하지 않는다.

// deniedState 는 한 요청에서 법인 불일치 감사를 한 번만 남기려는 표시다(RequireIn 훅과 404 점검이 겹치지 않게).
type deniedState struct {
	recorded bool
}

type deniedStateKey struct{}

// entityRoute 는 레코드 번호를 경로에 쓰는 라우트와 그 레코드의 표다(법인 열이 있는 표만).
type entityRoute struct {
	prefix string // 경로 접두(끝 슬래시 없음)
	table  string
	col    string
	kind   string // 감사에 적는 대상 종류(한국어)
}

var entityRoutes = []entityRoute{
	{"/api/purchase-requests/", "purchase_requests", "id", "구매요청"},
	{"/api/purchase-orders/", "purchase_orders", "id", "발주서"},
	{"/api/receipts/", "receipts", "id", "입고"},
	{"/api/warehouses/", "warehouses", "id", "창고"},
	{"/api/finance/journals/", "journals", "no", "전표"},
	{"/api/finance/payments/", "payment_requests", "no", "지급 요청"},
	{"/api/finance/tax-invoices/", "tax_invoices", "id", "세금계산서"},
	{"/api/attendance/leave/requests/", "leave_requests", "id", "휴가 신청"},
	{"/api/attendance/corrections/", "attendance_corrections", "id", "출퇴근 정정"},
}

var entityCodeRe = regexp.MustCompile(`^[A-Za-z0-9]{1,12}$`)

// matchEntityRoute 는 경로가 번호 라우트면 그 라우트와 경로의 번호 조각을 돌려준다.
func matchEntityRoute(path string) (entityRoute, string, bool) {
	for _, er := range entityRoutes {
		if strings.HasPrefix(path, er.prefix) {
			rest := strings.TrimPrefix(path, er.prefix)
			if i := strings.Index(rest, "/"); i >= 0 {
				rest = rest[:i]
			}
			if rest != "" {
				return er, rest, true
			}
		}
	}
	return entityRoute{}, "", false
}

// statusWriter 는 응답 상태 코드를 본다(번호 라우트에서만 감싼다).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// recordEntityDenied 는 법인 불일치 거부를 access.denied 로 남긴다. entityID 는 요청한 쪽의 법인(없으면 audit.NoEntity)이다.
func (s *Service) recordEntityDenied(r *http.Request, p contract.Principal, entityID int64, requested, kind, reason string) {
	if s.audit == nil || s.txs == nil {
		return
	}
	target := r.Pattern
	if target == "" {
		target = r.Method + " " + r.URL.Path
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	diff := []audit.DiffItem{{Key: "denied_kind", New: kind}, {Key: "denied_reason", New: reason}}
	if requested != "" {
		diff = append([]audit.DiffItem{{Key: "entity_requested", New: requested}}, diff...)
	}
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "access.denied", TargetType: audit.TargetRoute, TargetID: target,
			Result: audit.ResultDenied, Diff: diff, Detail: "법인 불일치", EntityID: entityID})
	})
	if err != nil {
		log.Printf("auth: 법인 불일치 거부 기록 실패: %v", err)
	}
}

// entityCodeForAudit 는 헤더에 온 법인 코드를 감사에 적을 수 있는 값으로 바꾼다(형식이 다르면 값을 적지 않는다).
func entityCodeForAudit(h string) string {
	h = strings.TrimSpace(h)
	if h == "*" {
		return "*"
	}
	if entityCodeRe.MatchString(h) {
		return strings.ToUpper(h)
	}
	return "(형식 오류)"
}

// denyHeaderEntity 는 현재 법인 지정(헤더)이 거부됐을 때의 감사다.
func (s *Service) denyHeaderEntity(r *http.Request, p contract.Principal) {
	h := r.Header.Get(entityHeader)
	reason := "고를 수 없는 법인"
	if strings.TrimSpace(h) == "*" {
		reason = "전체 법인 조회 권한 없음"
	}
	s.recordEntityDenied(r, p, audit.NoEntity, entityCodeForAudit(h), "현재 법인 지정", reason)
}

// withEntityAudit 는 요청 context 에 RequireIn 훅과 중복 방지 표시를 싣는다.
func (s *Service) withEntityAudit(r *http.Request, p contract.Principal, a contract.ActiveEntity) (*http.Request, *deniedState) {
	st := &deniedState{}
	ctx := context.WithValue(r.Context(), deniedStateKey{}, st)
	var reqEntity int64 = audit.NoEntity
	if !a.All && a.ID > 0 {
		reqEntity = a.ID
	}
	ctx = contract.WithEntityDeniedHook(ctx, func(_ context.Context, perm contract.Permission, _ int64) {
		if st.recorded {
			return
		}
		st.recorded = true
		s.recordEntityDenied(r, p, reqEntity, s.entityCodeOf(r.Context(), a), "권한: "+string(perm), "다른 법인의 레코드")
	})
	return r.WithContext(ctx), st
}

// entityCodeOf 는 현재 법인의 코드다(전체 모드·모르면 빈 값).
func (s *Service) entityCodeOf(ctx context.Context, a contract.ActiveEntity) string {
	if a.All || a.ID <= 0 {
		return ""
	}
	var code string
	if err := s.db.QueryRowContext(ctx, `SELECT code FROM entities WHERE id = ?`, a.ID).Scan(&code); err != nil {
		return ""
	}
	return code
}

// auditEntityMiss 는 번호 라우트가 404 를 돌려줬을 때 그 레코드가 다른 법인에 실제로 있는지 보고 있으면 access.denied 를 남긴다.
// 레코드가 아예 없거나(진짜 404) 같은 법인이면 기록하지 않는다. 표·열 이름은 entityRoutes 의 상수다.
func (s *Service) auditEntityMiss(r *http.Request, p contract.Principal, a contract.ActiveEntity, st *deniedState, er entityRoute, idPart string) {
	if st != nil && st.recorded || a.All || a.ID <= 0 {
		return
	}
	var arg any = idPart
	if er.col == "id" {
		n, err := strconv.ParseInt(idPart, 10, 64)
		if err != nil || n <= 0 {
			return
		}
		arg = n
	}
	var owner int64
	err := s.db.QueryRowContext(context.WithoutCancel(r.Context()), `SELECT entity_id FROM `+er.table+` WHERE `+er.col+` = ?`, arg).Scan(&owner)
	if err == sql.ErrNoRows || err != nil || owner == a.ID {
		return
	}
	if st != nil {
		st.recorded = true
	}
	s.recordEntityDenied(r, p, a.ID, s.entityCodeOf(r.Context(), a), er.kind, "다른 법인의 레코드")
}
