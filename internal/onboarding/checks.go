package onboarding

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// 항목 판정 질의(명세 38 5절). 항목마다 질의 함수 하나이고 DB 를 읽어 계산한다. 비밀값을 읽지 않는다: 키·경로는 Env 의 있음/없음만 쓴다.
// 개별 항목 질의가 실패하면 그 항목만 "확인하지 못했습니다"로 표시한다(나머지는 보인다).

// State 는 항목의 상태다.
type State string

const (
	StateDone       State = "done"           // 완료
	StateInProgress State = "in_progress"    // 진행 중
	StateTodo       State = "todo"           // 미착수
	StateNA         State = "not_applicable" // 해당 없음(사람이 사유와 함께 표시)
	StateUnknown    State = "unknown"        // 확인하지 못했습니다(질의 실패)
)

// Env 는 서버 환경에서 오는 사실이다. 값이 아니라 설정 여부(있음/없음)만 담는다.
type Env struct {
	HTTPS            bool // PL_BASE_URL 이 https
	CookieSecure     bool // 세션 쿠키 Secure
	AnchorConfigured bool // 감사 외부 앵커 키·경로 설정됨
	AccountKey       bool // PL_ACCOUNT_SECRET_KEY 설정됨
	AuthKey          bool // PL_AUTH_SECRET_KEY 설정됨
	MailConfigured   bool // SMTP 설정됨
	DevIDP           bool // PL_DEV_IDP 켜짐(개발 모드). 꺼져 있으면 운영이라 모의 대행사가 켜진 것을 미완료로 본다(B43)
	BaseURLSet       bool // PL_BASE_URL 설정됨(변경 웹훅 본문의 api_url 을 만드는 데 필요)
}

type query interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

type result struct {
	state    State
	evidence string
}

type check struct {
	id        string
	title     string
	link      string
	skippable bool
	fn        func(ctx context.Context, q query, now time.Time, env Env) (result, error)
}

func count(ctx context.Context, q query, s string, args ...any) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, s, args...).Scan(&n)
	return n, err
}

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// grade 는 충족한 조건 수로 상태를 정한다: 모두 → 완료, 일부 → 진행 중, 없음 → 미착수.
func grade(met, total int) State {
	switch {
	case met >= total:
		return StateDone
	case met > 0:
		return StateInProgress
	}
	return StateTodo
}

// requiredAccounts 는 전표 자동 생성에 필요한 시스템 계정 코드다(0037: 보통예금·상품·부가세대급금·외상매입금·미지급금).
var requiredAccounts = []string{"1020", "1210", "1310", "2110", "2120"}

// importTargets 는 B35 가져오기 대상 7종이다(import_batches.target).
var importTargets = []string{"account", "warehouse", "vendor", "item", "budget", "opening_stock", "opening_payable"}

