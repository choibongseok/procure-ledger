package scim

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/org"
)

type reqCtx struct {
	tok   tokenRow
	actor contract.Principal
	st    Settings
	today contract.Day
}

// writeResult 는 쓰기 요청 한 건의 결과다(응답·동기화 기록의 재료).
type writeResult struct {
	UID      int64
	Status   int    // 성공 상태(200·201·204)
	Op       string // create reactivate deactivate link update noop
	Email    string
	Ext      string
	Name     string // 커밋 뒤 표시 이름 갱신(발령이 아니라 별도 트랜잭션)
	Phone    string // 커밋 뒤 연락처 갱신(org.UpdateContact, 별도 트랜잭션). PhoneSet 일 때만 쓴다
	PhoneSet bool
	Warnings []string
}

func (s *Service) newReq(ctx context.Context) (*reqCtx, error) {
	tok, ok := tokenFrom(ctx)
	if !ok {
		return nil, errors.New("scim: 인증 문맥이 없습니다")
	}
	st, err := s.loadSettings(ctx, s.db)
	if err != nil {
		return nil, err
	}
	return &reqCtx{tok: tok, actor: newActor(tok), st: st, today: s.org.Today()}, nil
}

// ---- 동기화 기록 ----

type syncEntry struct {
	Method, Resource, Op string
	Status               int
	ScimType, Ext        string
	UserID               int64
	Summary              string
	Warnings             []string
}

