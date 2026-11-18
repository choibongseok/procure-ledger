// (internal/handler/org*). 조직도·인사 발령 API(B20). 9절.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// RegisterOrg 는 조직도 조회와 조직 변경·인사 발령 라우트를 등록한다. 모든 라우트가 권한을 선언한다.
// 조직 서비스가 없으면(조립 전) 아무것도 등록하지 않는다.
func RegisterOrg(r contract.Registrar, d deps.Deps) {
	svc, ok := d.Org.(*org.Service)
	if !ok {
		return
	}
	h := &orgHandlers{svc: svc}
	r.HandleFunc("GET", "/api/org/tree", contract.PermOrgRead, h.tree)
	r.HandleFunc("GET", "/api/org/units/{id}", contract.PermOrgRead, h.unit)
	r.HandleFunc("GET", "/api/org/units/{id}/members", contract.PermOrgRead, h.members)
	r.HandleFunc("GET", "/api/org/people", contract.PermOrgRead, h.search)
	r.HandleFunc("GET", "/api/org/people/{id}", contract.PermOrgRead, h.person)
	r.HandleFunc("GET", "/api/org/people/{id}/history", contract.PermOrgHistory, h.history)
	r.HandleFunc("GET", "/api/org/ranks", contract.PermOrgRead, h.ranks)
	r.HandleFunc("PUT", "/api/org/ranks", contract.PermOrgManage, h.saveRanks)
	r.HandleFunc("GET", "/api/org/events", contract.PermOrgManage, h.events)
	r.HandleFunc("POST", "/api/org/events/preview", contract.PermOrgManage, h.preview)
	r.HandleFunc("POST", "/api/org/events", contract.PermOrgManage, h.register)
	// 휴직 사유 원문(B50 11절). 범위(본인·직속 조직장·인사 담당)와 열람 기록은 서비스가 판정한다. 범위 밖은 404.
	r.HandleFunc("GET", "/api/org/events/{id}/absence-reason", contract.PermAttendanceSelf, h.absenceReason)
	r.HandleFunc("POST", "/api/org/events/{id}/cancel", contract.PermOrgManage, h.cancel)
	r.HandleFunc("POST", "/api/org/events/{id}/correct", contract.PermOrgManage, h.correct)
	r.HandleFunc("GET", "/api/org/consistency", contract.PermOrgManage, h.consistency)
	h.registerImport(r)
}

type orgHandlers struct{ svc *org.Service }

func orgJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// orgErr 는 계약 오류를 상태 코드로 옮긴다. 30일 넘는 소급은 409 와 확인 요청 본문이다.
func orgErr(w http.ResponseWriter, err error) {
	var ce *org.ConfirmError
	if errors.As(err, &ce) {
		orgJSON(w, http.StatusConflict, map[string]any{"needConfirm": true, "days": ce.Days, "message": ce.Msg})
		return
	}
	var nc *org.ImportNeedConfirm
	if errors.As(err, &nc) {
		orgJSON(w, http.StatusConflict, map[string]any{"needConfirm": true, "message": nc.Msg})
		return
	}
	if errors.Is(err, contract.ErrNotInOrg) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if errors.Is(err, org.ErrPrivacyNote) { // 열람 기록을 남기지 못해 정보를 내지 않는다(fail-closed)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	adminErr(w, err)
}

func asOfParam(r *http.Request) contract.Day { return contract.Day(r.URL.Query().Get("asOf")) }

func qBool(r *http.Request, key string) bool {
	v := r.URL.Query().Get(key)
	return v == "1" || v == "true"
}

type unitDTO struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	ParentID  int64  `json:"parentId"`
	SortOrder int    `json:"sortOrder"`
	Closed    bool   `json:"closed"`
}

func toUnitDTO(u contract.OrgUnit) unitDTO {
	return unitDTO{u.ID, u.Code, u.Name, string(u.Kind), u.ParentID, u.SortOrder, u.Closed}
}

