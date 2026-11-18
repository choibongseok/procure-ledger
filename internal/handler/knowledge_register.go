// (검색·색인)·erp(결재 문서 연결). 사내 지식 검색 라우트(B47,). 열람 범위 강제는 서비스 계층이 한다.
package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/knowledge"
)

// RegisterKnowledge 는 사내 지식 검색과 관리 라우트를 등록한다. 호출: cmd/server/routes_ai.go.
//
//	GET  /api/knowledge/search?q=&limit=5&offset=0&kinds=approval_doc,manual   knowledge:search  내용 검색
//	POST /api/knowledge/answer {question}                                       knowledge:search  질문하기(근거 조각만, 인용 번호 검증)
//	GET  /api/admin/knowledge/status                                           knowledge:manage  색인 현황(제목 없음)
//	POST /api/admin/knowledge/reindex  {entity, kind}                          knowledge:manage  다시 색인
//	PUT  /api/admin/knowledge/forms/{code}  {searchable}                      knowledge:manage  양식별 내용 검색 포함
//	GET  /api/knowledge/uploads/{id}[/file]                                    knowledge:search  업로드 문서 원문 보기·내려받기(열람 범위 밖은 404)
//	GET/POST /api/admin/knowledge/uploads, POST .../{id}/replace, PUT .../{id}/scope, DELETE .../{id}   knowledge:manage  업로드 매뉴얼·규정 관리
//
// 응답에는 점수·전체 건수·"가려진 결과" 같은 필드가 없다(권한 밖 문서의 존재가 새지 않게). more 는 이어 볼 수 있다는 뜻일 뿐 건수가 아니다.
func RegisterKnowledge(r contract.Registrar, d deps.Deps) {
	if d.Knowledge != nil {
		h := knowledgeHandler{svc: d.Knowledge}
		r.HandleFunc("GET", "/api/knowledge/search", contract.PermKnowledgeSearch, h.search)
		r.HandleFunc("GET", "/api/knowledge/uploads/{id}", contract.PermKnowledgeSearch, h.open)
		if d.KnowledgeAnswer != nil {
			r.HandleFunc("POST", "/api/knowledge/answer", contract.PermKnowledgeSearch, knowledgeAnswerHandler{svc: d.KnowledgeAnswer}.answer)
		}
		r.HandleFunc("GET", "/api/knowledge/uploads/{id}/file", contract.PermKnowledgeSearch, h.file)
	}
	if d.KnowledgeAdmin != nil {
		a := knowledgeAdminHandler{svc: d.KnowledgeAdmin}
		r.HandleFunc("GET", "/api/admin/knowledge/status", contract.PermKnowledgeManage, a.status)
		r.HandleFunc("POST", "/api/admin/knowledge/reindex", contract.PermKnowledgeManage, a.reindex)
		r.HandleFunc("PUT", "/api/admin/knowledge/forms/{code}", contract.PermKnowledgeManage, a.setSearchable)
		r.HandleFunc("GET", "/api/admin/knowledge/departments", contract.PermKnowledgeManage, a.departments)
		r.HandleFunc("GET", "/api/admin/knowledge/uploads", contract.PermKnowledgeManage, a.uploads)
		r.HandleFunc("POST", "/api/admin/knowledge/uploads", contract.PermKnowledgeManage, a.upload)
		r.HandleFunc("POST", "/api/admin/knowledge/uploads/{id}/replace", contract.PermKnowledgeManage, a.replace)
		r.HandleFunc("PUT", "/api/admin/knowledge/uploads/{id}/scope", contract.PermKnowledgeManage, a.setScope)
		r.HandleFunc("DELETE", "/api/admin/knowledge/uploads/{id}", contract.PermKnowledgeManage, a.remove)
	}
}

type knowledgeHandler struct{ svc domain.KnowledgeService }

type knowledgeHitJSON struct {
	Kind        string `json:"kind"`
	KindLabel   string `json:"kindLabel"`
	Title       string `json:"title"`
	DocNo       string `json:"docNo"`
	LocatorKind string `json:"locatorKind"`
	Locator     string `json:"locator"`
	Heading     string `json:"heading"`
	Snippet     string `json:"snippet"`
	Source      string `json:"source"`
	Link        string `json:"link"`
	Version     int    `json:"version,omitempty"`
}

func (h knowledgeHandler) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	var kinds []string
	if k := strings.TrimSpace(q.Get("kinds")); k != "" {
		kinds = strings.Split(k, ",")
	}
	res, err := h.svc.Search(r.Context(), domain.KnowledgeFilter{Query: q.Get("q"), Limit: limit, Offset: offset, Kinds: kinds})
	if err != nil {
		writeErr(w, err)
		return
	}
	hits := make([]knowledgeHitJSON, len(res.Hits))
	for i, x := range res.Hits {
		hits[i] = knowledgeHitJSON{Kind: x.Kind, KindLabel: domain.KnowledgeKindLabels[x.Kind], Title: x.Title, DocNo: x.DocNo, LocatorKind: x.LocatorKind, Locator: x.Locator,
			Heading: x.Heading, Snippet: x.Snippet, Source: x.Source, Link: x.Link, Version: x.Version}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits, "more": res.More})
}

type knowledgeAdminHandler struct{ svc domain.KnowledgeAdminService }