func (s *Service) logSync(ctx context.Context, tok tokenRow, e syncEntry) {
	ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if e.Warnings == nil {
		e.Warnings = []string{}
	}
	wj, _ := json.Marshal(e.Warnings)
	var uid any
	if e.UserID != 0 {
		uid = e.UserID
	}
	if _, err := s.db.ExecContext(ctx2, `INSERT INTO scim_sync_log (at, token_id, source, method, resource, op, result, http_status, scim_type, external_id, user_id, summary, warnings)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.stamp(), tok.ID, tok.Source, e.Method, e.Resource, e.Op, resultOf(e.Status), e.Status, e.ScimType, truncate(e.Ext, 255), uid, truncate(e.Summary, 300), string(wj)); err != nil {
		logf("scim: 동기화 기록 실패: %v", err)
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

var auditedReasons = map[string]bool{reasonProt: true, reasonRisk: true, reasonMut: true, reasonUniq: true, reasonLast: true, reasonAdopt: true, reasonSameDay: true}

// runWrite 는 쓰기 요청의 공통 틀이다: 한 트랜잭션, 실패하면 SCIM 오류와 동기화 기록(+중요한 거부는 감사), 성공하면 사용자 리소스 응답.
func (s *Service) runWrite(w http.ResponseWriter, r *http.Request, method, opName string, op func(ctx context.Context, tx contract.Tx, rc *reqCtx) (writeResult, error)) {
	ctx := r.Context()
	rc, err := s.newReq(ctx)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	var res writeResult
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var e error
		res, e = op(ctx, tx, rc)
		return e
	})
	if err != nil {
		ae := toAPIError(err)
		s.logSync(ctx, rc.tok, syncEntry{Method: method, Resource: "User", Op: opName, Status: ae.Status, ScimType: ae.Type, Ext: res.Ext, Summary: ae.Detail})
		if auditedReasons[ae.Reason] {
			s.auditThrottled(ctx, "rej|"+strconv.FormatInt(rc.tok.ID, 10)+"|"+ae.Reason, time.Minute, rc.actor, "scim.rejected", audit.TargetRoute, "/scim/v2/Users", audit.ResultDenied,
				[]audit.DiffItem{{Key: "reason", New: ae.Reason}, {Key: "scim_type", New: ae.Type}, {Key: "token", New: rc.tok.Name}}, ae.Detail)
		}
		writeAPIError(w, ae)
		return
	}
	if res.Name != "" {
		if err := s.org.UpdateProfile(ctx, rc.actor, res.UID, res.Name); err != nil {
			res.Warnings = append(res.Warnings, "표시 이름을 바꾸지 못했습니다: "+toAPIError(err).Detail)
		}
	}
	if res.PhoneSet {
		// 연락처 형식이 맞지 않아도 요청 전체는 성공하고 그 속성만 경고로 남긴다(명세 구현 반영 7).
		if err := s.org.UpdateContact(ctx, rc.actor, res.UID, res.Phone); err != nil {
			res.Warnings = append(res.Warnings, "연락처를 바꾸지 못했습니다: "+toAPIError(err).Detail)
		} else if res.Op == "noop" {
			res.Op = "update"
		}
	}
	if res.Op != "noop" || len(res.Warnings) > 0 {
		sum := summaryOf(res)
		s.logSync(ctx, rc.tok, syncEntry{Method: method, Resource: "User", Op: res.Op, Status: res.Status, Ext: res.Ext, UserID: res.UID, Summary: sum, Warnings: res.Warnings})
	}
	if res.Status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	v, err := s.loadView(ctx, s.db, res.UID, rc.tok.Source, rc.today)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	if res.Status == http.StatusCreated {
		w.Header().Set("Location", s.location("Users", res.UID))
	}
	writeSCIM(w, res.Status, s.resource(v))
}

func summaryOf(r writeResult) string {
	who := maskEmail(r.Email)
	switch r.Op {
	case "create":
		return "입사 발령: " + who
	case "reactivate":
		return "재입사 발령: " + who
	case "deactivate":
		return "퇴사 발령: " + who
	case "link":
		return "기존 사용자 연결: " + who
	case "transfer":
		return "전보 발령: " + who
	case "rank_change":
		return "직위 변경 발령: " + who
	case "update":
		return "정보 갱신: " + who
	}
	return "변경 없음: " + who
}

// ---- 조회 ----

func parseInt(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc, err := s.newReq(ctx)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	q := r.URL.Query()
	conds, fargs, cls, ferr := parseFilter(q.Get("filter"))
	if ferr != nil {
		writeAPIError(w, ferr)
		return
	}
	start := parseInt(q.Get("startIndex"), 1)
	if start < 1 {
		start = 1
	}
	count := parseInt(q.Get("count"), defaultCount)
	if count < 0 {
		count = 0
	}
	if count > maxCount {
		count = maxCount
	}
	marks, rargs := riskArgs()
	where := "l.source = ? AND NOT EXISTS (SELECT 1 FROM user_effective_permissions e WHERE e.user_id = u.id AND e.permission IN (" + marks + "))"
	args := append([]any{rc.tok.Source}, rargs...)
	for _, c := range conds {
		where += " AND " + c
	}
	args = append(args, fargs...)
	base := `FROM users u JOIN scim_links l ON l.user_id = u.id WHERE ` + where
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) `+base, args...).Scan(&total); err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	var ids []int64
	if count > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT u.id `+base+` ORDER BY u.id LIMIT ? OFFSET ?`, append(args, count, start-1)...)
		if err != nil {
			writeAPIError(w, toAPIError(err))
			return
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				writeAPIError(w, toAPIError(err))
				return
			}
			ids = append(ids, id)
		}
		rows.Close()
	}
	// 연결되지 않은 기존 사용자 한 명을 userName 단건 조회에 한해 돌려준다(IdP 의 "있으면 갱신, 없으면 생성" 흐름이 채택으로 이어지게, 명세 7.3).
	if total == 0 && rc.st.AdoptExisting && len(cls) == 1 && strings.EqualFold(cls[0].attr, "userName") && cls[0].op == "eq" {
		if uid, ok := s.adoptable(ctx, s.db, rc, strings.ToLower(cls[0].val)); ok {
			ids, total = []int64{uid}, 1
		}
	}
	res := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		v, err := s.loadView(ctx, s.db, id, rc.tok.Source, rc.today)
		if err != nil {
			writeAPIError(w, toAPIError(err))
			return
		}
		res = append(res, project(s.resource(v), q.Get("attributes"), q.Get("excludedAttributes")))
	}
	writeSCIM(w, http.StatusOK, map[string]any{"schemas": []string{schemaList}, "totalResults": total, "startIndex": start, "itemsPerPage": len(res), "Resources": res})
}

// adoptable 은 이 이메일의 미연결(어느 출처에도 연결되지 않은) 비보호 사용자를 찾는다.
func (s *Service) adoptable(ctx context.Context, q querier, rc *reqCtx, email string) (int64, bool) {
	uid, ok := userByEmail(ctx, q, email)
	if !ok {
		return 0, false
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_links WHERE user_id = ?`, uid).Scan(&n); err != nil || n > 0 {
		return 0, false
	}
	if p, err := s.protected(ctx, q, uid); err != nil || p {
		return 0, false
	}
	return uid, true
}