type placeDTO struct {
	UnitID int64     `json:"unitId"`
	Name   string    `json:"name"`
	Kind   string    `json:"kind"`
	Path   string    `json:"path"`     // 회사를 뺀 소속 경로(표용)
	Full   string    `json:"fullPath"` // 회사부터 이어진 소속 경로(사람 카드용)
	IsHead bool      `json:"isHead"`
	Acting bool      `json:"acting"`
	Title  string    `json:"title,omitempty"` // 조직장이면 직책 이름(팀장 등)
	Closed bool      `json:"closed"`
	Trail  []unitDTO `json:"trail"`
}

// fullPathText 는 회사를 포함해 맨 위 조직부터 이은 경로다(명세 8.1 의 소속 경로).
func fullPathText(units []contract.OrgUnit) string {
	names := make([]string, 0, len(units))
	for _, u := range units {
		names = append(names, u.Name)
	}
	return strings.Join(names, " > ")
}

func toPlaceDTO(p contract.OrgPlace) placeDTO {
	d := placeDTO{UnitID: p.Unit.ID, Name: p.Unit.Name, Kind: string(p.Unit.Kind), Path: contract.OrgPathText(p.Path), Full: fullPathText(p.Path), IsHead: p.IsHead,
		Acting: p.Acting, Closed: p.Unit.Closed}
	if p.IsHead {
		d.Title = contract.HeadTitle(p.Unit.Kind)
	}
	for _, u := range p.Path {
		d.Trail = append(d.Trail, toUnitDTO(u))
	}
	return d
}

type cardDTO struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	EmployeeNo string     `json:"employeeNo"`
	Rank       string     `json:"rank"`
	Active     bool       `json:"active"`
	Email      string     `json:"email"`
	Phone      string     `json:"phone"`
	Extension  string     `json:"extension"`
	OnLeave    bool       `json:"onLeave"` // 휴직 중(구분은 민감 정보라 내보내지 않는다)
	Primary    placeDTO   `json:"primary"`
	Concurrent []placeDTO `json:"concurrent"`
	Acting     []placeDTO `json:"acting"`
}

func toCardDTO(c contract.PersonCard) cardDTO {
	d := cardDTO{ID: c.UserID, Name: c.Name, EmployeeNo: c.EmployeeNo, Rank: c.Rank, Active: c.Active, Email: c.Email, Phone: c.Phone,
		Extension: c.Extension, OnLeave: c.OnLeave, Primary: toPlaceDTO(c.Primary), Concurrent: []placeDTO{}, Acting: []placeDTO{}}
	for _, p := range c.Concurrent {
		d.Concurrent = append(d.Concurrent, toPlaceDTO(p))
	}
	for _, p := range c.Acting {
		d.Acting = append(d.Acting, toPlaceDTO(p))
	}
	return d
}

type treeDTO struct {
	ID         int64      `json:"id"`
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Closed     bool       `json:"closed"`
	HeadUserID int64      `json:"headUserId"`
	HeadName   string     `json:"headName"`
	HeadActing bool       `json:"headActing"`
	Direct     int        `json:"direct"`
	Total      int        `json:"total"`
	Children   []*treeDTO `json:"children"`
}

func toTreeDTO(n *org.TreeNode) *treeDTO {
	d := &treeDTO{n.ID, n.Code, n.Name, string(n.Kind), n.Closed, n.HeadUserID, n.HeadName, n.HeadActing, n.Direct, n.Total, []*treeDTO{}}
	for _, c := range n.Children {
		d.Children = append(d.Children, toTreeDTO(c))
	}
	return d
}

func (h *orgHandlers) tree(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.Tree(r.Context(), asOfParam(r))
	if err != nil {
		orgErr(w, err)
		return
	}
	if n == nil {
		orgJSON(w, http.StatusOK, map[string]any{"root": nil, "asOf": string(h.dayOf(r))})
		return
	}
	orgJSON(w, http.StatusOK, map[string]any{"root": toTreeDTO(n), "asOf": string(h.dayOf(r))})
}

func (h *orgHandlers) dayOf(r *http.Request) contract.Day {
	if d := asOfParam(r); d != "" {
		return d
	}
	return h.svc.Today()
}