var checks = []check{
	{id: "company", title: "회사 정보", link: "/admin/company", fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		filled, err := count(ctx, q, `SELECT CASE WHEN name <> '' THEN 1 ELSE 0 END + CASE WHEN business_no <> '' THEN 1 ELSE 0 END + CASE WHEN representative <> '' THEN 1 ELSE 0 END + CASE WHEN address <> '' THEN 1 ELSE 0 END FROM company_settings WHERE id = 1`)
		if errors.Is(err, sql.ErrNoRows) {
			return result{StateTodo, "회사 정보가 아직 저장되지 않았습니다"}, nil
		}
		if err != nil {
			return result{}, err
		}
		assets, err := count(ctx, q, `SELECT COUNT(*) FROM company_assets WHERE current = 1`)
		if err != nil {
			return result{}, err
		}
		ev := fmt.Sprintf("회사명·사업자번호·대표자·주소 중 %d개 입력, 로고·직인 %d개", filled, assets)
		switch {
		case filled >= 3 && assets >= 1:
			return result{StateDone, ev}, nil
		case filled >= 1:
			return result{StateInProgress, ev + " (필수 3개 이상과 로고 또는 직인 1개가 필요합니다)"}, nil
		}
		return result{StateTodo, ev}, nil
	}},
	{id: "org", title: "조직도", link: "/org/manage", fn: func(ctx context.Context, q query, now time.Time, _ Env) (result, error) {
		day := string(contract.DayOf(now))
		cur := `valid_from <= ?1 AND (valid_to IS NULL OR ?1 < valid_to)`
		units, err := count(ctx, q, `SELECT COUNT(*) FROM org_unit_spans WHERE closed = 0 AND `+cur, day)
		if err != nil {
			return result{}, err
		}
		outside, err := count(ctx, q, `SELECT COUNT(*) FROM users u WHERE u.active = 1 AND NOT EXISTS (SELECT 1 FROM org_person_spans p WHERE p.user_id = u.id AND p.left_flag = 0 AND p.valid_from <= ?1 AND (p.valid_to IS NULL OR ?1 < p.valid_to))`, day)
		if err != nil {
			return result{}, err
		}
		noHead, err := count(ctx, q, `SELECT COUNT(*) FROM org_unit_spans WHERE closed = 0 AND head_user_id = 0 AND `+cur, day)
		if err != nil {
			return result{}, err
		}
		ev := fmt.Sprintf("조직 단위 %d개, 조직에 속하지 않은 활성 사용자 %d명, 조직장이 없는 단위 %d개", units, outside, noHead)
		met := 0
		if units >= 2 {
			met++
		}
		if outside == 0 && units > 0 {
			met++
		}
		if noHead == 0 && units > 0 {
			met++
		}
		return result{grade(met, 3), ev}, nil
	}},
	{id: "users", title: "사용자·역할", link: "/admin/roles", fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		active, err := count(ctx, q, `SELECT COUNT(*) FROM users WHERE active = 1`)
		if err != nil {
			return result{}, err
		}
		// 역할 이름이 아니라 실효 권한으로 센다(사용자 정의 역할도 맞게 보이도록): 관리자 = 역할 관리 권한, 재무 = 재무 결재 단계 권한.
		withPerm := func(perm string) (int, error) {
			return count(ctx, q, `SELECT COUNT(DISTINCT u.id) FROM users u JOIN user_effective_permissions e ON e.user_id = u.id WHERE u.active = 1 AND e.permission = ?`, perm)
		}
		admins, err := withPerm(string(contract.PermRoleManage))
		if err != nil {
			return result{}, err
		}
		fin, err := withPerm(string(contract.PermFinanceStage))
		if err != nil {
			return result{}, err
		}
		emergency := 0
		var raw string
		if err := q.QueryRowContext(ctx, `SELECT config_json FROM auth_policy WHERE id = 1`).Scan(&raw); err == nil {
			var p struct {
				Emergency []int64 `json:"emergencyAdmins"`
			}
			if json.Unmarshal([]byte(raw), &p) == nil {
				emergency = len(p.Emergency)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return result{}, err
		}
		ev := fmt.Sprintf("활성 사용자 %d명, 역할 관리 권한자 %d명, 재무 결재 권한자 %d명, 비상 관리자 %d명", active, admins, fin, emergency)
		met := 0
		for _, ok := range []bool{active >= 2, admins >= 1, fin >= 1, emergency >= 1} {
			if ok {
				met++
			}
		}
		return result{grade(met, 4), ev}, nil
	}},
	{id: "approval_rules", title: "결재 규칙", link: "/authority", fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		authority, err := count(ctx, q, `SELECT COUNT(*) FROM authority_rules WHERE active = 1`)
		if err != nil {
			return result{}, err
		}
		lines, err := count(ctx, q, `SELECT COUNT(*) FROM approval_line_rules`)
		if err != nil {
			return result{}, err
		}
		met := 0
		if authority >= 1 {
			met++
		}
		if lines >= 1 {
			met++
		}
		return result{grade(met, 2), fmt.Sprintf("전결 규정 %d건, 결재선 규칙 %d건", authority, lines)}, nil
	}},
	{id: "forms", title: "결재 양식", link: "/forms", fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		active, err := count(ctx, q, `SELECT COUNT(*) FROM approval_forms WHERE active = 1`)
		if err != nil {
			return result{}, err
		}
		unpublished, err := count(ctx, q, `SELECT COUNT(*) FROM approval_forms f WHERE f.active = 1 AND NOT EXISTS (SELECT 1 FROM approval_form_versions v WHERE v.form_id = f.id AND v.status = 'published')`)
		if err != nil {
			return result{}, err
		}
		ev := fmt.Sprintf("사용 중인 양식 %d개, 게시본이 없는 양식 %d개", active, unpublished)
		switch {
		case active > 0 && unpublished == 0:
			return result{StateDone, ev}, nil
		case active > unpublished:
			return result{StateInProgress, ev}, nil
		}
		return result{StateTodo, ev}, nil
	}},
	{id: "accounts", title: "계정과목", link: "/finance/accounts", fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		active, err := count(ctx, q, `SELECT COUNT(*) FROM accounts WHERE active = 1`)
		if err != nil {
			return result{}, err
		}
		have := 0
		for _, code := range requiredAccounts {
			n, err := count(ctx, q, `SELECT COUNT(*) FROM accounts WHERE code = ? AND active = 1`, code)
			if err != nil {
				return result{}, err
			}
			have += n
		}
		ev := fmt.Sprintf("활성 계정과목 %d개, 전표 자동 생성에 필요한 시스템 계정 %d/%d개", active, have, len(requiredAccounts))
		if active >= 1 && have == len(requiredAccounts) {
			return result{StateDone, ev}, nil
		}
		if active >= 1 {
			return result{StateInProgress, ev}, nil
		}
		return result{StateTodo, ev}, nil
	}},
	{id: "opening_data", title: "기초 데이터", link: "/org/import", skippable: true, fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		applied := 0
		for _, t := range importTargets {
			n, err := count(ctx, q, `SELECT COUNT(*) FROM import_batches WHERE target = ? AND status = 'applied'`, t)
			if err != nil {
				return result{}, err
			}
			if n > 0 {
				applied++
			}
		}
		return result{grade(applied, len(importTargets)), fmt.Sprintf("가져오기 대상 %d종 중 %d종을 반영했습니다", len(importTargets), applied)}, nil
	}},
	{id: "notifications", title: "알림 설정", link: "/admin/notifications", skippable: true, fn: func(ctx context.Context, q query, _ time.Time, env Env) (result, error) {
		tested, err := count(ctx, q, `SELECT COUNT(*) FROM audit_logs WHERE action = 'notification.mail_test' AND result = 'success'`)
		if err != nil {
			return result{}, err
		}
		ev := yesNo(env.MailConfigured, "메일 발송이 설정됨", "메일 발송이 설정되지 않음") + ", " + yesNo(tested > 0, "시험 메일 성공 이력 있음", "시험 메일 성공 이력 없음")
		switch {
		case env.MailConfigured && tested > 0:
			return result{StateDone, ev}, nil
		case env.MailConfigured:
			return result{StateInProgress, ev}, nil
		}
		return result{StateTodo, ev}, nil
	}},
	{id: "scim", title: "계정 동기화", link: "/admin/scim", skippable: true, fn: func(ctx context.Context, q query, now time.Time, _ Env) (result, error) {
		var enabled string
		if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'scim.enabled'`).Scan(&enabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result{}, err
		}
		// 만료 시각은 UTC RFC3339 문자열이라 날 단위 비교는 문자열 비교로 충분하다.
		nowS, soonS := now.UTC().Format(time.RFC3339), now.UTC().Add(14*24*time.Hour).Format(time.RFC3339)
		valid, err := count(ctx, q, `SELECT COUNT(*) FROM scim_tokens WHERE revoked_at IS NULL AND expires_at > ?`, nowS)
		if err != nil {
			return result{}, err
		}
		soon, err := count(ctx, q, `SELECT COUNT(*) FROM scim_tokens WHERE revoked_at IS NULL AND expires_at > ? AND expires_at < ?`, nowS, soonS)
		if err != nil {
			return result{}, err
		}
		synced, err := count(ctx, q, `SELECT COUNT(*) FROM scim_sync_log WHERE result = 'success'`)
		if err != nil {
			return result{}, err
		}
		on := enabled == "true"
		ev := yesNo(on, "계정 동기화 사용 중", "계정 동기화 꺼짐") + fmt.Sprintf(", 유효한 연동 토큰 %d개, 성공한 동기화 %d건", valid, synced)
		if soon > 0 {
			ev += fmt.Sprintf(" (14일 안에 만료되는 토큰 %d개)", soon)
		}
		switch {
		case on && valid > 0 && synced > 0 && soon == 0:
			return result{StateDone, ev}, nil
		case on || valid > 0:
			return result{StateInProgress, ev}, nil
		}
		return result{StateTodo, ev}, nil
	}},
	{id: "api_integration", title: "연동 API", link: "/admin/integrations", skippable: true, fn: func(ctx context.Context, q query, now time.Time, env Env) (result, error) {
		// 명세 42 13.3: 사용 중인 토큰이 만료 14일 안이면 미완료(연동이 곧 멈춘다). 켜져 있고 유효한 토큰이 있으며 임박한 토큰이 없으면 완료.
		var enabled string
		if err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'api.enabled'`).Scan(&enabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result{}, err
		}
		nowS, soonS := now.UTC().Format(time.RFC3339), now.UTC().Add(14*24*time.Hour).Format(time.RFC3339)
		valid, err := count(ctx, q, `SELECT COUNT(*) FROM api_tokens WHERE revoked_at IS NULL AND expires_at > ?`, nowS)
		if err != nil {
			return result{}, err
		}
		soon, err := count(ctx, q, `SELECT COUNT(*) FROM api_tokens WHERE revoked_at IS NULL AND expires_at > ? AND expires_at < ?`, nowS, soonS)
		if err != nil {
			return result{}, err
		}
		// 변경 웹훅(B42 2단계): 구독이 있는데 접속 주소(PL_BASE_URL)가 없으면 모든 발송 건이 실패하고, 자동 중지된 구독이 있으면 연동이 멈춰 있다.
		subs, err := count(ctx, q, `SELECT COUNT(*) FROM webhook_subscriptions WHERE active = 1 AND paused_reason = ''`)
		if err != nil {
			return result{}, err
		}
		paused, err := count(ctx, q, `SELECT COUNT(*) FROM webhook_subscriptions WHERE paused_reason <> ''`)
		if err != nil {
			return result{}, err
		}
		// 쓰기 범위(가져오기·전표 초안) 토큰이 있으면 안내한다: 데이터를 바꿀 수 있는 토큰이라 허용 출발지·만료를 점검하라는 뜻이다.
		writers, err := count(ctx, q, `SELECT COUNT(*) FROM api_tokens WHERE revoked_at IS NULL AND expires_at > ? AND (scopes LIKE '%:write"%' OR scopes LIKE '%:draft"%')`, nowS)
		if err != nil {
			return result{}, err
		}
		on := enabled == "1"
		ev := yesNo(on, "연동 API 켜짐", "연동 API 꺼짐") + fmt.Sprintf(", 유효한 API 토큰 %d개", valid)
		if writers > 0 {
			ev += fmt.Sprintf(" (데이터를 바꿀 수 있는 쓰기 범위 토큰 %d개: 허용 출발지와 만료를 점검하세요)", writers)
		}
		if soon > 0 {
			ev += fmt.Sprintf(" (14일 안에 만료되는 토큰 %d개)", soon)
		}
		webhookOK := true
		if subs+paused > 0 {
			ev += fmt.Sprintf(", 변경 웹훅 구독 %d개", subs)
			if paused > 0 {
				ev += fmt.Sprintf(" (중지된 구독 %d개)", paused)
				webhookOK = false
			}
			if !env.BaseURLSet {
				ev += ", 접속 주소(PL_BASE_URL)가 없어 웹훅 발송이 모두 실패합니다"
				webhookOK = false
			}
		}
		switch {
		case !webhookOK && (on || valid > 0 || subs > 0):
			return result{StateInProgress, ev}, nil
		case on && valid > 0 && soon == 0:
			return result{StateDone, ev}, nil
		case on || valid > 0:
			return result{StateInProgress, ev}, nil
		}
		return result{StateTodo, ev}, nil
	}},
	{id: "tax_asp", title: "전자세금계산서 연동", link: "/admin/tax-asp", skippable: true, fn: func(ctx context.Context, q query, now time.Time, env Env) (result, error) {
		// 명세 43 7·9절: 대행사 설정이 있고 켜져 있으며 인증서가 만료 30일 밖이고 운영에서 모의 대행사가 아니어야 완료. 만료일은 평문 칸이라 풀지 않고 읽는다.
		var provider, notAfter string
		var active int
		err := q.QueryRowContext(ctx, `SELECT provider, cert_not_after, active FROM tax_asp_settings WHERE entity_id = 1`).Scan(&provider, &notAfter, &active)
		if errors.Is(err, sql.ErrNoRows) {
			return result{StateTodo, "대행사 연결이 설정되지 않았습니다(세금계산서를 발행하거나 수신하지 않으면 건너뛸 수 있습니다)"}, nil
		}
		if err != nil {
			return result{}, err
		}
		kst := time.FixedZone("KST", 9*3600)
		ev := yesNo(active == 1, "대행사 연결 켜짐", "대행사 연결 꺼짐")
		ok := active == 1
		if notAfter != "" {
			exp, perr := time.ParseInLocation("2006-01-02", notAfter, kst)
			if perr == nil {
				today, _ := time.ParseInLocation("2006-01-02", now.In(kst).Format("2006-01-02"), kst)
				days := int(exp.Sub(today).Hours() / 24)
				switch {
				case days < 0:
					ev += ", 인증서가 만료되어 발행할 수 없습니다"
					ok = false
				case days <= 30:
					ev += fmt.Sprintf(", 인증서 만료 %d일 전", days)
					ok = false
				default:
					ev += ", 인증서 유효"
				}
			}
		}
		if provider == "mock" && !env.DevIDP {
			ev += ", 운영에서 모의 대행사가 선택되어 있습니다(실제 국세청에 접수되지 않습니다)"
			ok = false
		}
		var syncResult string
		_ = q.QueryRowContext(ctx, `SELECT last_result FROM tax_sync_state WHERE entity_id = 1`).Scan(&syncResult)
		if syncResult == "owner_inactive" {
			ev += ", 수신 동기화가 멈췄습니다(대행사 연동을 켠 담당자가 비활성이거나 없습니다. 담당자가 다시 켜세요)"
			ok = false
		}
		if ok {
			return result{StateDone, ev}, nil
		}
		return result{StateInProgress, ev}, nil
	}},
	{id: "privacy_sod", title: "개인정보 기록·권한 분리", link: "/admin/sod/rules", fn: func(ctx context.Context, q query, now time.Time, _ Env) (result, error) {
		// 명세 41 13절: 보관 기간이 정해졌고, 조회 기록이 있으면 최근 하루 요약이 있으며, 차단 규칙이 하나 이상 있고, 조회 기록을 볼 감사 담당이 있어야 완료.
		retention, err := count(ctx, q, `SELECT COUNT(*) FROM app_settings WHERE key = 'privacy.retention_days'`)
		if err != nil {
			return result{}, err
		}
		logged, err := count(ctx, q, `SELECT COUNT(*) FROM privacy_access_log`)
		if err != nil {
			return result{}, err
		}
		digest := 0
		if logged > 0 {
			if digest, err = count(ctx, q, `SELECT COUNT(*) FROM audit_logs WHERE action = 'privacy.log_digest' AND at >= ?`, now.UTC().Add(-25*time.Hour).Format(time.RFC3339)); err != nil {
				return result{}, err
			}
		}
		blocks := 0
		for _, r := range sod.Rules {
			mode := r.Default
			if !r.Locked {
				var m string
				switch err := q.QueryRowContext(ctx, `SELECT mode FROM sod_rule_settings WHERE rule_code = ?`, r.Code).Scan(&m); {
				case err == nil:
					mode = m
				case !errors.Is(err, sql.ErrNoRows):
					return result{}, err
				}
			}
			if mode == sod.ModeBlock {
				blocks++
			}
		}
		auditors, err := count(ctx, q, `SELECT COUNT(DISTINCT e.user_id) FROM user_effective_permissions e JOIN users u ON u.id = e.user_id WHERE e.permission = ? AND u.active = 1`, string(contract.PermPrivacyLogRead))
		if err != nil {
			return result{}, err
		}
		conds := []bool{retention > 0, logged == 0 || digest > 0, blocks > 0, auditors > 0}
		met := 0
		for _, ok := range conds {
			if ok {
				met++
			}
		}
		ev := yesNo(retention > 0, "조회 기록 보관 기간 설정함", "조회 기록 보관 기간 미설정(기본 730일)") +
			yesNo(logged == 0 || digest > 0, ", 하루 요약 확인됨", ", 최근 하루 요약 없음") +
			fmt.Sprintf(", 차단 규칙 %d개, 조회 기록 열람 담당 %d명", blocks, auditors)
		return result{grade(met, len(conds)), ev}, nil
	}},
	{id: "entities", title: "법인·사업장", link: "/admin/entities", skippable: true, fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		// 명세 44 15.1: 기본 법인에 사업자번호가 있고, 활성 법인마다 활성 주사업장이 있으며, 기본 법인이 아닌 법인은 루트 조직이 연결돼 있으면 완료.
		// 법인이 기본 법인 하나뿐이고 사업자번호가 비어 있으면 미착수(회사 정보 항목이 같은 일을 안내한다).
		total, err := count(ctx, q, `SELECT COUNT(*) FROM entities WHERE active = 1`)
		if err != nil {
			return result{}, err
		}
		noBiz, err := count(ctx, q, `SELECT COUNT(*) FROM entities WHERE active = 1 AND business_no = ''`)
		if err != nil {
			return result{}, err
		}
		noHead, err := count(ctx, q, `SELECT COUNT(*) FROM entities e WHERE e.active = 1 AND NOT EXISTS (SELECT 1 FROM entity_sites s WHERE s.entity_id = e.id AND s.kind = 'head' AND s.active = 1)`)
		if err != nil {
			return result{}, err
		}
		noRoot, err := count(ctx, q, `SELECT COUNT(*) FROM entities WHERE active = 1 AND is_default = 0 AND root_unit_id IS NULL`)
		if err != nil {
			return result{}, err
		}
		ev := fmt.Sprintf("활성 법인 %d개", total)
		if total <= 1 && noBiz > 0 {
			return result{StateTodo, ev + ", 기본 법인에 사업자번호가 없습니다(관리 > 회사 설정에서 입력)"}, nil
		}
		ok := true
		if noBiz > 0 {
			ev += fmt.Sprintf(", 사업자번호가 없는 법인 %d개", noBiz)
			ok = false
		}
		if noHead > 0 {
			ev += fmt.Sprintf(", 주사업장이 없는 법인 %d개", noHead)
			ok = false
		}
		if noRoot > 0 {
			ev += fmt.Sprintf(", 루트 조직이 연결되지 않은 법인 %d개", noRoot)
			ok = false
		}
		if ok {
			return result{StateDone, ev}, nil
		}
		return result{StateInProgress, ev}, nil
	}},
	{id: "llm_providers", title: "LLM 공급자", link: "/admin/llm/providers", skippable: true, fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		// 명세 45 14절: 서버 설정의 기본 공급자만 쓰면 미착수(해당 없음으로 넘길 수 있다). 켜진 공급자가 있고, 켜진 규칙의 주 공급자가 모두 켜져 있으며,
		// 외부 공급자를 주로 쓰는 규칙에는 대체 공급자가 있으면 완료.
		total, err := count(ctx, q, `SELECT COUNT(*) FROM llm_providers`)
		if err != nil {
			return result{}, err
		}
		active, err := count(ctx, q, `SELECT COUNT(*) FROM llm_providers WHERE active = 1`)
		if err != nil {
			return result{}, err
		}
		if total == 0 {
			return result{StateTodo, "등록된 공급자가 없습니다(지금은 서버 설정의 기본 공급자만 사용)"}, nil
		}
		routes, err := count(ctx, q, `SELECT COUNT(*) FROM llm_routes WHERE active = 1`)
		if err != nil {
			return result{}, err
		}
		offPrimary, err := count(ctx, q, `SELECT COUNT(*) FROM llm_routes r JOIN llm_providers p ON p.id = r.primary_provider_id WHERE r.active = 1 AND p.active = 0`)
		if err != nil {
			return result{}, err
		}
		noFallback, err := count(ctx, q, `SELECT COUNT(*) FROM llm_routes r JOIN llm_providers p ON p.id = r.primary_provider_id WHERE r.active = 1 AND p.active = 1 AND p.external = 1 AND r.fallback_provider_id IS NULL`)
		if err != nil {
			return result{}, err
		}
		ev := fmt.Sprintf("공급자 %d개(켜짐 %d개), 라우팅 규칙 %d개", total, active, routes)
		ok := active > 0
		if active == 0 {
			ev += ", 켜진 공급자가 없습니다"
		}
		if offPrimary > 0 {
			ev += fmt.Sprintf(", 주 공급자가 꺼진 규칙 %d개", offPrimary)
			ok = false
		}
		if noFallback > 0 {
			ev += fmt.Sprintf(", 대체 공급자가 없는 외부 공급자 규칙 %d개", noFallback)
			ok = false
		}
		if ok {
			return result{StateDone, ev}, nil
		}
		return result{StateInProgress, ev}, nil
	}},
	{id: "eval_sets", title: "평가 세트", link: "/admin/eval/sets", skippable: true, fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		// 명세 46: 회사 업무 문항으로 만든 평가 세트가 하나라도 게시돼 있으면 완료(모델·라우팅을 바꿀 때 품질을 확인할 수단이 있다). 만들었지만 게시 전이면 진행 중, 없으면 미착수.
		total, err := count(ctx, q, `SELECT COUNT(*) FROM eval_sets WHERE active = 1`)
		if err != nil {
			return result{}, err
		}
		if total == 0 {
			return result{StateTodo, "만든 평가 세트가 없습니다(모델·라우팅을 바꿀 때 품질을 확인할 문항을 만들어 두세요)"}, nil
		}
		published, err := count(ctx, q, `SELECT COUNT(*) FROM eval_sets WHERE active = 1 AND current_version > 0`)
		if err != nil {
			return result{}, err
		}
		ev := fmt.Sprintf("평가 세트 %d개, 게시된 세트 %d개", total, published)
		if published > 0 {
			return result{StateDone, ev}, nil
		}
		return result{StateInProgress, ev + "(문항을 만들고 게시하세요)"}, nil
	}},
	{id: "mcp_tools", title: "AI 도구 만들기", link: "/admin/mcp-tools", skippable: true, fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		// 명세 48: 켜진 사용자 정의 도구가 하나라도 있으면 완료. 정의만 만들었으면 진행 중, 없으면 미착수(내장 도구만 써도 된다).
		total, err := count(ctx, q, `SELECT COUNT(*) FROM mcp_tools`)
		if err != nil {
			return result{}, err
		}
		if total == 0 {
			return result{StateTodo, "만든 AI 도구가 없습니다(자주 묻는 조회를 도구로 만들어 두면 AI 가 배포 없이 씁니다)"}, nil
		}
		enabled, err := count(ctx, q, `SELECT COUNT(*) FROM mcp_tools WHERE enabled = 1`)
		if err != nil {
			return result{}, err
		}
		ev := fmt.Sprintf("AI 도구 %d개, 켜진 도구 %d개", total, enabled)
		if enabled > 0 {
			return result{StateDone, ev}, nil
		}
		return result{StateInProgress, ev + "(시험 실행 뒤 켜세요)"}, nil
	}},
	{id: "ai_cost", title: "AI 비용 배부", link: "/admin/ai-cost", skippable: true, fn: func(ctx context.Context, q query, _ time.Time, _ Env) (result, error) {
		// 명세 49: 배부 규칙·월 환율이 있고 확정한 달이 하나라도 있으면 완료. 규칙이나 환율만 있으면 진행 중, 없으면 미착수(AI 비용을 부서에 나누지 않아도 된다).
		rules, err := count(ctx, q, `SELECT COUNT(*) FROM ai_cost_rule_sets`)
		if err != nil {
			return result{}, err
		}
		fx, err := count(ctx, q, `SELECT COUNT(*) FROM ai_cost_fx`)
		if err != nil {
			return result{}, err
		}
		runs, err := count(ctx, q, `SELECT COUNT(*) FROM ai_cost_runs WHERE status = 'confirmed'`)
		if err != nil {
			return result{}, err
		}
		budgets, err := count(ctx, q, `SELECT COUNT(*) FROM ai_cost_budgets WHERE amount_krw > 0`)
		if err != nil {
			return result{}, err
		}
		if rules == 0 && fx == 0 && runs == 0 {
			return result{StateTodo, "AI 비용 배부 규칙·월 환율이 없습니다(AI 비용을 부서별로 나눌 때 규칙과 그 달 환율, 공급자 청구액을 정해 두세요)"}, nil
		}
		ev := fmt.Sprintf("배부 규칙 %d개 버전, 환율을 정한 달 %d개, 확정한 달 %d개, 예산이 있는 부서·월 %d건", rules, fx, runs, budgets)
		if rules > 0 && fx > 0 && runs > 0 {
			return result{StateDone, ev}, nil
		}
		return result{StateInProgress, ev + "(규칙·환율을 정하고 지난 달 배부를 확정하세요)"}, nil
	}},
	{id: "login_security", title: "로그인 보안", link: "/admin/auth", fn: func(ctx context.Context, q query, _ time.Time, env Env) (result, error) {
		totp, err := count(ctx, q, `SELECT COUNT(DISTINCT t.user_id) FROM user_totp t JOIN users u ON u.id = t.user_id JOIN user_effective_permissions e ON e.user_id = u.id WHERE t.confirmed_at IS NOT NULL AND u.active = 1 AND e.permission = ?`, string(contract.PermAuthManage))
		if err != nil {
			return result{}, err
		}
		met := 0
		for _, ok := range []bool{totp >= 1, env.HTTPS, env.CookieSecure} {
			if ok {
				met++
			}
		}
		ev := fmt.Sprintf("2단계 인증을 등록한 관리자 %d명, 접속 주소 %s, 세션 쿠키 %s", totp, yesNo(env.HTTPS, "https", "http"), yesNo(env.CookieSecure, "Secure", "Secure 아님"))
		return result{grade(met, 3), ev}, nil
	}},
	{id: "audit_anchor", title: "감사 앵커", link: "/admin/audit", fn: func(ctx context.Context, q query, _ time.Time, env Env) (result, error) {
		stamps, err := count(ctx, q, `SELECT COUNT(*) FROM audit_anchor_stamps`)
		if err != nil {
			return result{}, err
		}
		ev := yesNo(env.AnchorConfigured, "외부 앵커가 설정됨", "외부 앵커가 설정되지 않음") + fmt.Sprintf(", 기록 %d건", stamps)
		switch {
		case env.AnchorConfigured && stamps > 0:
			return result{StateDone, ev}, nil
		case env.AnchorConfigured:
			return result{StateInProgress, ev}, nil
		}
		return result{StateTodo, ev}, nil
	}},
	{id: "keys", title: "키 설정", link: "", fn: func(_ context.Context, _ query, _ time.Time, env Env) (result, error) {
		met := 0
		if env.AccountKey {
			met++
		}
		if env.AuthKey {
			met++
		}
		ev := "계좌번호 암호화 키 " + yesNo(env.AccountKey, "있음", "없음") + ", 2단계 인증 키 " + yesNo(env.AuthKey, "있음", "없음")
		return result{grade(met, 2), ev}, nil
	}},
	{id: "backup", title: "백업", link: "/admin/backup", fn: func(ctx context.Context, q query, now time.Time, _ Env) (result, error) {
		last := func(kind string) (time.Time, bool, error) {
			var s string
			err := q.QueryRowContext(ctx, `SELECT finished_at FROM backup_runs WHERE kind = ? AND result = 'success' ORDER BY id DESC LIMIT 1`, kind).Scan(&s)
			if errors.Is(err, sql.ErrNoRows) {
				return time.Time{}, false, nil
			}
			if err != nil {
				return time.Time{}, false, err
			}
			t, perr := parseTime(s)
			return t, perr == nil, nil
		}
		b, hasB, err := last("backup")
		if err != nil {
			return result{}, err
		}
		r, hasR, err := last("rehearsal")
		if err != nil {
			return result{}, err
		}
		freshB := hasB && now.Sub(b) <= 7*24*time.Hour
		freshR := hasR && now.Sub(r) <= 30*24*time.Hour
		ev := "지난 7일 안의 성공한 백업 " + yesNo(freshB, "있음", "없음") + ", 30일 안의 복구 리허설 " + yesNo(freshR, "있음", "없음")
		met := 0
		if freshB {
			met++
		}
		if freshR {
			met++
		}
		return result{grade(met, 2), ev}, nil
	}},
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("시각 형식")
}