func (s *Service) getUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rc, err := s.newReq(ctx)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if perr != nil {
		writeAPIError(w, errNotFound("사용자를 찾을 수 없습니다."))
		return
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_links WHERE source = ? AND user_id = ?`, rc.tok.Source, id).Scan(&n); err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	if n == 0 {
		writeAPIError(w, errNotFound("사용자를 찾을 수 없습니다."))
		return
	}
	if p, err := s.protected(ctx, s.db, id); err != nil {
		writeAPIError(w, toAPIError(err))
		return
	} else if p {
		writeAPIError(w, errNotFound("사용자를 찾을 수 없습니다."))
		return
	}
	v, err := s.loadView(ctx, s.db, id, rc.tok.Source, rc.today)
	if err != nil {
		writeAPIError(w, toAPIError(err))
		return
	}
	writeSCIM(w, http.StatusOK, project(s.resource(v), r.URL.Query().Get("attributes"), r.URL.Query().Get("excludedAttributes")))
}

// ---- 도우미 ----

func userByEmail(ctx context.Context, q querier, email string) (int64, bool) {
	var id int64
	if err := q.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(email) = ?`, strings.ToLower(email)).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// protected 는 위험 권한을 실효 권한으로 가진 보호 계정인지다(명세 8.10). SCIM 은 이런 계정을 읽지도 바꾸지도 못한다.
func (s *Service) protected(ctx context.Context, q querier, uid int64) (bool, error) {
	marks, args := riskArgs()
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_effective_permissions WHERE user_id = ? AND permission IN (`+marks+`)`, append([]any{uid}, args...)...).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Service) linkByExternal(ctx context.Context, q querier, source, ext string) (int64, bool, error) {
	var uid int64
	err := q.QueryRowContext(ctx, `SELECT user_id FROM scim_links WHERE source = ? AND external_id = ?`, source, ext).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return uid, err == nil, err
}

func (s *Service) linkedHere(ctx context.Context, q querier, source string, uid int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_links WHERE source = ? AND user_id = ?`, source, uid).Scan(&n)
	return n > 0, err
}

func (s *Service) linkedElsewhere(ctx context.Context, q querier, source string, uid int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_links WHERE source <> ? AND user_id = ?`, source, uid).Scan(&n)
	return n > 0, err
}

func (s *Service) insertLink(ctx context.Context, tx contract.Tx, rc *reqCtx, ext string, uid int64, adopted bool) error {
	now := s.stamp()
	if _, err := tx.ExecContext(ctx, `INSERT INTO scim_links (source, external_id, user_id, linked_at, last_synced_at) VALUES (?,?,?,?,?)`, rc.tok.Source, ext, uid, now, now); err != nil {
		if dbutil.IsUnique(err) {
			return errUniqueness("이미 연결된 외부 식별자(externalId)입니다.", reasonUniq)
		}
		return err
	}
	return s.record(ctx, tx, rc.actor, "scim.link", audit.TargetUser, strconv.FormatInt(uid, 10), audit.ResultSuccess,
		[]audit.DiffItem{{Key: "source", New: rc.tok.Source}, {Key: "external_id_tail", New: tail4(ext)}, {Key: "adopted", New: boolText(adopted)}}, "")
}

func (s *Service) touchLink(ctx context.Context, tx contract.Tx, rc *reqCtx, uid int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE scim_links SET last_synced_at = ? WHERE source = ? AND user_id = ?`, s.stamp(), rc.tok.Source, uid)
	return err
}

func (s *Service) reasonText(op string) string { return "SCIM 동기화: " + op }