func (h *orgHandlers) unit(w http.ResponseWriter, r *http.Request) {
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	ctx, asOf := r.Context(), asOfParam(r)
	u, err := h.svc.Unit(ctx, id, asOf)
	if err != nil {
		orgErr(w, err)
		return
	}
	path, _ := h.svc.Path(ctx, id, asOf)
	head, _ := h.svc.Head(ctx, id, asOf)
	kids, _ := h.svc.Children(ctx, id, asOf)
	out := map[string]any{"unit": toUnitDTO(u), "path": contract.OrgPathText(path), "children": []unitDTO{}}
	hd := map[string]any{"vacant": head.Vacant, "acting": head.Acting, "userId": head.UserID, "name": ""}
	if !head.Vacant {
		if c, err := h.svc.Card(ctx, contract.Principal{}, head.UserID, asOf); err == nil {
			hd["name"] = c.Name
		}
	}
	out["head"] = hd
	cs := make([]unitDTO, 0, len(kids))
	for _, k := range kids {
		cs = append(cs, toUnitDTO(k))
	}
	out["children"] = cs
	cnt, _ := h.svc.CountMembers(ctx, id, asOf, true)
	out["totalMembers"] = cnt
	acting := []map[string]any{}
	if al, err := h.svc.ActingList(ctx, id); err == nil {
		for _, a := range al {
			acting = append(acting, map[string]any{"userId": a.UserID, "name": a.Name, "from": string(a.From), "to": string(a.To)})
		}
	}
	out["acting"] = acting
	orgJSON(w, http.StatusOK, out)
}

func (h *orgHandlers) members(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	cards, err := h.svc.SearchPeople(r.Context(), p, contract.PeopleQuery{UnitID: id, AsOf: asOfParam(r), Limit: contract.OrgSearchMax,
		IncludeLeft: qBool(r, "includeLeft")})
	if err != nil {
		orgErr(w, err)
		return
	}
	// descendants=0 이면 그 조직 자체의 구성원만 남긴다(기본은 하위 조직 포함).
	if v := r.URL.Query().Get("descendants"); v == "0" || v == "false" {
		keep := cards[:0]
		for _, c := range cards {
			in := c.Primary.Unit.ID == id
			for _, cu := range c.Concurrent {
				in = in || cu.Unit.ID == id
			}
			if in {
				keep = append(keep, c)
			}
		}
		cards = keep
	}
	out := make([]cardDTO, 0, len(cards))
	for _, c := range cards {
		out = append(out, toCardDTO(c))
	}
	orgJSON(w, http.StatusOK, map[string]any{"members": out})
}

func (h *orgHandlers) search(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	unit, _ := strconv.ParseInt(q.Get("unitId"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	cards, err := h.svc.SearchPeople(r.Context(), p, contract.PeopleQuery{Text: q.Get("q"), UnitID: unit, HeadOnly: qBool(r, "headOnly"),
		IncludeLeft: qBool(r, "includeLeft"), AsOf: asOfParam(r), Limit: limit})
	if err != nil {
		orgErr(w, err)
		return
	}
	out := make([]cardDTO, 0, len(cards))
	for _, c := range cards {
		out = append(out, toCardDTO(c))
	}
	orgJSON(w, http.StatusOK, map[string]any{"people": out})
}

func (h *orgHandlers) person(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	c, err := h.svc.Card(r.Context(), p, id, asOfParam(r))
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusOK, toCardDTO(c))
}

type eventDTO struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	KindLabel   string `json:"kindLabel"`
	Subject     string `json:"subject"`
	SubjectID   int64  `json:"subjectId"`
	SubjectName string `json:"subjectName"`
	Effective   string `json:"effectiveDate"`
	Summary     string `json:"summary"`
	Reason      string `json:"reason"`
	CreatedBy   string `json:"createdBy"`
	Status      string `json:"status"`
	Corrects    int64  `json:"corrects"`
	// 휴직 발령에 사유가 있다는 표시. 원문은 이 응답에 없고 /absence-reason 으로만 읽는다.
	HasAbsenceReason bool `json:"hasAbsenceReason"`
}

