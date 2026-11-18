package scim

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// userIn 은 SCIM User 요청 본문이다(POST·PUT·PATCH 가 모두 이 형태로 모은다). 포인터는 "보냈는가"를 구분한다.
type userIn struct {
	Schemas     []string `json:"schemas"`
	ExternalID  *string  `json:"externalId"`
	UserName    *string  `json:"userName"`
	DisplayName *string  `json:"displayName"`
	Name        *struct {
		Formatted  *string `json:"formatted"`
		FamilyName *string `json:"familyName"`
		GivenName  *string `json:"givenName"`
	} `json:"name"`
	Title        *string         `json:"title"`
	Active       json.RawMessage `json:"active"`
	Emails       json.RawMessage `json:"emails"`
	PhoneNumbers []struct {
		Value string `json:"value"`
		Type  string `json:"type"`
	} `json:"phoneNumbers"`
	Enterprise *struct {
		Department     *string `json:"department"`
		EmployeeNumber *string `json:"employeeNumber"`
	} `json:"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"`
	Password     json.RawMessage `json:"password"`
	Roles        json.RawMessage `json:"roles"`
	Entitlements json.RawMessage `json:"entitlements"`
	Groups       json.RawMessage `json:"groups"`
}

func (u *userIn) department() string {
	if u.Enterprise != nil && u.Enterprise.Department != nil {
		return strings.TrimSpace(*u.Enterprise.Department)
	}
	return ""
}

func (u *userIn) employeeNumber() *string {
	if u.Enterprise != nil {
		return u.Enterprise.EmployeeNumber
	}
	return nil
}

func (u *userIn) workPhone() (string, bool) {
	for _, p := range u.PhoneNumbers {
		if strings.EqualFold(p.Type, "work") {
			return strings.TrimSpace(p.Value), true
		}
	}
	return "", false
}

func isHangul(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

// displayNameOf 는 이름 속성의 우선순위(displayName → name.formatted → 성+이름)로 표시 이름을 정한다. 없으면 "" 이다.
func (u *userIn) displayNameOf() string {
	if u.DisplayName != nil && strings.TrimSpace(*u.DisplayName) != "" {
		return strings.TrimSpace(*u.DisplayName)
	}
	if u.Name != nil {
		if u.Name.Formatted != nil && strings.TrimSpace(*u.Name.Formatted) != "" {
			return strings.TrimSpace(*u.Name.Formatted)
		}
		fam, giv := "", ""
		if u.Name.FamilyName != nil {
			fam = strings.TrimSpace(*u.Name.FamilyName)
		}
		if u.Name.GivenName != nil {
			giv = strings.TrimSpace(*u.Name.GivenName)
		}
		switch {
		case fam != "" && giv != "":
			if isHangul(fam) && isHangul(giv) {
				return fam + giv
			}
			return fam + " " + giv
		case fam != "":
			return fam
		case giv != "":
			return giv
		}
	}
	return ""
}

// activeOf 는 active 값을 읽는다. 불리언과 문자열 "True"·"False"(Entra 변종)를 받는다.
func (u *userIn) activeOf() (val, set bool, err *apiError) {
	if len(u.Active) == 0 || string(u.Active) == "null" {
		return false, false, nil
	}
	var b bool
	if json.Unmarshal(u.Active, &b) == nil {
		return b, true, nil
	}
	var s string
	if json.Unmarshal(u.Active, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, true, nil
		case "false":
			return false, true, nil
		}
	}
	return false, false, errInvalidValue("active 는 true 또는 false 여야 합니다.")
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// ignoredAttrs 는 받았지만 이번 버전이 적용하지 않는 속성의 경고다(조용히 버리지 않고 동기화 기록에 남긴다).
func (u *userIn) ignoredAttrs() []string {
	var w []string
	if len(u.Password) > 0 && string(u.Password) != "null" {
		w = append(w, "password 는 받지 않습니다(외부 IdP 로그인 전제)")
	}
	if (len(u.Roles) > 0 && string(u.Roles) != "null") || (len(u.Entitlements) > 0 && string(u.Entitlements) != "null") {
		w = append(w, "roles·entitlements 는 무시합니다(역할은 SCIM 으로 줄 수 없습니다)")
	}
	if len(u.Groups) > 0 && string(u.Groups) != "null" {
		w = append(w, "groups 는 읽기 전용이라 무시합니다")
	}
	return w
}

func validEmail(e string) bool {
	if e == "" || strings.ContainsAny(e, " <>,;") {
		return false
	}
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e
}

// ---- 사용자 보기 ----

type orgState struct {
	InOrg    bool
	Employed bool
	RankID   int64
	UnitID   int64
	Rank     string
	Unit     string
	UnitCode string
	HireDay  string // 가장 늦은 입사 효력일(없으면 "")
}

func (s *Service) orgStateOf(ctx context.Context, q querier, uid int64, today contract.Day) (orgState, error) {
	var st orgState
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_person_spans WHERE user_id = ?`, uid).Scan(&n); err != nil {
		return st, err
	}
	st.InOrg = n > 0
	if !st.InOrg {
		return st, nil
	}
	d := string(today)
	err := q.QueryRowContext(ctx, `SELECT ps.rank_id, ps.primary_unit_id, r.name FROM org_person_spans ps JOIN org_ranks r ON r.id = ps.rank_id
		WHERE ps.user_id = ? AND ps.left_flag = 0 AND ps.valid_from <= ? AND (ps.valid_to IS NULL OR ? < ps.valid_to)`, uid, d, d).Scan(&st.RankID, &st.UnitID, &st.Rank)
	switch {
	case err == nil:
		st.Employed = true
		_ = q.QueryRowContext(ctx, `SELECT name, code FROM org_unit_spans WHERE unit_id = ? AND valid_from <= ? AND (valid_to IS NULL OR ? < valid_to)`, st.UnitID, d, d).Scan(&st.Unit, &st.UnitCode)
	case errors.Is(err, sql.ErrNoRows):
	default:
		return st, err
	}
	var hire sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT MAX(effective_date) FROM org_events WHERE subject_type = 'person' AND subject_id = ? AND kind = 'hire'`, uid).Scan(&hire); err != nil {
		return st, err
	}
	st.HireDay = hire.String
	return st, nil
}