// hireCmd 는 입사(또는 재입사) 명령을 만든다. 부서·직위는 속성 → 기본값 순이고, 둘 다 없거나 조직·직위 표에 없으면 거부한다(명세 8.4, D5·D6).
func (s *Service) hireCmd(ctx context.Context, tx contract.Tx, rc *reqCtx, in *userIn, uid int64, email, name string) (org.Command, error) {
	unitRef := in.department()
	if rc.st.GroupMode == "unit" { // 조직 그룹 모드: 주 소속은 그룹 소속이 정한다(부서 속성은 무시)
		unitRef = ""
		if uid != 0 {
			unitRef = s.memberUnitRef(ctx, tx, rc.tok.Source, uid)
		}
	}
	if unitRef == "" {
		unitRef = rc.st.DefaultUnit
	}
	if unitRef == "" {
		return org.Command{}, errInvalidValue("부서 정보가 없습니다. 부서(department)를 보내거나 관리자 화면에서 기본 조직을 정하세요.")
	}
	unitID, err := s.org.ResolveUnitRef(ctx, tx, unitRef)
	if err != nil {
		if errors.Is(err, org.ErrUnitNotFound) {
			return org.Command{}, errInvalidValue("조직 '" + unitRef + "' 을(를) 찾을 수 없습니다. 조직을 먼저 만들거나 조직 코드를 확인하세요.")
		}
		return org.Command{}, errInvalidValue(err.Error())
	}
	rankName := str(in.Title)
	if rankName == "" {
		rankName = rc.st.DefaultRank
	}
	if rankName == "" {
		return org.Command{}, errInvalidValue("직위 정보가 없습니다. 직위(title)를 보내거나 관리자 화면에서 기본 직위를 정하세요.")
	}
	rid, err := rankID(ctx, tx, rankName)
	if err != nil {
		return org.Command{}, errInvalidValue("직위 '" + rankName + "' 이(가) 직위 표에 없습니다.")
	}
	phone, _ := in.workPhone()
	cmd := org.Command{Kind: org.KindHire, UserID: uid, ToUnit: unitID, Rank: rid, Phone: phone,
		Role: contract.Role(rc.st.DefaultRole), Reason: s.reasonText(rc.tok.Source + " 입사")}
	if uid == 0 {
		cmd.Email, cmd.Name = email, name
		cmd.EmployeeNo = str(in.employeeNumber())
	}
	return cmd, nil
}