func toEventDTOs(evs []org.EventView) []eventDTO {
	out := make([]eventDTO, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventDTO{e.ID, e.Kind, e.KindLabel, e.Subject, e.SubjectID, e.SubjectName, string(e.Effective), e.Summary, e.Reason, e.CreatedBy, e.Status, e.Corrects, e.HasAbsenceReason})
	}
	return out
}

// history 는 사람의 발령 이력(사유 포함)이다. org:history 권한(admin·auditor)만 부를 수 있다.
func (h *orgHandlers) history(w http.ResponseWriter, r *http.Request) {
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	evs, err := h.svc.ListEvents(r.Context(), org.EventFilter{UserID: id})
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusOK, map[string]any{"events": toEventDTOs(evs)})
}

func (h *orgHandlers) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50 // 쪽당 기본 50건, 최대 200건
	}
	if limit > 200 {
		limit = 200
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	unitID, _ := strconv.ParseInt(q.Get("unitId"), 10, 64)
	f := org.EventFilter{Status: q.Get("status"), Q: q.Get("q"), Kind: q.Get("kind"), UnitID: unitID, Offset: offset, Limit: limit}
	for key, dst := range map[string]*contract.Day{"from": &f.From, "to": &f.To} {
		if v := q.Get(key); v != "" {
			d, err := org.ParseDay(v)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			*dst = d
		}
	}
	pg, err := h.svc.ListEventsPage(r.Context(), f)
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusOK, map[string]any{"events": toEventDTOs(pg.Items), "total": pg.Total, "offset": offset, "limit": limit})
}

type commandIn struct {
	Kind          string `json:"kind"`
	EffectiveDate string `json:"effectiveDate"`
	Reason        string `json:"reason"`
	Confirm       bool   `json:"confirm"`
	UnitID        int64  `json:"unitId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	UnitKind      string `json:"unitKind"`
	ParentID      int64  `json:"parentId"`
	Sort          int    `json:"sort"`
	UserID        int64  `json:"userId"`
	ToUnit        int64  `json:"toUnit"`
	Rank          int64  `json:"rank"`
	Email         string `json:"email"`
	EmployeeNo    string `json:"employeeNo"`
	Role          string `json:"role"`
	Phone         string `json:"phone"`
	Extension     string `json:"extension"`
	LastDay       string `json:"lastDay"`
	EndDay        string `json:"endDay"`
	LeaveKind     string `json:"leaveKind"`
	AbsenceKind   string `json:"absenceKind"`
	ReturnDay     string `json:"returnDay"`
	AbsenceReason string `json:"absenceReason"`
}

func (c commandIn) command() org.Command {
	return org.Command{Kind: c.Kind, Effective: contract.Day(c.EffectiveDate), Reason: c.Reason, Confirm: c.Confirm, UnitID: c.UnitID, Code: c.Code,
		Name: c.Name, UnitKind: contract.OrgKind(c.UnitKind), ParentID: c.ParentID, Sort: c.Sort, UserID: c.UserID, ToUnit: c.ToUnit, Rank: c.Rank,
		Email: c.Email, EmployeeNo: c.EmployeeNo, Role: contract.Role(c.Role), Phone: c.Phone, Extension: c.Extension,
		LastDay: contract.Day(c.LastDay), EndDay: contract.Day(c.EndDay), LeaveKind: c.LeaveKind,
		AbsenceKind: c.AbsenceKind, ReturnDay: contract.Day(c.ReturnDay), AbsenceReason: c.AbsenceReason}
}

func readCommand(w http.ResponseWriter, r *http.Request) (commandIn, bool) {
	var in commandIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "요청 내용을 읽을 수 없습니다.", http.StatusBadRequest)
		return in, false
	}
	return in, true
}

func (h *orgHandlers) preview(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	in, ok := readCommand(w, r)
	if !ok {
		return
	}
	pv, err := h.svc.Preview(r.Context(), p, in.command())
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusOK, map[string]any{"effects": pv.Effects, "warnings": nonNil(pv.Warnings), "pendingApprovals": pv.PendingApprovals, "pendingDocs": toPendingDocs(pv.PendingDocs), "retroactive": pv.Retroactive})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func outcomeJSON(o org.Outcome) map[string]any {
	return map[string]any{"eventIds": o.EventIDs, "unitId": o.UnitID, "userId": o.UserID, "retroactive": o.Retroactive,
		"warnings": nonNil(o.Warnings), "effectiveDate": string(o.EffectiveDate)}
}

func (h *orgHandlers) register(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	in, ok := readCommand(w, r)
	if !ok {
		return
	}
	o, err := h.svc.Register(r.Context(), p, in.command())
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusCreated, outcomeJSON(o))
}

func (h *orgHandlers) cancel(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason  string `json:"reason"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "요청 내용을 읽을 수 없습니다.", http.StatusBadRequest)
		return
	}
	if err := h.svc.Cancel(r.Context(), p, id, in.Reason, in.Confirm); err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *orgHandlers) correct(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	in, ok := readCommand(w, r)
	if !ok {
		return
	}
	o, err := h.svc.Correct(r.Context(), p, id, in.command())
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusCreated, outcomeJSON(o))
}

