package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"net/http"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// 다법인(B44 10절): 요청마다 소속·겸직 법인과 권한별 효력 법인을 읽어 Principal 에 채우고, 현재 법인(세션 값·X-Entity-ID 헤더)을 정해 context 에 싣는다.
// 현재 법인은 그 사람이 고를 수 있는 법인(Principal.SelectableEntities) 안일 때만 허용한다. 밖이면 403 이다.

const entityHeader = "X-Entity-ID"

// sessionAll 은 sessions.active_entity_id 에서 "전체" 모드를 뜻하는 값이다(0 은 정하지 않음).
const sessionAll int64 = -1

// loadEntities 는 Principal 의 OwnEntities·EntityPerms 를 채운다. 조직에 배치되지 않은 사람은 소속 법인이 없지만, 활성 법인이 하나뿐인 설치에서는
// 그 법인을 소속으로 본다(법인이 하나뿐일 때 동작이 이전과 같도록: 조직도 등록 전 사용자가 사용자 직접 부여로 일하던 경로).
func (s *Service) loadEntities(ctx context.Context, p *contract.Principal) error {
	_, own, err := org.EntitiesOf(ctx, s.db, p.UserID, contract.DayOf(s.now()))
	if err != nil {
		return err
	}
	if len(own) == 0 {
		var ids []int64
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM entities WHERE active = 1 ORDER BY id LIMIT 2`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 1 {
			own = ids
		}
	}
	p.OwnEntities = own
	p.EntityPerms, err = access.EntityPermsOf(ctx, s.db, p.UserID, own)
	return err
}

// entityByCode 는 법인 코드로 번호를 찾는다.
func (s *Service) entityByCode(ctx context.Context, code string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM entities WHERE code = ? AND active = 1`, strings.ToUpper(strings.TrimSpace(code))).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

var errEntityDenied = errors.New("고를 수 없는 법인입니다")

// resolveEntity 는 요청의 현재 법인을 정한다. 헤더가 세션 값보다 우선하고, 둘 다 없으면 소속 법인(없으면 기본 법인)이다.
func (s *Service) resolveEntity(ctx context.Context, p contract.Principal, r *http.Request, sessionVal int64) (contract.ActiveEntity, error) {
	sel := p.SelectableEntities()
	if h := strings.TrimSpace(r.Header.Get(entityHeader)); h != "" {
		if h == "*" {
			if !sel.All {
				return contract.ActiveEntity{}, errEntityDenied
			}
			return contract.ActiveEntity{All: true}, nil
		}
		id, ok, err := s.entityByCode(ctx, h)
		if err != nil {
			return contract.ActiveEntity{}, err
		}
		if !ok || !sel.Has(id) {
			return contract.ActiveEntity{}, errEntityDenied
		}
		return contract.ActiveEntity{ID: id}, nil
	}
	if sessionVal == sessionAll && sel.All {
		return contract.ActiveEntity{All: true}, nil
	}
	if sessionVal > 0 && sel.Has(sessionVal) {
		return contract.ActiveEntity{ID: sessionVal}, nil // 정했던 법인이 범위 밖이 되면(겸직 종료·회수) 아래 기본값으로 되돌린다(명세 E14)
	}
	if len(p.OwnEntities) > 0 {
		return contract.ActiveEntity{ID: p.OwnEntities[0]}, nil
	}
	return contract.ActiveEntity{ID: contract.DefaultEntityID}, nil
}

// sessionEntity 는 세션 행의 현재 법인 값이다.
func (s *Service) sessionEntity(ctx context.Context, raw string) int64 {
	var v int64
	_ = s.db.QueryRowContext(ctx, `SELECT active_entity_id FROM sessions WHERE id_hash = ?`, sha256Hex(raw)).Scan(&v)
	return v
}

type entityInfo struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// selectableEntities 는 현재 법인으로 고를 수 있는 활성 법인 목록이다.
func (s *Service) selectableEntities(ctx context.Context, p contract.Principal) ([]entityInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, code, name FROM entities WHERE active = 1 ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sel := p.SelectableEntities()
	out := []entityInfo{}
	for rows.Next() {
		var e entityInfo
		if err := rows.Scan(&e.ID, &e.Code, &e.Name); err != nil {
			return nil, err
		}
		if sel.Has(e.ID) {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// meEntities 는 /api/me 에 실을 법인 정보다: 고를 수 있는 법인, 현재 법인, 그룹(전체) 모드 가능 여부.
func (s *Service) meEntities(ctx context.Context, p contract.Principal) map[string]any {
	list, err := s.selectableEntities(ctx, p)
	if err != nil {
		list = []entityInfo{}
	}
	out := map[string]any{"entities": list, "groupScope": p.SelectableEntities().All}
	cur := map[string]any{"all": false}
	if a, ok := contract.ActiveEntityFrom(ctx); ok {
		cur["all"] = a.All
		for _, e := range list {
			if e.ID == a.ID && !a.All {
				cur["id"], cur["code"], cur["name"] = e.ID, e.Code, e.Name
			}
		}
	}
	out["currentEntity"] = cur
	return out
}

// handleSetEntity 는 세션의 현재 법인을 바꾼다. 본문 {"entity": "<법인 코드>"} 또는 {"entity": "*"}(전체: 그룹 범위가 있을 때만).
func (s *Service) handleSetEntity(w http.ResponseWriter, r *http.Request) {
	p, _ := contract.PrincipalFrom(r.Context())
	var in struct {
		Entity string `json:"entity"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil {
		http.Error(w, "요청 형식이 올바르지 않습니다", http.StatusBadRequest)
		return
	}
	raw := rawSessionCookie(r)
	if raw == "" {
		http.Error(w, "현재 법인은 로그인한 화면에서만 바꿀 수 있습니다", http.StatusForbidden)
		return
	}
	sel := p.SelectableEntities()
	var val int64
	if strings.TrimSpace(in.Entity) == "*" {
		if !sel.All {
			s.recordEntityDenied(r, p, audit.NoEntity, "*", "현재 법인 지정", "전체 법인 조회 권한 없음")
			http.Error(w, "전체 법인 조회 권한이 없습니다", http.StatusForbidden)
			return
		}
		val = sessionAll
	} else {
		id, ok, err := s.entityByCode(r.Context(), in.Entity)
		if err != nil {
			http.Error(w, "처리 중 오류가 발생했습니다", http.StatusInternalServerError)
			return
		}
		if !ok || !sel.Has(id) {
			s.recordEntityDenied(r, p, audit.NoEntity, entityCodeForAudit(in.Entity), "현재 법인 지정", "고를 수 없는 법인")
			http.Error(w, "고를 수 없는 법인입니다", http.StatusForbidden)
			return
		}
		val = id
	}
	if _, err := s.db.ExecContext(r.Context(), `UPDATE sessions SET active_entity_id = ? WHERE id_hash = ? AND user_id = ?`, val, sha256Hex(raw), p.UserID); err != nil {
		http.Error(w, "처리 중 오류가 발생했습니다", http.StatusInternalServerError)
		return
	}
	a := contract.ActiveEntity{ID: val}
	if val == sessionAll {
		a = contract.ActiveEntity{All: true}
	}
	writeJSON(w, http.StatusOK, s.meEntities(contract.WithActiveEntity(r.Context(), a), p))
}
