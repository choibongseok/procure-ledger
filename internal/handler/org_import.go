// (internal/handler/org*). 조직 CSV 일괄 반영 API(B22). 6.9절.
package handler

import (
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
)

const (
	importBodyMax    = 3*org.ImportMaxFileBytes + 1<<20 // 파일 3개 + 여유
	importItemsShown = 500                              // 미리보기 이벤트 목록 표시 상한(넘으면 "외 N건" + 전체 계획 내려받기)
)

func (h *orgHandlers) registerImport(r contract.Registrar) {
	r.HandleFunc("POST", "/api/org/import/preview", contract.PermOrgManage, h.importPreview)
	r.HandleFunc("POST", "/api/org/import/apply", contract.PermOrgManage, h.importApply)
	r.HandleFunc("POST", "/api/org/import/errors.csv", contract.PermOrgManage, h.importErrorsCSV)
	r.HandleFunc("POST", "/api/org/import/plan.csv", contract.PermOrgManage, h.importPlanCSV)
}

// readImportFiles 는 multipart 로 올라온 CSV 파일들을 읽는다(칸 이름은 의미가 없다, 종류는 제목 줄로 판별한다).
func readImportFiles(w http.ResponseWriter, r *http.Request) (org.ImportInput, bool) {
	var in org.ImportInput
	r.Body = http.MaxBytesReader(w, r.Body, importBodyMax)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "파일이 5MB를 넘습니다.", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "요청 내용을 읽을 수 없습니다.", http.StatusBadRequest)
		}
		return in, false
	}
	defer r.MultipartForm.RemoveAll()
	for _, fhs := range r.MultipartForm.File {
		for _, fh := range fhs {
			f, err := fh.Open()
			if err != nil {
				http.Error(w, "파일을 열 수 없습니다.", http.StatusBadRequest)
				return in, false
			}
			data, err := io.ReadAll(io.LimitReader(f, org.ImportMaxFileBytes+2))
			f.Close()
			if err != nil {
				http.Error(w, "파일을 읽을 수 없습니다.", http.StatusBadRequest)
				return in, false
			}
			in.Files = append(in.Files, org.ImportUpload{Name: fh.Filename, Data: data})
		}
	}
	return in, true
}

type importErrDTO struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  string `json:"col"`
	Msg  string `json:"msg"`
	Text string `json:"text"`
}

func toImportErrs(in []org.ImportRowError) []importErrDTO {
	out := make([]importErrDTO, 0, len(in))
	for _, e := range in {
		out = append(out, importErrDTO{e.File, e.Line, e.Col, e.Msg, e.String()})
	}
	return out
}

func (h *orgHandlers) importPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	in, ok := readImportFiles(w, r)
	if !ok {
		return
	}
	pv, err := h.svc.PreviewImport(r.Context(), p, in)
	if err != nil {
		orgErr(w, err)
		return
	}
	type itemDTO struct {
		Effective string `json:"effectiveDate"`
		Label     string `json:"label"`
		Card      string `json:"card"`
		Class     string `json:"class"`
		Target    string `json:"target"`
		Content   string `json:"content"`
		File      string `json:"file"`
		Line      int    `json:"line"`
	}
	items := make([]itemDTO, 0, len(pv.Items))
	for i, it := range pv.Items {
		if i >= importItemsShown {
			break
		}
		items = append(items, itemDTO{string(it.Eff), it.Label, it.Card, it.Class, it.Target, it.Content, it.File, it.Line})
	}
	more := 0
	if len(pv.Items) > importItemsShown {
		more = len(pv.Items) - importItemsShown
	}
	type fileDTO struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		KindName string `json:"kindName"`
		Rows     int    `json:"rows"`
		Encoding string `json:"encoding"`
	}
	files := make([]fileDTO, 0, len(pv.Files))
	for _, f := range pv.Files {
		files = append(files, fileDTO{f.Name, f.Kind, f.KindName, f.Rows, f.Encoding})
	}
	type docDTO struct {
		ID        int64  `json:"id"`
		No        string `json:"no"`
		Title     string `json:"title"`
		Requester string `json:"requester"`
	}
	type impactDTO struct {
		Who   string   `json:"who"`
		Why   string   `json:"why"`
		Count int      `json:"count"`
		Docs  []docDTO `json:"docs"`
	}
	impact := make([]impactDTO, 0, len(pv.Impact))
	for _, im := range pv.Impact {
		docs := make([]docDTO, 0, len(im.Docs))
		for _, d := range im.Docs {
			docs = append(docs, docDTO{d.ID, d.No, d.Title, d.Requester})
		}
		impact = append(impact, impactDTO{im.Who, im.Why, im.Count, docs})
	}
	exp := ""
	if !pv.ExpiresAt.IsZero() {
		exp = pv.ExpiresAt.UTC().Format(time.RFC3339)
	}
	orgJSON(w, http.StatusOK, map[string]any{
		"previewId": pv.PreviewID, "expiresAt": exp, "base": string(pv.Base), "files": files, "counts": pv.Counts,
		"items": items, "itemsTotal": len(pv.Items), "itemsMore": more, "errors": toImportErrs(pv.Errors), "moreErrors": pv.MoreErrors,
		"fileErrors": toImportErrs(pv.FileErrors), "warnings": nonNil(pv.Warnings), "impact": impact, "impactMore": pv.ImpactMore,
		"retroRows": nonNil(pv.RetroRows), "noChange": pv.NoChangeAll,
	})
}