type rankDTO struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Sort   int    `json:"sort"`
	Active bool   `json:"active"`
	InUse  int    `json:"inUse"`
}

func (h *orgHandlers) ranks(w http.ResponseWriter, r *http.Request) {
	rs, err := h.svc.ListRanks(r.Context())
	if err != nil {
		orgErr(w, err)
		return
	}
	out := make([]rankDTO, 0, len(rs))
	for _, x := range rs {
		out = append(out, rankDTO{x.ID, x.Name, x.Sort, x.Active, x.InUse})
	}
	orgJSON(w, http.StatusOK, map[string]any{"ranks": out})
}

func (h *orgHandlers) saveRanks(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	var in struct {
		Ranks []rankDTO `json:"ranks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "요청 내용을 읽을 수 없습니다.", http.StatusBadRequest)
		return
	}
	inputs := make([]org.RankInput, 0, len(in.Ranks))
	for _, x := range in.Ranks {
		inputs = append(inputs, org.RankInput{ID: x.ID, Name: x.Name, Sort: x.Sort, Active: x.Active})
	}
	if err := h.svc.SaveRanks(r.Context(), p, inputs); err != nil {
		orgErr(w, err)
		return
	}
	h.ranks(w, r)
}

func (h *orgHandlers) consistency(w http.ResponseWriter, r *http.Request) {
	is, err := h.svc.Issues(r.Context())
	if err != nil {
		orgErr(w, err)
		return
	}
	type issueDTO struct {
		Kind   string `json:"kind"`
		UnitID int64  `json:"unitId"`
		UserID int64  `json:"userId"`
		Name   string `json:"name"`
		Detail string `json:"detail"`
	}
	out := make([]issueDTO, 0, len(is))
	for _, i := range is {
		out = append(out, issueDTO{i.Kind, i.UnitID, i.UserID, i.Name, i.Detail})
	}
	rep, err := h.svc.Consistency(r.Context())
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusOK, map[string]any{"issues": out, "log": map[string]any{"ok": rep.OK, "mismatches": rep.Mismatches, "broken": rep.Broken}})
}

func toPendingDocs(in []org.ImpactDoc) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, d := range in {
		out = append(out, map[string]any{"id": d.ID, "no": d.No, "title": d.Title, "requester": d.Requester})
	}
	return out
}

// absenceReason 은 휴직 사유 원문이다(B50 11절). 본인이 아닌 사람에게 내기 전에 열람 기록을 남기고, 기록하지 못하면 사유 없이 실패한다.
func (h *orgHandlers) absenceReason(w http.ResponseWriter, r *http.Request) {
	p, ok := adminPrincipal(w, r)
	if !ok {
		return
	}
	id, ok := adminID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.AbsenceReason(r.Context(), p, id)
	if err != nil {
		orgErr(w, err)
		return
	}
	orgJSON(w, http.StatusOK, map[string]any{"eventId": v.EventID, "userId": v.UserID, "reason": v.Reason})
}