// syncActive 는 active 값을 재직 상태에 맞춘다: true → 입사·재입사, false → 퇴사. 이미 그 상태이면 변경 없음(멱등).
func (s *Service) syncActive(ctx context.Context, tx contract.Tx, rc *reqCtx, v userView, want bool, in *userIn, res *writeResult) error {
	if want == v.Active {
		return nil
	}
	if want {
		cmd, err := s.hireCmd(ctx, tx, rc, in, v.ID, v.Email, v.Name)
		if err != nil {
			return err
		}
		cmd.Reason = s.reasonText(rc.tok.Source + " 재입사")
		if _, err := s.org.RegisterBatch(ctx, tx, rc.actor, []org.Command{cmd}, true); err != nil {
			return err
		}
		res.Op = "reactivate"
		return nil
	}
	if !v.State.InOrg {
		return errConflict("조직에 등록되지 않은 사용자라 퇴사 발령을 할 수 없습니다. 인사 발령 화면에서 처리하세요.", "")
	}
	if v.State.HireDay != "" && v.State.HireDay >= string(rc.today) {
		return errConflict("입사 당일에는 퇴사 발령을 낼 수 없습니다. 다음 날 다시 전송되거나, 인사 발령 화면에서 처리하세요.", reasonSameDay)
	}
	var head int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_unit_spans WHERE head_user_id = ? AND head_acting = 0 AND closed = 0 AND valid_from <= ? AND (valid_to IS NULL OR ? < valid_to)`,
		v.ID, string(rc.today), string(rc.today)).Scan(&head); err == nil && head > 0 {
		res.Warnings = append(res.Warnings, "조직장 공석: 퇴사한 사람이 조직장이었습니다")
	}
	if n := pendingApprovals(ctx, tx, v.ID); n > 0 {
		res.Warnings = append(res.Warnings, "진행 중 결재 "+itoa(int64(n))+"건이 퇴사한 사람의 결재 차례였습니다(재지정이 필요합니다)")
	}
	cmd := org.Command{Kind: org.KindLeave, UserID: v.ID, LastDay: org.AddDays(rc.today, -1), Reason: s.reasonText(rc.tok.Source + " 퇴사")}
	outs, err := s.org.RegisterBatch(ctx, tx, rc.actor, []org.Command{cmd}, true)
	if err != nil {
		return err
	}
	for _, o := range outs {
		res.Warnings = append(res.Warnings, o.Warnings...)
	}
	res.Op = "deactivate"
	return nil
}

// updateExisting 은 연결된 사용자에게 요청의 속성을 적용한다(1단계: active 와 표시 이름. 전보·직위·연락처 변경은 무시하고 경고).
func (s *Service) updateExisting(ctx context.Context, tx contract.Tx, rc *reqCtx, uid int64, in *userIn, res writeResult) (writeResult, error) {
	res.UID = uid
	if p, err := s.protected(ctx, tx, uid); err != nil {
		return res, err
	} else if p {
		return res, errMutability("위험 권한이 있는 계정은 SCIM 으로 바꿀 수 없습니다.", reasonProt)
	}
	v, err := s.loadView(ctx, tx, uid, rc.tok.Source, rc.today)
	if err != nil {
		return res, err
	}
	res.Email = v.Email
	if un := strings.ToLower(str(in.UserName)); un != "" && un != strings.ToLower(v.Email) {
		return res, errMutability("userName(이메일)은 바꿀 수 없습니다.", reasonMut)
	}
	if en := in.employeeNumber(); en != nil && str(en) != "" && v.EmpNo != "" && str(en) != v.EmpNo {
		return res, errMutability("사번(employeeNumber)은 바꿀 수 없습니다.", reasonMut)
	}
	want, set, aerr := in.activeOf()
	if aerr != nil {
		return res, aerr
	}
	res.Op = "noop"
	if set {
		if err := s.syncActive(ctx, tx, rc, v, want, in, &res); err != nil {
			return res, err
		}
	}
	if name := in.displayNameOf(); name != "" && name != v.Name {
		res.Name = name
		if res.Op == "noop" {
			res.Op = "update"
		}
	}
	if res.Op != "reactivate" && res.Op != "deactivate" {
		if err := s.syncAttrs(ctx, tx, rc, v, in, &res); err != nil {
			return res, err
		}
	}
	res.Warnings = append(res.Warnings, in.ignoredAttrs()...)
	if err := s.touchLink(ctx, tx, rc, uid); err != nil {
		return res, err
	}
	if res.Status == 0 {
		res.Status = http.StatusOK
	}
	return res, nil
}

// ---- 쓰기 핸들러 ----

func decodeUser(r *http.Request) (*userIn, *apiError) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, &apiError{Status: 413, Type: "tooMany", Detail: "요청 본문이 너무 큽니다(1MiB 이하)."}
		}
		return nil, &apiError{Status: 400, Type: "invalidSyntax", Detail: "요청 본문을 읽을 수 없습니다."}
	}
	in := &userIn{}
	if err := json.Unmarshal(b, in); err != nil {
		return nil, &apiError{Status: 400, Type: "invalidSyntax", Detail: "JSON 형식이 올바르지 않습니다."}
	}
	return in, nil
}

func (s *Service) createUser(w http.ResponseWriter, r *http.Request) {
	in, ae := decodeUser(r)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	s.runWrite(w, r, http.MethodPost, "create", func(ctx context.Context, tx contract.Tx, rc *reqCtx) (writeResult, error) {
		email := strings.ToLower(str(in.UserName))
		if !validEmail(email) {
			return writeResult{}, errInvalidValue("userName 은 이메일 형식이어야 합니다.")
		}
		ext := str(in.ExternalID)
		if ext == "" {
			ext = email // externalId 를 보내지 않는 IdP(Okta 일부)는 이메일을 식별자로 쓴다
		}
		res := writeResult{Email: email, Ext: ext, Warnings: in.ignoredAttrs()}
		if len(ext) > 255 {
			return res, errInvalidValue("externalId 가 너무 깁니다.")
		}
		if err := dbutil.AcquireKeyLock(ctx, tx, "scim:link:"+rc.tok.Source+":"+ext); err != nil {
			return res, err
		}
		if uid, found, err := s.linkByExternal(ctx, tx, rc.tok.Source, ext); err != nil {
			return res, err
		} else if found { // 멱등: 같은 externalId 의 재전송은 기존 사용자 갱신으로 받는다
			v, err := s.loadView(ctx, tx, uid, rc.tok.Source, rc.today)
			if err != nil {
				return res, err
			}
			if !strings.EqualFold(v.Email, email) {
				return res, errUniqueness("같은 externalId 의 사용자 이메일이 다릅니다. 이메일 변경은 지원하지 않습니다.", reasonUniq)
			}
			out, err := s.updateExisting(ctx, tx, rc, uid, in, res)
			out.Ext = ext
			return out, err
		}
		want, set, aerr := in.activeOf()
		if aerr != nil {
			return res, aerr
		}
		uid, exists := userByEmail(ctx, tx, email)
		if !exists {
			if set && !want {
				return res, errInvalidValue("비활성 상태로는 새 사용자를 만들 수 없습니다. 활성 상태가 된 뒤에 전송하세요.")
			}
			name := in.displayNameOf()
			if name == "" {
				return res, errInvalidValue("이름(displayName 또는 name)이 필요합니다.")
			}
			cmd, err := s.hireCmd(ctx, tx, rc, in, 0, email, name)
			if err != nil {
				return res, err
			}
			outs, err := s.org.RegisterBatch(ctx, tx, rc.actor, []org.Command{cmd}, true)
			if err != nil {
				return res, err
			}
			res.UID = outs[0].UserID
			res.Warnings = append(res.Warnings, outs[0].Warnings...)
			if err := s.insertLink(ctx, tx, rc, ext, res.UID, false); err != nil {
				return res, err
			}
			res.Op, res.Status = "create", http.StatusCreated
			return res, nil
		}
		// 같은 이메일의 기존 사용자: 채택하거나 충돌로 거부한다.
		if p, err := s.protected(ctx, tx, uid); err != nil {
			return res, err
		} else if p {
			return res, errUniqueness("같은 이메일의 계정이 있고 위험 권한이 있어 SCIM 으로 연결할 수 없습니다.", reasonProt)
		}
		if el, err := s.linkedElsewhere(ctx, tx, rc.tok.Source, uid); err != nil {
			return res, err
		} else if el {
			return res, errUniqueness("같은 이메일의 사용자가 다른 출처에 이미 연결되어 있습니다.", reasonUniq)
		}
		if here, err := s.linkedHere(ctx, tx, rc.tok.Source, uid); err != nil {
			return res, err
		} else if here {
			return res, errUniqueness("같은 이메일의 사용자가 다른 externalId 로 이미 연결되어 있습니다.", reasonUniq)
		}
		if !rc.st.AdoptExisting {
			return res, errUniqueness("같은 이메일의 사용자가 이미 있습니다(기존 사용자 연결이 꺼져 있습니다).", reasonAdopt)
		}
		if err := s.insertLink(ctx, tx, rc, ext, uid, true); err != nil {
			return res, err
		}
		out, err := s.updateExisting(ctx, tx, rc, uid, in, res)
		if err != nil {
			return out, err
		}
		out.Ext = ext
		if out.Op == "noop" || out.Op == "update" {
			out.Op = "link"
		}
		out.Status = http.StatusCreated
		return out, nil
	})
}

// target 은 경로의 사용자 번호를 이 출처의 연결 사용자로 풀어 준다. 미연결 사용자는 채택 설정이 켜져 있고 allowAdopt 일 때만 연결한다.
func (s *Service) target(ctx context.Context, tx contract.Tx, rc *reqCtx, idText string, in *userIn, allowAdopt bool) (int64, error) {
	uid, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		return 0, errNotFound("사용자를 찾을 수 없습니다.")
	}
	here, err := s.linkedHere(ctx, tx, rc.tok.Source, uid)
	if err != nil {
		return 0, err
	}
	if here {
		return uid, nil
	}
	if !allowAdopt || !rc.st.AdoptExisting {
		return 0, errNotFound("사용자를 찾을 수 없습니다.")
	}
	var email string
	if err := tx.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, uid).Scan(&email); err != nil {
		return 0, errNotFound("사용자를 찾을 수 없습니다.")
	}
	if got, ok := s.adoptable(ctx, tx, rc, email); !ok || got != uid {
		return 0, errNotFound("사용자를 찾을 수 없습니다.")
	}
	ext := ""
	if in != nil {
		ext = str(in.ExternalID)
	}
	if ext == "" {
		ext = strings.ToLower(email)
	}
	if err := s.insertLink(ctx, tx, rc, ext, uid, true); err != nil {
		return 0, err
	}
	return uid, nil
}

func (s *Service) replaceUser(w http.ResponseWriter, r *http.Request) {
	in, ae := decodeUser(r)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	s.runWrite(w, r, http.MethodPut, "update", func(ctx context.Context, tx contract.Tx, rc *reqCtx) (writeResult, error) {
		uid, err := s.target(ctx, tx, rc, r.PathValue("id"), in, true)
		if err != nil {
			return writeResult{}, err
		}
		if err := dbutil.AcquireKeyLock(ctx, tx, "scim:user:"+strconv.FormatInt(uid, 10)); err != nil {
			return writeResult{}, err
		}
		return s.updateExisting(ctx, tx, rc, uid, in, writeResult{})
	})
}

func (s *Service) deleteUser(w http.ResponseWriter, r *http.Request) {
	s.runWrite(w, r, http.MethodDelete, "deactivate", func(ctx context.Context, tx contract.Tx, rc *reqCtx) (writeResult, error) {
		uid, err := s.target(ctx, tx, rc, r.PathValue("id"), nil, false)
		if err != nil {
			return writeResult{}, err
		}
		if err := dbutil.AcquireKeyLock(ctx, tx, "scim:user:"+strconv.FormatInt(uid, 10)); err != nil {
			return writeResult{}, err
		}
		res := writeResult{UID: uid, Op: "noop", Status: http.StatusNoContent}
		if p, err := s.protected(ctx, tx, uid); err != nil {
			return res, err
		} else if p {
			return res, errMutability("위험 권한이 있는 계정은 SCIM 으로 바꿀 수 없습니다.", reasonProt)
		}
		v, err := s.loadView(ctx, tx, uid, rc.tok.Source, rc.today)
		if err != nil {
			return res, err
		}
		res.Email = v.Email
		if err := s.syncActive(ctx, tx, rc, v, false, &userIn{}, &res); err != nil {
			return res, err
		}
		if err := s.touchLink(ctx, tx, rc, uid); err != nil {
			return res, err
		}
		return res, nil
	})
}

// pendingApprovals 는 이 사람이 지금 처리자인 진행 중 결재 단계 수다(퇴사·전보 경고용). 정의는 org 의 같은 이름 함수와 같다(뷰 approval_step_current).
func pendingApprovals(ctx context.Context, q querier, uid int64) int {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_steps s
		JOIN approval_step_current c ON c.pr_id = s.pr_id AND c.round = s.round AND c.seq = s.seq
		JOIN purchase_requests p ON p.id = s.pr_id AND p.round = s.round
		WHERE s.status = 'pending' AND c.user_id = ? AND p.status IN ('submitted','in_review')`, uid).Scan(&n); err != nil {
		return 0
	}
	return n
}