func (h *orgHandlers) importApply(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	in, ok := readImportFiles(w, r)
	if !ok {
		return
	}
	confirm := false
	if v := r.FormValue("confirm_retroactive"); v != "" {
		confirm, _ = strconv.ParseBool(v)
	}
	res, err := h.svc.ApplyImport(r.Context(), p, in, r.FormValue("previewId"), confirm)
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusCreated, map[string]any{"batch": res.Batch, "events": res.Events, "counts": res.Counts})
}

func writeCSV(w http.ResponseWriter, name string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // 엑셀이 UTF-8 로 읽게 BOM
	cw := csv.NewWriter(w)
	for _, row := range rows {
		safe := make([]string, len(row))
		for i, c := range row {
			safe[i] = org.CSVSafe(c)
		}
		_ = cw.Write(safe)
	}
	cw.Flush()
}

// importErrorsCSV 는 같은 파일을 다시 올려 오류 행만 원본 칸과 함께 내려받게 한다(서버는 파일을 저장하지 않는다).
func (h *orgHandlers) importErrorsCSV(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	in, ok := readImportFiles(w, r)
	if !ok {
		return
	}
	pv, err := h.svc.PreviewImport(r.Context(), p, in)
	if err != nil {
		orgErr(w, err)
		return
	}
	rows := [][]string{{"파일", "행", "열", "오류", "원본 칸"}}
	for _, e := range append(append([]org.ImportRowError{}, pv.FileErrors...), pv.Errors...) {
		line := ""
		if e.Line > 0 {
			line = strconv.Itoa(e.Line)
		}
		rows = append(rows, append([]string{e.File, line, e.Col, e.Msg}, e.Raw...))
	}
	writeCSV(w, "import-errors.csv", rows)
}

// importPlanCSV 는 미리보기의 전체 계획(이벤트 목록)을 내려받게 한다. 500건 넘게 보이지 않는 부분도 담는다.
func (h *orgHandlers) importPlanCSV(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	in, ok := readImportFiles(w, r)
	if !ok {
		return
	}
	pv, err := h.svc.PreviewImport(r.Context(), p, in)
	if err != nil {
		orgErr(w, err)
		return
	}
	if len(pv.Errors)+len(pv.FileErrors) > 0 {
		http.Error(w, "미리보기에 오류가 있어 계획을 내려받을 수 없습니다. 오류 행을 고쳐 다시 올리세요.", http.StatusConflict)
		return
	}
	rows := [][]string{{"효력일", "종류", "대상", "내용", "원본 파일", "행 번호"}}
	for _, it := range pv.Items {
		rows = append(rows, []string{string(it.Eff), it.Label, it.Target, it.Content, it.File, strconv.Itoa(it.Line)})
	}
	writeCSV(w, "import-plan.csv", rows)
}
