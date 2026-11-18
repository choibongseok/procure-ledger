// (internal/handler/llm*).
package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/mcpusage"
)

const (
	defaultUsageDays = 7
	maxUsageDays     = 366
	usageDateLayout  = "2006-01-02"
)

type mcpUsageHandler struct {
	svc *mcpusage.Service
	now func() time.Time
}

func registerMCPUsage(r contract.Registrar, d deps.Deps) {
	h := &mcpUsageHandler{now: time.Now}
	if d.AuditScan != nil {
		h.svc = &mcpusage.Service{Scan: d.AuditScan, Tokens: d.TokenLookup, DB: d.DB}
		if d.MCPRate != nil {
			h.svc.RatePerMin = d.MCPRate.Limit
			if u, ok := d.MCPRate.(interface{ UserLimit() int }); ok {
				h.svc.RatePerUserPerMin = u.UserLimit
			}
		}
	}
	r.HandleFunc("GET", "/api/mcp/usage", contract.PermUsageRead, h.usage)
}

// parseUsagePeriod 는 from·to(YYYY-MM-DD, 서버 시간대)를 [시작, 끝) 로 바꾼다. to 날짜는 그날 끝까지 포함한다.
// 둘 다 없으면 오늘을 포함한 최근 7일이다. 한쪽만 있으면 나머지를 기본 기간으로 채운다.
func parseUsagePeriod(fromS, toS string, now time.Time) (time.Time, time.Time, error) {
	day := func(s string) (time.Time, error) {
		t, err := time.ParseInLocation(usageDateLayout, s, now.Location())
		if err != nil {
			return t, fmt.Errorf("%w: 날짜는 연-월-일(예 2026-12-18) 형식으로 입력하세요", contract.ErrValidation)
		}
		return t, nil
	}
	var from, to time.Time
	var err error
	if toS != "" {
		if to, err = day(toS); err != nil {
			return from, to, err
		}
		to = to.AddDate(0, 0, 1)
	} else {
		y, m, d := now.Date()
		to = time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
	}
	if fromS != "" {
		if from, err = day(fromS); err != nil {
			return from, to, err
		}
	} else {
		from = to.AddDate(0, 0, -defaultUsageDays)
	}
	if !from.Before(to) {
		return from, to, fmt.Errorf("%w: 시작일이 종료일보다 늦습니다", contract.ErrValidation)
	}
	if to.Sub(from) > maxUsageDays*24*time.Hour {
		return from, to, fmt.Errorf("%w: 조회 기간은 최대 %d일입니다", contract.ErrValidation, maxUsageDays)
	}
	return from, to, nil
}

func (h *mcpUsageHandler) usage(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		aiWriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "사용 현황을 조회할 수 없습니다"})
		return
	}
	q := r.URL.Query()
	from, to, err := parseUsagePeriod(q.Get("from"), q.Get("to"), h.now())
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	rep, err := h.svc.Report(r.Context(), from, to)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	aiWriteJSON(w, http.StatusOK, map[string]any{
		"from": from.Format(usageDateLayout), "to": to.AddDate(0, 0, -1).Format(usageDateLayout),
		"ratePerMin": rep.RatePerMin, "ratePerUserPerMin": rep.RatePerUserPerMin, "total": rep.Total, "tools": rep.Tools, "tokens": rep.Tokens,
	})
}
