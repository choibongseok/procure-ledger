// 회사 설정 라우트(B34 단계 1,). 조회 company:read(로그인한 모든 역할), 변경 company:manage(admin 만).
package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/service"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// RegisterCompany 는 /api/company 와 로고·직인 이미지 라우트를 등록한다. 호출: cmd/server/routes_erp.go.
func RegisterCompany(r contract.Registrar, d deps.Deps) {
	if d.Company == nil {
		return
	}
	h := companyHandler{svc: d.Company}
	r.HandleFunc("GET", "/api/company", contract.PermCompanyRead, h.get)
	r.HandleFunc("PUT", "/api/company", contract.PermCompanyManage, h.put)
	r.HandleFunc("GET", "/api/company/logo", contract.PermCompanyRead, h.asset(domain.CompanyAssetLogo))
	r.HandleFunc("GET", "/api/company/seal", contract.PermCompanyRead, h.asset(domain.CompanyAssetSeal))
	r.HandleFunc("PUT", "/api/company/logo", contract.PermCompanyManage, h.upload(domain.CompanyAssetLogo))
	r.HandleFunc("PUT", "/api/company/seal", contract.PermCompanyManage, h.upload(domain.CompanyAssetSeal))
	// 회계 설정·영업일 달력(단계 2·3). 조회는 서비스가 회사 설정 관리자·재무·감사로 좁힌다.
	r.HandleFunc("GET", "/api/company/fiscal", contract.PermCompanyRead, h.fiscal)
	r.HandleFunc("PUT", "/api/company/fiscal-start", contract.PermCompanyManage, h.setFiscalStart)
	r.HandleFunc("GET", "/api/company/vat-rates", contract.PermCompanyRead, h.vatRates)
	r.HandleFunc("POST", "/api/company/vat-rates", contract.PermCompanyManage, h.addVATRate)
	r.HandleFunc("POST", "/api/company/vat-rates/{id}/cancel", contract.PermCompanyManage, h.cancelVATRate)
	r.HandleFunc("GET", "/api/company/holidays", contract.PermCompanyRead, h.holidays)
	r.HandleFunc("POST", "/api/company/holidays", contract.PermCompanyManage, h.addHolidays)
	r.HandleFunc("POST", "/api/company/holidays/preview", contract.PermCompanyManage, h.previewHolidays)
	r.HandleFunc("DELETE", "/api/company/holidays/{date}", contract.PermCompanyManage, h.removeHoliday)
}

type companyHandler struct{ svc domain.CompanyService }

func companyDTO(c domain.CompanyInfo) map[string]any {
	return map[string]any{"name": c.Name, "businessNo": c.BusinessNo, "representative": c.Representative, "address": c.Address, "phone": c.Phone,
		"hasLogo": c.HasLogo, "hasSeal": c.HasSeal, "configured": c.Configured, "version": c.Version}
}

func (h companyHandler) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Info(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companyDTO(c))
}

func (h companyHandler) put(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name           string `json:"name"`
		BusinessNo     string `json:"businessNo"`
		Representative string `json:"representative"`
		Address        string `json:"address"`
		Phone          string `json:"phone"`
		Version        int64  `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := h.svc.Update(r.Context(), domain.CompanyInput{Name: in.Name, BusinessNo: in.BusinessNo, Representative: in.Representative, Address: in.Address, Phone: in.Phone}, in.Version)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companyDTO(c))
}

// asset 은 현재 이미지를 인라인으로 내려 준다(짧은 캐시). 이미지가 없으면 404 이고 출력 화면은 자리를 비운다.
func (h companyHandler) asset(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := h.svc.Asset(r.Context(), kind)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", a.ContentType)
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, max-age=60")
		w.Header().Set("Content-Length", strconv.Itoa(len(a.Data)))
		_, _ = w.Write(a.Data)
	}
}

const companyUploadLimit = 512 * 1024

// upload 는 멀티파트의 file(이미지)을 받는다. 형식·크기 검사는 서비스가 한다(상한보다 조금 큰 본문까지만 읽는다).
func (h companyHandler) upload(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, companyUploadLimit+(1<<16))
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "이미지는 512KB 이하로 올려 주세요.", http.StatusRequestEntityTooLarge)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "이미지 파일을 선택하세요.", http.StatusBadRequest)
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, companyUploadLimit+1))
		if err != nil {
			http.Error(w, "이미지를 읽을 수 없습니다.", http.StatusBadRequest)
			return
		}
		c, err := h.svc.SetAsset(r.Context(), kind, data)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, companyDTO(c))
	}
}

func (h companyHandler) fiscal(w http.ResponseWriter, r *http.Request) {
	f, err := h.svc.Fiscal(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"startMonth": f.StartMonth, "name": f.Name, "thisFrom": f.ThisFrom, "thisTo": f.ThisTo, "lastFrom": f.LastFrom, "lastTo": f.LastTo,
		"closed": f.Closed, "version": f.Version, "configured": f.Configured})
}

func (h companyHandler) setFiscalStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Month   int   `json:"month"`
		Version int64 `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := h.svc.SetFiscalStart(r.Context(), in.Month, in.Version)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companyDTO(c))
}

func (h companyHandler) vatRates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.VATRates(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(rows))
	for i, v := range rows {
		out[i] = map[string]any{"id": v.ID, "effectiveFrom": v.EffectiveFrom, "percent": v.Percent, "createdBy": v.CreatedBy, "createdAt": ts(v.CreatedAt), "canceled": v.Canceled, "pending": v.Pending}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h companyHandler) addVATRate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		EffectiveFrom string `json:"effectiveFrom"`
		Percent       int    `json:"percent"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := h.svc.AddVATRate(r.Context(), in.EffectiveFrom, in.Percent)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": v.ID, "effectiveFrom": v.EffectiveFrom, "percent": v.Percent, "pending": v.Pending})
}

func (h companyHandler) cancelVATRate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.CancelVATRate(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func holidayDTOs(hs []domain.Holiday) []map[string]any {
	out := make([]map[string]any, len(hs))
	for i, x := range hs {
		out[i] = map[string]any{"date": x.Date, "name": x.Name, "createdBy": x.CreatedBy}
	}
	return out
}

func (h companyHandler) holidays(w http.ResponseWriter, r *http.Request) {
	hs, err := h.svc.Holidays(r.Context(), queryInt(r, "year", 0))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, holidayDTOs(hs))
}

func (h companyHandler) addHolidays(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []struct {
			Date string `json:"date"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if !decode(w, r, &in) {
		return
	}
	items := make([]domain.Holiday, len(in.Items))
	for i, x := range in.Items {
		items[i] = domain.Holiday{Date: x.Date, Name: x.Name}
	}
	n, err := h.svc.AddHolidays(r.Context(), items)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": n, "skipped": len(items) - n})
}

// previewHolidays 는 일괄 입력 글을 읽어 줄별 결과를 돌려준다(등록하지 않는다). 오류가 줄의 절반을 넘으면 확정을 막도록 화면이 쓴다.
func (h companyHandler) previewHolidays(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	items, problems := service.ParseHolidayLines(in.Text)
	if problems == nil {
		problems = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": holidayDTOs(items), "problems": problems})
}

func (h companyHandler) removeHoliday(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveHoliday(r.Context(), r.PathValue("date")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
