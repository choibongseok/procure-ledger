package org

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 조직 CSV 일괄 반영의 미리보기·반영(명세 6.4~6.7). 파일은 저장하지 않는다. 반영 때 같은 파일을 다시 올리고
// 서명된 previewId(HMAC-SHA256: 파일 해시·조직 상태 지문·반영 기준일·발급 사용자·발급 시각)로 같은 파일·같은 상태임을 확인한다.

const (
	importTokenTTL  = 10 * time.Minute
	importAuditMax  = 200 // 감사 상세에 담는 이벤트 수 상한
	importErrorsMax = 200 // 미리보기에 보이는 오류 수 상한
	importCulprits  = 25  // 일괄 검증이 실패했을 때 행으로 짚는 오류 수 상한
	importAutoNote  = "CSV 일괄 반영"
)

// ImportConflict 는 미리보기와 반영 사이에 상태가 바뀌었을 때의 오류다(HTTP 409).
type ImportConflict struct{ Msg string }

func (e *ImportConflict) Error() string        { return e.Msg }
func (e *ImportConflict) Is(target error) bool { return target == contract.ErrInvalidState }

// ImportNeedConfirm 은 30일을 넘는 소급이 들어 있는데 확인이 없을 때의 오류다.
type ImportNeedConfirm struct{ Msg string }

func (e *ImportNeedConfirm) Error() string        { return e.Msg }
func (e *ImportNeedConfirm) Is(target error) bool { return target == contract.ErrInvalidState }

// ImpactDoc 는 결재 진행 중 건 하나다.
type ImpactDoc struct {
	ID        int64
	No        string
	Title     string
	Requester string
}

// ImpactRow 는 퇴사·전보·조직 폐지·조직장 해임 대상 중 "지금 처리할 차례인 진행 중 건"이 있는 사람이다.
type ImpactRow struct {
	Who   string
	Why   string // 구분(퇴사 2026. 11. 30. 등)
	Count int
	Docs  []ImpactDoc
}

// ImportPreview 는 미리보기 결과다. 오류가 있으면 PreviewID 는 비어 있다.
type ImportPreview struct {
	PreviewID   string
	ExpiresAt   time.Time
	Base        contract.Day
	Files       []ImportFileInfo
	Items       []PlanItem
	Counts      map[string]int
	Errors      []ImportRowError // 처음 200개
	MoreErrors  int
	FileErrors  []ImportRowError
	Warnings    []string
	Impact      []ImpactRow
	ImpactMore  int
	RetroRows   []string
	NoChangeAll bool // 이벤트가 0건(반영할 변경 없음)
}

// ImportResult 는 반영 결과다.
type ImportResult struct {
	Batch  int64
	Events int
	Counts map[string]int
}

type importToken struct {
	Files map[string]string `json:"f"` // 파일 종류 → SHA-256
	FP    int64             `json:"p"` // 조직 상태 지문(max(org_events.id))
	Base  contract.Day      `json:"b"`
	User  int64             `json:"u"`
	At    int64             `json:"t"`
}

func (s *Service) importKeyBytes() []byte {
	s.importOnce.Do(func() {
		s.importKey = make([]byte, 32)
		_, _ = rand.Read(s.importKey)
		s.importUsed = map[string]bool{}
	})
	return s.importKey
}

func (s *Service) signImport(t importToken) string {
	body, _ := json.Marshal(t)
	mac := hmac.New(sha256.New, s.importKeyBytes())
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) verifyImport(id string, uid int64) (importToken, error) {
	var t importToken
	parts := strings.SplitN(id, ".", 2)
	if len(parts) != 2 {
		return t, &ImportConflict{"미리보기가 올바르지 않습니다. 다시 미리보기 하세요."}
	}
	body, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return t, &ImportConflict{"미리보기가 올바르지 않습니다. 다시 미리보기 하세요."}
	}
	mac := hmac.New(sha256.New, s.importKeyBytes())
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		// 서버를 다시 시작하면 서명 키가 바뀌므로 이전 미리보기는 여기서 걸린다.
		return t, &ImportConflict{"미리보기가 만료되었습니다. 다시 미리보기 하세요."}
	}
	if json.Unmarshal(body, &t) != nil || t.User != uid {
		return t, &ImportConflict{"미리보기가 올바르지 않습니다. 다시 미리보기 하세요."}
	}
	if s.now().Sub(time.Unix(t.At, 0)) > importTokenTTL {
		return t, &ImportConflict{"미리보기가 만료되었습니다. 다시 미리보기 하세요."}
	}
	s.importMu.Lock()
	used := s.importUsed[id]
	s.importMu.Unlock()
	if used {
		return t, &ImportConflict{"이미 반영된 미리보기입니다. 같은 파일을 다시 올려 미리보기 하세요."}
	}
	return t, nil
}

