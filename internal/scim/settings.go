package scim

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// Settings 는 SCIM 동작 설정이다(app_settings 의 scim.* 키, 명세 4절). 행이 없으면 기본값이다.
type Settings struct {
	Enabled       bool   `json:"enabled"`
	GroupMode     string `json:"groupMode"`     // off | unit | role (그룹 연동은 3단계, 지금은 off 만)
	DefaultUnit   string `json:"defaultUnit"`   // 조직 코드
	DefaultRank   string `json:"defaultRank"`   // 직위 이름
	DefaultRole   string `json:"defaultRole"`   // 역할 키
	AdoptExisting bool   `json:"adoptExisting"` // 같은 이메일의 미연결 기존 사용자를 연결할지
}

func defaultSettings() Settings {
	return Settings{GroupMode: "off", DefaultRole: "staff", AdoptExisting: true}
}

func (s *Service) loadSettings(ctx context.Context, q querier) (Settings, error) {
	st := defaultSettings()
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM app_settings WHERE key LIKE 'scim.%'`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return st, err
		}
		switch k {
		case "scim.enabled":
			st.Enabled = v == "true"
		case "scim.group_mode":
			st.GroupMode = v
		case "scim.default_unit":
			st.DefaultUnit = v
		case "scim.default_rank":
			st.DefaultRank = v
		case "scim.default_role":
			st.DefaultRole = v
		case "scim.adopt_existing":
			st.AdoptExisting = v != "false"
		}
	}
	return st, rows.Err()
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// GetSettings 는 설정을 읽는다(scim:read).
func (s *Service) GetSettings(ctx context.Context, p contract.Principal) (Settings, error) {
	if _, err := contract.Authorize(contract.WithPrincipal(ctx, p), contract.PermSCIMRead); err != nil {
		return Settings{}, err
	}
	return s.loadSettings(ctx, s.db)
}

func verr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

// SaveSettings 는 설정을 저장한다(scim:manage, 세션 전용). 바뀐 키마다 감사 scim.settings_set 한 건이다.
func (s *Service) SaveSettings(ctx context.Context, p contract.Principal, in Settings) (Settings, error) {
	if err := s.authorizeWrite(ctx, p); err != nil {
		return Settings{}, err
	}
	in.DefaultUnit, in.DefaultRank, in.DefaultRole = strings.TrimSpace(in.DefaultUnit), strings.TrimSpace(in.DefaultRank), strings.TrimSpace(in.DefaultRole)
	if in.GroupMode == "" {
		in.GroupMode = "off"
	}
	if in.GroupMode != "off" && in.GroupMode != "unit" && in.GroupMode != "role" {
		return Settings{}, verr("그룹 연동 방식은 사용 안 함·조직·역할 중 하나여야 합니다.")
	}
	if in.GroupMode == "role" && s.access == nil {
		return Settings{}, verr("이 서버에서는 그룹→역할 연동을 쓸 수 없습니다.")
	}
	if in.DefaultRole == "" {
		in.DefaultRole = "staff"
	}
	actor := s.webActor(p)
	var saved Settings
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if in.DefaultUnit != "" {
			if _, err := s.org.ResolveUnitRef(ctx, tx, in.DefaultUnit); err != nil {
				return verr("기본 조직 '%s' 을(를) 찾을 수 없습니다. 조직 코드를 확인하세요.", in.DefaultUnit)
			}
		}
		if in.DefaultRank != "" {
			if _, err := rankID(ctx, tx, in.DefaultRank); err != nil {
				return verr("기본 직위 '%s' 이(가) 직위 표에 없습니다.", in.DefaultRank)
			}
		}
		var rn int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM roles WHERE key = ? AND deleted_at IS NULL`, in.DefaultRole).Scan(&rn); err != nil {
			return err
		}
		if rn == 0 {
			return verr("기본 역할이 올바르지 않습니다.")
		}
		if err := access.CheckAssignable(ctx, tx, in.DefaultRole); err != nil {
			return err // 위험 권한이 든 역할은 저장하지 않는다(RiskyRoleError, 400)
		}
		old, err := s.loadSettings(ctx, tx)
		if err != nil {
			return err
		}
		if in.GroupMode != old.GroupMode { // 한 번에 하나만: 다른 방식의 매핑이 남아 있으면 먼저 지우게 한다
			var other int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_group_maps WHERE kind <> ?`, in.GroupMode).Scan(&other); err != nil {
				return err
			}
			if other > 0 {
				return verr("다른 방식의 그룹 매핑이 남아 있습니다. 그룹 매핑을 먼저 삭제한 뒤 방식을 바꾸세요.")
			}
		}
		now := s.stamp()
		set := func(key, oldV, newV string) error {
			if oldV == newV {
				return nil
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES (?,?,?,?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`, key, newV, p.UserID, now); err != nil {
				return err
			}
			return s.record(ctx, tx, actor, "scim.settings_set", audit.TargetSetting, key, audit.ResultSuccess,
				[]audit.DiffItem{{Key: key, Old: oldV, New: newV}}, "")
		}
		for _, kv := range [][3]string{
			{"scim.enabled", boolText(old.Enabled), boolText(in.Enabled)},
			{"scim.group_mode", old.GroupMode, in.GroupMode},
			{"scim.default_unit", old.DefaultUnit, in.DefaultUnit},
			{"scim.default_rank", old.DefaultRank, in.DefaultRank},
			{"scim.default_role", old.DefaultRole, in.DefaultRole},
			{"scim.adopt_existing", boolText(old.AdoptExisting), boolText(in.AdoptExisting)},
		} {
			if err := set(kv[0], kv[1], kv[2]); err != nil {
				return err
			}
		}
		saved = in
		return nil
	})
	return saved, err
}

func rankID(ctx context.Context, q querier, name string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM org_ranks WHERE name = ? AND active = 1`, strings.TrimSpace(name)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("직위 없음")
	}
	return id, err
}