// headOf 는 이 사람이 조직장인 조직 수다(직무대행 제외, 오늘 기준).
func (s *Service) headOf(ctx context.Context, q querier, uid int64, today contract.Day) int {
	var n int
	d := string(today)
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_unit_spans WHERE head_user_id = ? AND head_acting = 0 AND closed = 0 AND valid_from <= ? AND (valid_to IS NULL OR ? < valid_to)`, uid, d, d).Scan(&n); err != nil {
		return 0
	}
	return n
}

// syncAttrs 는 재직 중인 사용자의 부서·직위·연락처 변경을 반영한다(명세 8.3·8.4). 부서·직위는 기존 발령 경로(전보·직위 변경, 효력일은 오늘)이고
// 한 요청의 두 발령은 RegisterBatch 한 번에 묶인다. 없는 부서·직위는 400 이며 아무것도 바뀌지 않는다. 연락처는 발령이 아니라 org_people 갱신과 감사 user.update 다.
func (s *Service) syncAttrs(ctx context.Context, tx contract.Tx, rc *reqCtx, v userView, in *userIn, res *writeResult) error {
	dept, title := in.department(), str(in.Title)
	phone, phoneSet := in.workPhone()
	if !v.State.Employed {
		if (dept != "" || title != "") && v.State.InOrg {
			res.Warnings = append(res.Warnings, "퇴사 상태인 사용자의 부서·직위 변경은 재입사 때 반영합니다")
		}
		return nil
	}
	var cmds []org.Command
	transfer := false
	if rc.st.GroupMode == "unit" { // 조직 그룹 모드에서는 부서 속성을 무시한다(원천이 둘이면 서로 덮어쓴다)
		dept = ""
	}
	if dept != "" && dept != v.State.Unit && dept != v.State.UnitCode {
		unitID, err := s.org.ResolveUnitRef(ctx, tx, dept)
		if err != nil {
			if errors.Is(err, org.ErrUnitNotFound) {
				return errInvalidValue("조직 '" + dept + "' 을(를) 찾을 수 없습니다. 조직을 먼저 만들거나 조직 코드를 확인하세요.")
			}
			return errInvalidValue(err.Error())
		}
		if unitID != v.State.UnitID {
			cmds = append(cmds, org.Command{Kind: org.KindTransfer, UserID: v.ID, ToUnit: unitID, Reason: s.reasonText(rc.tok.Source + " 전보")})
			transfer = true
		}
	}
	if title != "" && title != v.State.Rank {
		rid, err := rankID(ctx, tx, title)
		if err != nil {
			return errInvalidValue("직위 '" + title + "' 이(가) 직위 표에 없습니다.")
		}
		if rid != v.State.RankID {
			cmds = append(cmds, org.Command{Kind: org.KindRankChange, UserID: v.ID, Rank: rid, Reason: s.reasonText(rc.tok.Source + " 직위 변경")})
		}
	}
	if len(cmds) > 0 {
		if transfer {
			if n := s.headOf(ctx, tx, v.ID, rc.today); n > 0 {
				res.Warnings = append(res.Warnings, "조직장 공석: 전보로 조직장 자리가 비었습니다")
			}
			if n := pendingApprovals(ctx, tx, v.ID); n > 0 {
				res.Warnings = append(res.Warnings, "진행 중 결재 "+itoa(int64(n))+"건이 이 사람의 결재 차례입니다(전보 뒤 결재선이 달라질 수 있습니다)")
			}
		}
		outs, err := s.org.RegisterBatch(ctx, tx, rc.actor, cmds, true)
		if err != nil {
			return err
		}
		for _, o := range outs {
			res.Warnings = append(res.Warnings, o.Warnings...)
		}
		if transfer {
			res.Op = "transfer"
		} else {
			res.Op = "rank_change"
		}
	}
	if phoneSet {
		if np := normPhone(phone); np != normPhone(v.Phone) {
			res.Phone, res.PhoneSet = np, true
		}
	}
	return nil
}

// normPhone 은 IdP 가 보내는 전화번호의 공백·괄호·점을 하이픈 하나로 맞춘다(org 의 전화번호 형식은 숫자·하이픈·앞 +). 모르는 문자가 있으면 그대로 돌려줘 org 검증이 거른다.
func normPhone(v string) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	lastHyphen := false
	for _, r := range v {
		switch {
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' || r == '\u00a0':
			if b.Len() > 0 && !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		default:
			return v
		}
	}
	return strings.Trim(b.String(), "-")
}