type userView struct {
	ID         int64
	Email      string
	Name       string
	Active     bool
	State      orgState
	EmpNo      string
	Phone      string
	ExternalID string
	Created    time.Time
	Modified   time.Time
	Groups     []map[string]any
}

var errNoUser = errors.New("사용자 없음")

func (s *Service) loadView(ctx context.Context, q querier, uid int64, source string, today contract.Day) (userView, error) {
	v := userView{ID: uid}
	var active int
	if err := q.QueryRowContext(ctx, `SELECT email, name, active FROM users WHERE id = ?`, uid).Scan(&v.Email, &v.Name, &active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return v, errNoUser
		}
		return v, err
	}
	st, err := s.orgStateOf(ctx, q, uid, today)
	if err != nil {
		return v, err
	}
	v.State = st
	v.Active = st.Employed || (!st.InOrg && active == 1)
	if err := q.QueryRowContext(ctx, `SELECT employee_no, phone FROM org_people WHERE user_id = ?`, uid).Scan(&v.EmpNo, &v.Phone); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	v.Groups = s.userGroups(ctx, q, source, uid)
	var linked, synced string
	err = q.QueryRowContext(ctx, `SELECT external_id, linked_at, last_synced_at FROM scim_links WHERE source = ? AND user_id = ?`, source, uid).Scan(&v.ExternalID, &linked, &synced)
	if err == nil {
		v.Created, v.Modified = parseTime(linked), parseTime(synced)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	return v, nil
}

func (s *Service) location(kind string, id int64) string {
	return s.baseURL + "/scim/v2/" + kind + "/" + itoa(id)
}

func (s *Service) resource(v userView) map[string]any {
	name := map[string]any{"formatted": v.Name}
	r := map[string]any{
		"schemas":     []string{schemaUser, schemaEnt},
		"id":          itoa(v.ID),
		"userName":    v.Email,
		"displayName": v.Name,
		"name":        name,
		"active":      v.Active,
		"emails":      []map[string]any{{"value": v.Email, "type": "work", "primary": true}},
		"meta": map[string]any{"resourceType": "User", "created": v.Created.Format(time.RFC3339), "lastModified": v.Modified.Format(time.RFC3339),
			"location": s.location("Users", v.ID)},
	}
	if v.ExternalID != "" {
		r["externalId"] = v.ExternalID
	}
	if v.State.Rank != "" {
		r["title"] = v.State.Rank
	}
	if len(v.Groups) > 0 {
		r["groups"] = v.Groups
	}
	if v.Phone != "" {
		r["phoneNumbers"] = []map[string]any{{"value": v.Phone, "type": "work"}}
	}
	ent := map[string]any{}
	if v.State.Unit != "" {
		ent["department"] = v.State.Unit
	}
	if v.EmpNo != "" {
		ent["employeeNumber"] = v.EmpNo
	}
	if len(ent) > 0 {
		r[schemaEnt] = ent
	}
	return r
}

// project 는 attributes·excludedAttributes 쿼리를 최상위 속성 이름으로 적용한다(schemas·id 는 항상 남긴다).
func project(r map[string]any, attrs, excl string) map[string]any {
	keep := func(k string) bool { return k == "schemas" || k == "id" }
	if attrs != "" {
		set := map[string]bool{}
		for _, a := range strings.Split(attrs, ",") {
			set[strings.ToLower(strings.TrimSpace(strings.SplitN(a, ".", 2)[0]))] = true
		}
		out := map[string]any{}
		for k, v := range r {
			if keep(k) || set[strings.ToLower(k)] {
				out[k] = v
			}
		}
		return out
	}
	if excl != "" {
		set := map[string]bool{}
		for _, a := range strings.Split(excl, ",") {
			set[strings.ToLower(strings.TrimSpace(a))] = true
		}
		out := map[string]any{}
		for k, v := range r {
			if keep(k) || !set[strings.ToLower(k)] {
				out[k] = v
			}
		}
		return out
	}
	return r
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
