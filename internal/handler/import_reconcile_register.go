// 기초 데이터 대조 보고서 라우트와 내보내기(B35 3.9). 권한 import:read.
package handler

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/export"
)

// RegisterImportReconcile 는 /api/imports/reconcile 과 내보내기를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterImportReconcile(r contract.Registrar, d deps.Deps) {
	if d.ImportReconcile == nil {
		return
	}
	svc := d.ImportReconcile
	r.HandleFunc("GET", "/api/imports/reconcile", contract.PermImportRead, func(w http.ResponseWriter, req *http.Request) {
		rep, err := svc.Report(req.Context(), reconcileInput(req.URL.Query()))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, reconcileDTO(rep))
	})
	if d.Export != nil {
		export.Register(r, "/api/imports/reconcile/export", contract.PermImportRead, d.Export, reconcileExport(svc))
	}
}

// reconcileInput 은 쿼리의 원 시스템 수치를 읽는다(비었거나 숫자가 아니면 입력하지 않은 것으로 본다).
func reconcileInput(q url.Values) domain.ReconcileInput {
	in := domain.ReconcileInput{Original: map[string]int64{}}
	for _, code := range domain.ReconcileCodes {
		if v := q.Get(code); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				in.Original[code] = n
			}
		}
	}
	return in
}

var reconcileVerdictLabel = map[string]string{"match": "일치", "diff": "차이", "": ""}

func reconcileDTO(rep domain.ReconcileReport) map[string]any {
	rows := make([]map[string]any, len(rep.Rows))
	for i, x := range rep.Rows {
		rows[i] = map[string]any{"code": x.Code, "label": x.Label, "original": ptrOrNil(x.Original), "system": x.System, "diff": ptrOrNil(x.Diff),
			"verdict": x.Verdict, "verdictLabel": reconcileVerdictLabel[x.Verdict], "notes": append([]string{}, x.Notes...)}
	}
	return map[string]any{"rows": rows, "pendingOpeningCount": rep.PendingOpeningCount}
}

func reconcileExport(svc domain.ImportReconcileService) *listSource {
	return &listSource{
		ds: export.Dataset{Code: "import_reconcile", Label: "기초 데이터 대조", Columns: []export.Column{
			{Label: "항목"}, {Label: "원 시스템", Kind: export.KindInt}, {Label: "새 시스템", Kind: export.KindInt}, {Label: "차이", Kind: export.KindInt}, {Label: "판정"}, {Label: "참고"}}},
		filters: func(url.Values) []export.FilterItem { return nil },
		fetch: func(ctx context.Context, q url.Values, _ int) ([][]export.Cell, error) {
			rep, err := svc.Report(ctx, reconcileInput(q))
			if err != nil {
				return nil, err
			}
			out := make([][]export.Cell, 0, len(rep.Rows))
			for _, x := range rep.Rows {
				note := ""
				for i, n := range x.Notes {
					if i > 0 {
						note += " "
					}
					note += n
				}
				out = append(out, []export.Cell{x.Label, ptrOrNil(x.Original), x.System, ptrOrNil(x.Diff), reconcileVerdictLabel[x.Verdict], note})
			}
			return out, nil
		},
	}
}