func (s *Service) orgFingerprint(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM org_events`).Scan(&n)
	return n, err
}

func planCommands(items []PlanItem, confirm bool, batch int64, defaultReason bool) []Command {
	cmds := make([]Command, len(items))
	for i, it := range items {
		c := it.Cmd
		c.Confirm = confirm
		c.ImportBatch = batch
		if defaultReason && strings.TrimSpace(c.Reason) == "" {
			c.Reason = importAutoNote
		}
		cmds[i] = c
	}
	return cmds
}

// dryRun 은 명령 앞 k 개를 롤백 전제로 일괄 등록해 본다(저장·감사 없음). 영향 계산에 쓰는 결과도 돌려준다.
func (s *Service) dryRun(ctx context.Context, p contract.Principal, cmds []Command) (*Result, *Preview, error) {
	var res *Result
	pv := &Preview{}
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		_, r, err := s.applyCommands(ctx, tx, p, append([]Command(nil), cmds...), false, nil, pv)
		if err != nil {
			return err
		}
		res = r
		return errPreviewDone
	})
	s.invalidate()
	if errors.Is(err, errPreviewDone) {
		return res, pv, nil
	}
	return nil, nil, err
}

// PreviewImport 는 파일을 읽어 계획을 만들고 실제 등록 경로의 검증을 롤백 전제로 돌려 본다.
func (s *Service) PreviewImport(ctx context.Context, p contract.Principal, in ImportInput) (*ImportPreview, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermOrgManage); err != nil {
		return nil, err
	}
	base := s.Today()
	plan, err := s.BuildImportPlan(ctx, in, base)
	if err != nil {
		return nil, err
	}
	out := &ImportPreview{Base: base, Files: plan.Files, Counts: plan.Counts, FileErrors: plan.FileErrors, RetroRows: plan.RetroRows}
	errs := append([]ImportRowError(nil), plan.Errors...)
	if len(plan.FileErrors) == 0 && len(errs) == 0 {
		// 일괄 등록 검증(Fold 포함): 실패하면 앞 k 개 이분 탐색으로 실패한 행을 짚는다.
		items := append([]PlanItem(nil), plan.Items...)
		for round := 0; round < importCulprits && len(items) > 0; round++ {
			if _, _, err := s.dryRun(ctx, p, planCommands(items, true, 0, true)); err == nil {
				break
			} else if isInternal(err) {
				return nil, err
			}
			lo, hi := 1, len(items)
			var cause error
			for lo < hi {
				mid := (lo + hi) / 2
				if _, _, e := s.dryRun(ctx, p, planCommands(items[:mid], true, 0, true)); e != nil {
					hi, cause = mid, e
				} else {
					lo = mid + 1
				}
			}
			if _, _, e := s.dryRun(ctx, p, planCommands(items[:lo], true, 0, true)); e != nil {
				cause = e
			}
			bad := items[lo-1]
			errs = append(errs, ImportRowError{File: bad.File, Line: bad.Line, Msg: businessMsg(cause)})
			items = append(items[:lo-1], items[lo:]...)
		}
		plan.Items = items
	}
	sort.SliceStable(errs, func(i, j int) bool {
		a, b := errs[i], errs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	if len(errs) > importErrorsMax {
		out.MoreErrors = len(errs) - importErrorsMax
		errs = errs[:importErrorsMax]
	}
	out.Errors = errs
	if plan.HasProblems() || len(errs) > 0 {
		return out, nil
	}
	out.Items = plan.Items
	out.NoChangeAll = len(plan.Items) == 0
	cmds := planCommands(plan.Items, true, 0, true)
	if len(cmds) > 0 {
		res, _, err := s.dryRun(ctx, p, cmds)
		if err != nil {
			return nil, err
		}
		pl, err := s.newPlanner(ctx, plan, base)
		if err != nil {
			return nil, err
		}
		out.Warnings, err = s.importWarnings(ctx, pl, plan, res)
		if err != nil {
			return nil, err
		}
		var shownUsers []int64
		out.Impact, out.ImpactMore, shownUsers, err = s.importImpact(ctx, pl, plan)
		if err != nil {
			return nil, err
		}
		// 영향 목록은 "이름(사번)" 으로 기존 구성원의 사번을 보인다: 사번이 응답에 실리므로 기록한다(B41, D5). 기록하지 못하면 미리보기를 내주지 않는다.
		if err := s.notePrivacy(ctx, p, privacy.Access{Category: privacy.CatEmployeeNo, Screen: privacy.ScreenImportPreview, Fields: []string{privacy.FieldEmployeeNo},
			Subjects: userSubjects(p, shownUsers)}); err != nil {
			return nil, err
		}
	}
	out.Warnings = append(out.Warnings, plan.IgnoredRoleRows...) // 변경이 없는 파일에도 안내한다
	if !out.NoChangeAll {
		fp, err := s.orgFingerprint(ctx, s.db)
		if err != nil {
			return nil, err
		}
		tok := importToken{Files: map[string]string{}, FP: fp, Base: base, User: p.UserID, At: s.now().Unix()}
		for _, f := range plan.Files {
			tok.Files[f.Kind] = f.SHA256
		}
		out.PreviewID = s.signImport(tok)
		out.ExpiresAt = s.now().Add(importTokenTTL)
	}
	return out, nil
}

// isInternal 은 업무 문구가 아닌 서버 오류인지다(그대로 위로 올린다).
func isInternal(err error) bool {
	var ue userError
	var fe *FoldError
	var fue *foldUserError
	var cc *cancelConflict
	return !(errors.As(err, &ue) || errors.As(err, &fe) || errors.As(err, &fue) || errors.As(err, &cc) || errors.Is(err, contract.ErrValidation) || errors.Is(err, contract.ErrInvalidState))
}

func businessMsg(err error) string {
	return err.Error()
}

// importWarnings 는 반영은 되지만 알려야 할 것을 모은다(명세 6.4 의 5): 결재 권한 없는 역할의 조직장, 조직장 자동 공석, 겸직 한도 근접.
func (s *Service) importWarnings(ctx context.Context, pl *planner, plan *ImportPlan, after *Result) ([]string, error) {
	var out []string
	// 반영 뒤의 모습은 이번 묶음의 마지막 효력일 기준이다(예약 퇴사·전보까지 반영된 상태).
	far := plan.Base
	for _, it := range plan.Items {
		if it.Eff > far {
			far = it.Eff
		}
	}
	sn := NewSnap(after, far)
	// 이번 묶음의 조직장 보임·직무대행 대상 중 결재 권한이 없는 역할
	for _, it := range plan.Items {
		if it.Cmd.Kind != KindHeadSet && it.Cmd.Kind != KindActingSet {
			continue
		}
		decide, roleText, name := true, "", ""
		if it.Cmd.UserID != 0 {
			decide, roleText, name = pl.userDecide[it.Cmd.UserID], pl.userRole[it.Cmd.UserID], pl.userName[it.Cmd.UserID]
		} else if it.Cmd.UserEmp != "" {
			name = it.Cmd.UserEmp
			for _, h := range plan.Items {
				if h.Cmd.Kind == KindHire && h.Cmd.EmployeeNo == it.Cmd.UserEmp {
					key := string(h.Cmd.Role)
					decide, roleText, name = pl.roleDecide[key], pl.roleNameByKey[key], strings.SplitN(h.Target, "(", 2)[0]
				}
			}
		}
		if !decide {
			unit := it.Target
			out = append(out, fmt.Sprintf("%s: %s의 권한 역할이 '%s'라서 결재 권한이 없습니다. 결재선에서 건너뛰어지거나 그 조직 직원의 상신이 막힐 수 있습니다.", unit, name, roleText))
		}
	}
	// 조직장이 있던 조직이 반영 뒤 공석이 되는 경우
	before, err := s.snapAt(ctx, plan.Base)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for id := range before.Units {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		b := before.Units[id]
		a, ok := sn.Units[id]
		if !ok || b.Closed || b.HeadUser == 0 || b.HeadActing {
			continue
		}
		if a.HeadUser == 0 && !a.Closed {
			out = append(out, fmt.Sprintf("%s: 조직장이 반영 뒤 공석이 됩니다. 직무대행을 따로 지정하세요.", b.Name))
		}
	}
	// 겸직 한도 근접
	var users []int64
	for u := range sn.Conc {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	for _, u := range users {
		if len(sn.Conc[u]) >= MaxConcurrent && len(before.Conc[u]) < MaxConcurrent {
			name := pl.userName[u]
			if name == "" {
				name = fmt.Sprintf("사용자 #%d", u)
			}
			out = append(out, fmt.Sprintf("%s: 겸직 %d곳이 되어 겸직 한도(%d곳)에 닿습니다.", name, len(sn.Conc[u]), MaxConcurrent))
		}
	}
	return out, nil
}

// importImpact 는 퇴사·전보·조직 폐지·조직장 해임 대상 중 지금 결재(합의)할 차례인 진행 중 건이 있는 사람을 찾는다.
// 건수는 B20 퇴사·전보 미리보기와 같은 정의(pendingApprovals)다. 최대 20건까지 문서를 보이고 넘으면 개수만 돌려준다.
func (s *Service) importImpact(ctx context.Context, pl *planner, plan *ImportPlan) ([]ImpactRow, int, []int64, error) {
	var rows []ImpactRow
	var users []int64
	seen := map[string]bool{}
	for _, it := range plan.Items {
		var uid int64
		why := ""
		switch it.Cmd.Kind {
		case KindLeave:
			uid, why = it.Cmd.UserID, "퇴사 "+DayText(it.Cmd.LastDay)
		case KindTransfer:
			uid, why = it.Cmd.UserID, "전보 "+DayText(it.Eff)
		case KindUnitClose, KindHeadClear:
			if u, ok := pl.unitByCde[it.Cmd.UnitCode]; ok && u.HeadUser != 0 && !u.HeadActing {
				uid, why = u.HeadUser, it.Label+" "+DayText(it.Eff)
			}
		}
		if uid == 0 {
			continue
		}
		key := fmt.Sprint(uid, why)
		if seen[key] {
			continue
		}
		seen[key] = true
		n := pendingApprovals(ctx, s.db, uid)
		if n == 0 {
			continue
		}
		docs, err := pendingDocs(ctx, s.db, uid, 20)
		if err != nil {
			return nil, 0, nil, err
		}
		users = append(users, uid)
		rows = append(rows, ImpactRow{Who: fmt.Sprintf("%s(%s)", pl.userName[uid], pl.userEmp[uid]), Why: why, Count: n, Docs: docs})
	}
	shown, more := 0, 0
	for i := range rows {
		keep := rows[i].Docs[:0]
		for _, d := range rows[i].Docs {
			if shown < 20 {
				keep = append(keep, d)
				shown++
			} else {
				more++
			}
		}
		rows[i].Docs = keep
		if rows[i].Count > len(rows[i].Docs) && len(rows[i].Docs) == 0 {
			// 문서 제목은 상한 때문에 못 보였지만 건수는 있다
		}
	}
	return rows, more, users, nil
}

// pendingDocs 는 사람이 지금 처리할 차례인 진행 중 문서(최대 limit 건)다. q 는 트랜잭션 안에서는 그 트랜잭션이어야 한다(연결이 하나다).
func pendingDocs(ctx context.Context, q Querier, userID int64, limit int) ([]ImpactDoc, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.no, p.title, u.name FROM approval_steps st
		JOIN approval_step_current c ON c.pr_id = st.pr_id AND c.round = st.round AND c.seq = st.seq
		JOIN purchase_requests p ON p.id = st.pr_id AND p.round = st.round
		JOIN users u ON u.id = p.requester_id
		WHERE st.status = 'pending' AND c.user_id = ? AND p.status IN ('submitted','in_review') ORDER BY p.id LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImpactDoc
	for rows.Next() {
		var d ImpactDoc
		if err := rows.Scan(&d.ID, &d.No, &d.Title, &d.Requester); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ApplyImport 는 같은 파일로 미리보기와 같은 계획을 만들어 한 트랜잭션에서 반영한다(전부 아니면 전무).
func (s *Service) ApplyImport(ctx context.Context, p contract.Principal, in ImportInput, previewID string, confirmRetro bool) (*ImportResult, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermOrgManage); err != nil {
		return nil, err
	}
	tok, err := s.verifyImport(previewID, p.UserID)
	if err != nil {
		return nil, err
	}
	plan, err := s.BuildImportPlan(ctx, in, tok.Base)
	if err != nil {
		return nil, err
	}
	if len(plan.FileErrors) > 0 || len(plan.Errors) > 0 {
		if plan.RiskyRoleRows > 0 { // 위험 권한 역할 지정 시도는 거부 감사로 남긴다
			s.recordDenied(ctx, p, "org.import_apply", audit.TargetImport, "risky_role", "위험 권한이 든 역할 지정 시도")
		}
		return nil, verr("미리보기에 오류가 있어 반영할 수 없습니다. 다시 미리보기 하세요.")
	}
	for _, f := range plan.Files {
		if tok.Files[f.Kind] != f.SHA256 {
			return nil, &ImportConflict{"미리보기 이후 파일이 바뀌었습니다. 다시 미리보기 하세요."}
		}
	}
	if len(plan.Files) != len(tok.Files) {
		return nil, &ImportConflict{"미리보기 이후 파일이 바뀌었습니다. 다시 미리보기 하세요."}
	}
	if len(plan.Items) == 0 {
		return nil, verr("반영할 변경이 없습니다. 이미 모두 반영된 파일입니다.")
	}
	if len(plan.RetroRows) > 0 && !confirmRetro {
		return nil, &ImportNeedConfirm{"효력일이 30일을 넘게 지난 행이 있습니다(" + plan.RetroRows[0] + "). 소급 반영은 이미 끝난 결재선과 열람 범위에 영향을 줄 수 있습니다. 확인 체크가 필요합니다."}
	}
	var result *ImportResult
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		fp, err := s.orgFingerprint(ctx, tx)
		if err != nil {
			return err
		}
		if fp != tok.FP {
			return &ImportConflict{"미리보기 뒤에 조직이 바뀌었습니다. 다시 미리보기 하세요."}
		}
		batch, err := s.nextImportBatch(ctx, tx)
		if err != nil {
			return err
		}
		cmds := planCommands(plan.Items, true, batch, true)
		outs, _, err := s.applyCommands(ctx, tx, p, cmds, false, nil, nil)
		if err != nil {
			return err
		}
		var first, last int64
		counts := map[string]int{}
		n := 0
		for i, o := range outs {
			counts[plan.Items[i].Card]++
			for _, id := range o.EventIDs {
				n++
				if first == 0 || id < first {
					first = id
				}
				if id > last {
					last = id
				}
			}
		}
		if n != len(plan.Items) {
			return &ImportConflict{"미리보기 뒤에 조직이 바뀌었습니다. 다시 미리보기 하세요."}
		}
		if err := s.recordImport(ctx, tx, p, plan, outs, batch, first, last, counts); err != nil {
			return err
		}
		result = &ImportResult{Batch: batch, Events: n, Counts: counts}
		return nil
	})
	s.invalidate()
	if err != nil {
		return nil, err
	}
	s.importMu.Lock()
	s.importUsed[previewID] = true
	s.importMu.Unlock()
	return result, nil
}

func (s *Service) nextImportBatch(ctx context.Context, tx contract.Tx) (int64, error) {
	var cur sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, settingImportSeq).Scan(&cur)
	var n int64
	if err == nil && cur.Valid {
		fmt.Sscanf(cur.String, "%d", &n)
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	n++
	if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, settingImportSeq, fmt.Sprint(n), s.now().UTC().Format(time.RFC3339)); err != nil {
		return 0, err
	}
	return n, nil
}

const settingImportSeq = "org_import_seq"

func (s *Service) recordImport(ctx context.Context, tx contract.Tx, p contract.Principal, plan *ImportPlan, outs []Outcome, batch, first, last int64, counts map[string]int) error {
	var names, sums []string
	rows := 0
	for _, f := range plan.Files {
		names = append(names, f.Name)
		sums = append(sums, f.KindName+":"+f.SHA256)
		rows += f.Rows
	}
	added := counts[CardUnitCreate] + counts[CardHire]
	changed := counts[CardUnitChange] + counts[CardTransfer] + counts[CardRank] + counts[CardConcurrent] + counts[CardActing]
	diff := []audit.DiffItem{
		{Key: "file_name", New: strings.Join(names, ", ")},
		{Key: "file_sha256", New: strings.Join(sums, ", ")},
		{Key: "rows", New: fmt.Sprint(rows)},
		{Key: "added", New: fmt.Sprint(added)},
		{Key: "changed", New: fmt.Sprint(changed)},
		{Key: "left", New: fmt.Sprint(counts[CardLeave])},
		{Key: "event_range", New: fmt.Sprintf("%d~%d", first, last)},
	}
	for i, it := range plan.Items {
		if i >= importAuditMax {
			diff = append(diff, audit.DiffItem{Key: "event", New: fmt.Sprintf("외 %d건", len(plan.Items)-importAuditMax)})
			break
		}
		ids := ""
		if i < len(outs) && len(outs[i].EventIDs) > 0 {
			ids = fmt.Sprintf(" #%d", outs[i].EventIDs[0])
		}
		diff = append(diff, audit.DiffItem{Key: "event", New: fmt.Sprintf("%s%s %s %s (%s)", it.Label, ids, it.Target, it.Content, it.Eff)})
	}
	return s.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "org.import_apply", TargetType: audit.TargetImport, TargetID: fmt.Sprint(batch),
		Result: audit.ResultSuccess, Diff: diff})
}