func (h knowledgeAdminHandler) status(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.Status(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": st.Docs, "states": st.States, "chunks": st.Chunks, "queueOpen": st.QueueOpen, "oldestQueue": st.OldestQueue,
		"generation": st.Generation, "changedAt": st.ChangedAt})
}

func (h knowledgeAdminHandler) reindex(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Entity string `json:"entity"`
		Kind   string `json:"kind"`
	}
	if !decode(w, r, &in) {
		return
	}
	done, skipped, err := h.svc.Reindex(r.Context(), strings.ToUpper(strings.TrimSpace(in.Entity)), strings.TrimSpace(in.Kind))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"done": done, "skipped": skipped})
}

func (h knowledgeAdminHandler) setSearchable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Searchable bool `json:"searchable"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.svc.SetFormSearchable(r.Context(), r.PathValue("code"), in.Searchable); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"searchable": in.Searchable})
}

// ---- 업로드 문서(매뉴얼·규정) ----

func (h knowledgeHandler) open(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	v, err := h.svc.Open(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	secs := make([]map[string]any, len(v.Sections))
	for i, x := range v.Sections {
		secs[i] = map[string]any{"locatorKind": x.LocatorKind, "locator": x.Locator, "heading": x.Heading, "text": x.Text}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": v.ID, "kind": v.Kind, "kindLabel": domain.KnowledgeKindLabels[v.Kind], "title": v.Title, "version": v.Version,
		"uploadedAt": v.UploadedAt, "filename": v.Filename, "sections": secs})
}

func (h knowledgeHandler) file(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	f, err := h.svc.File(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	// 내려받기로만 응답한다(브라우저가 열어 실행하지 않는다). 첨부와 같은 규칙.
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
	_, _ = w.Write(f.Data)
}

func uploadJSON(u domain.KnowledgeUpload) map[string]any {
	return map[string]any{"id": u.ID, "entityId": u.EntityID, "entityName": u.EntityName, "kind": u.Kind, "kindLabel": domain.KnowledgeKindLabels[u.Kind], "title": u.Title, "version": u.Version,
		"roles": u.Roles, "departments": u.Departments, "filename": u.Filename, "size": u.Size, "uploadedByName": u.UploadedByName, "uploadedAt": u.UploadedAt, "state": u.State, "note": u.Note, "notice": u.Notice, "warnings": u.Warnings}
}

func (h knowledgeAdminHandler) uploads(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Uploads(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, len(rows))
	for i, u := range rows {
		out[i] = uploadJSON(u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploads": out})
}

// readUploadFile 은 multipart 의 file 하나를 읽는다. 본문은 상한 + 여유 1MB 에서 서버가 먼저 자른다.
func readUploadFile(w http.ResponseWriter, r *http.Request) (name string, data []byte, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, knowledge.MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "파일이 너무 큽니다(최대 10MB).", http.StatusRequestEntityTooLarge)
		return "", nil, false
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "파일을 선택하세요.", http.StatusBadRequest)
		return "", nil, false
	}
	defer file.Close()
	data, err = io.ReadAll(io.LimitReader(file, knowledge.MaxUploadBytes+1))
	if err != nil {
		http.Error(w, "파일을 읽을 수 없습니다.", http.StatusBadRequest)
		return "", nil, false
	}
	return hdr.Filename, data, true
}

func (h knowledgeAdminHandler) upload(w http.ResponseWriter, r *http.Request) {
	name, data, ok := readUploadFile(w, r)
	if !ok {
		return
	}
	u, err := h.svc.Upload(r.Context(), domain.KnowledgeUploadInput{Kind: r.FormValue("kind"), Title: r.FormValue("title"), Entity: r.FormValue("entity"),
		Roles: r.FormValue("roles"), Departments: r.FormValue("departments"), Filename: name, Content: data})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, uploadJSON(u))
}

func (h knowledgeAdminHandler) replace(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	name, data, ok := readUploadFile(w, r)
	if !ok {
		return
	}
	u, err := h.svc.Replace(r.Context(), id, name, data)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, uploadJSON(u))
}

func (h knowledgeAdminHandler) setScope(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Roles       string `json:"roles"`
		Departments string `json:"departments"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, err := h.svc.SetScope(r.Context(), id, in.Roles, in.Departments)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, uploadJSON(u))
}

func (h knowledgeAdminHandler) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Remove(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h knowledgeAdminHandler) departments(w http.ResponseWriter, r *http.Request) {
	names, err := h.svc.Departments(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"departments": names})
}

type knowledgeAnswerHandler struct{ svc domain.KnowledgeAnswerService }

func (h knowledgeAnswerHandler) answer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeErr(w, fmt.Errorf("%w: 질문을 읽지 못했습니다", contract.ErrValidation))
		return
	}
	res, err := h.svc.Answer(r.Context(), in.Question)
	if err != nil {
		writeErr(w, err)
		return
	}
	src := make([]map[string]any, len(res.Sources))
	for i, c := range res.Sources {
		src[i] = map[string]any{"n": c.N, "source": c.Hit.Source, "link": c.Hit.Link, "kind": c.Hit.Kind, "kindLabel": domain.KnowledgeKindLabels[c.Hit.Kind], "snippet": c.Hit.Snippet}
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": res.Text, "found": res.Found, "sources": src})
}
