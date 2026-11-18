// Package entity 는 법인·사업장 관리(B44 1단계 platform, 4·7절)다.
//
// 불변식
//   - 법인은 지우지 않는다(사용 여부만). 기본 법인(id 1)은 비활성화할 수 없고, 기본 법인의 회사 정보는 회사 설정 화면(B34)이 정본이다.
//   - 조직의 법인은 루트(부모 없음) 조직이 어느 법인에 연결돼 있는가로 정해진다. 연결을 바꾸면 조직 구간을 다시 계산한다.
//   - 모든 쓰기는 같은 트랜잭션에서 감사를 남기고, 감사의 법인은 그 법인이다.
package entity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/org"
)

// OrgHook 는 조직 서비스의 캐시 무효화다(루트 연결 뒤 부른다). nil 이면 부르지 않는다.
type OrgHook interface{ Invalidate() }

// Options 는 Service 구성이다.
type Options struct {
	DB    *sql.DB
	Txs   contract.TxManager
	Audit audit.Recorder
	Org   OrgHook
	Now   func() time.Time
}

// Service 는 법인·사업장을 관리한다.
type Service struct{ o Options }

// New 는 Service 를 만든다.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

func valErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

func stateErr(msg string) error { return fmt.Errorf("%w: %s", contract.ErrInvalidState, msg) }

var (
	reCode       = regexp.MustCompile(`^[A-Z0-9]{2,6}$`)
	reBusinessNo = regexp.MustCompile(`^\d{3}-\d{2}-\d{5}$`)
	reSiteNo     = regexp.MustCompile(`^\d{4}$`)
)

// Entity 는 법인 한 건이다.
type Entity struct {
	ID               int64  `json:"id"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	BusinessNo       string `json:"businessNo"`
	Representative   string `json:"representative"`
	Address          string `json:"address"`
	Phone            string `json:"phone"`
	FiscalStartMonth int    `json:"fiscalStartMonth"`
	RootUnitID       int64  `json:"rootUnitId"`
	RootUnitName     string `json:"rootUnitName"`
	IsDefault        bool   `json:"isDefault"`
	Active           bool   `json:"active"`
	Version          int64  `json:"version"`
	Members          int    `json:"members"` // 소속 조직 수(루트 포함)
	Sites            int    `json:"sites"`
	UpdatedAt        string `json:"updatedAt"`
	// Warnings 는 등록 응답에만 실리는 안내다(막지 않는다): 법인이 둘이 되면 조직에 배치되지 않은 사용자의 소속 법인 범위 권한이 효력을 잃는다.
	Warnings []string `json:"warnings,omitempty"`
}

// Site 는 사업장 한 건이다.
type Site struct {
	ID        int64  `json:"id"`
	EntityID  int64  `json:"entityId"`
	SiteNo    string `json:"siteNo"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	KindName  string `json:"kindName"`
	Address   string `json:"address"`
	VATMode   string `json:"vatMode"`
	VATName   string `json:"vatName"`
	Active    bool   `json:"active"`
	UpdatedAt string `json:"updatedAt"`
}

// RootCandidate 는 법인에 연결할 수 있는 루트 조직(부모가 없고 다른 법인에 연결되지 않은 조직)이다.
type RootCandidate struct {
	UnitID int64  `json:"unitId"`
	Name   string `json:"name"`
	Code   string `json:"code"`
}

// Listing 은 법인 관리 화면의 목록 응답이다.
type Listing struct {
	Entities       []Entity        `json:"entities"`
	RootCandidates []RootCandidate `json:"rootCandidates"`
}

var kindNames = map[string]string{"head": "주사업장", "branch": "지점", "factory": "공장"}
var vatNames = map[string]string{"own": "사업장별 신고", "consolidated": "주사업장 총괄납부"}

func (s *Service) now() string { return s.o.Now().UTC().Format(time.RFC3339) }

func (s *Service) authorize(ctx context.Context) (contract.Principal, error) {
	return contract.Authorize(ctx, contract.PermEntityManage)
}

func (s *Service) record(ctx context.Context, tx contract.Tx, p contract.Principal, action string, tt audit.TargetType, id, entityID int64, diff ...audit.DiffItem) error {
	return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: tt, TargetID: fmt.Sprint(id), Result: audit.ResultSuccess, Diff: diff, EntityID: entityID})
}

const entityCols = `e.id, e.code, e.name, e.business_no, e.representative, e.address, e.phone, e.fiscal_start_month, COALESCE(e.root_unit_id, 0),
	COALESCE((SELECT s.name FROM org_unit_spans s WHERE s.unit_id = e.root_unit_id ORDER BY s.valid_from DESC LIMIT 1), ''),
	e.is_default, e.active, e.version, e.updated_at,
	(SELECT COUNT(DISTINCT s.unit_id) FROM org_unit_spans s WHERE s.entity_id = e.id),
	(SELECT COUNT(*) FROM entity_sites t WHERE t.entity_id = e.id AND t.active = 1)`

func scanEntity(sc interface{ Scan(...any) error }) (Entity, error) {
	var e Entity
	var def, act int
	err := sc.Scan(&e.ID, &e.Code, &e.Name, &e.BusinessNo, &e.Representative, &e.Address, &e.Phone, &e.FiscalStartMonth, &e.RootUnitID, &e.RootUnitName,
		&def, &act, &e.Version, &e.UpdatedAt, &e.Members, &e.Sites)
	e.IsDefault, e.Active = def == 1, act == 1
	return e, err
}

// List 는 법인 목록과 연결 가능한 루트 조직을 돌려준다(entity:manage).
func (s *Service) List(ctx context.Context) (Listing, error) {
	if _, err := s.authorize(ctx); err != nil {
		return Listing{}, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT `+entityCols+` FROM entities e ORDER BY e.is_default DESC, e.id`)
	if err != nil {
		return Listing{}, err
	}
	out := Listing{Entities: []Entity{}, RootCandidates: []RootCandidate{}}
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			rows.Close()
			return Listing{}, err
		}
		out.Entities = append(out.Entities, e)
	}
	rows.Close()
	cands, err := s.o.DB.QueryContext(ctx, `SELECT s.unit_id, s.name, s.code FROM org_unit_spans s
		WHERE s.parent_id = 0 AND s.valid_to IS NULL AND s.closed = 0 AND NOT EXISTS (SELECT 1 FROM entities e WHERE e.root_unit_id = s.unit_id) ORDER BY s.unit_id`)
	if err != nil {
		return Listing{}, err
	}
	defer cands.Close()
	for cands.Next() {
		var c RootCandidate
		if err := cands.Scan(&c.UnitID, &c.Name, &c.Code); err != nil {
			return Listing{}, err
		}
		out.RootCandidates = append(out.RootCandidates, c)
	}
	return out, cands.Err()
}

func (s *Service) get(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, id int64) (Entity, error) {
	e, err := scanEntity(q.QueryRowContext(ctx, `SELECT `+entityCols+` FROM entities e WHERE e.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, contract.ErrNotFound
	}
	return e, err
}

// Get 은 법인 한 건이다(entity:manage).
func (s *Service) Get(ctx context.Context, id int64) (Entity, error) {
	if _, err := s.authorize(ctx); err != nil {
		return Entity{}, err
	}
	return s.get(ctx, s.o.DB, id)
}

// Input 은 법인 등록·수정 입력이다.
type Input struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	BusinessNo       string `json:"businessNo"`
	Representative   string `json:"representative"`
	Address          string `json:"address"`
	Phone            string `json:"phone"`
	FiscalStartMonth int    `json:"fiscalStartMonth"`
	RootUnitID       int64  `json:"rootUnitId"`
	Version          int64  `json:"version"` // 수정 때 낙관적 잠금
}

func (in *Input) clean(creating bool) error {
	in.Code, in.Name, in.BusinessNo = strings.ToUpper(strings.TrimSpace(in.Code)), strings.TrimSpace(in.Name), strings.TrimSpace(in.BusinessNo)
	in.Representative, in.Address, in.Phone = strings.TrimSpace(in.Representative), strings.TrimSpace(in.Address), strings.TrimSpace(in.Phone)
	if creating && !reCode.MatchString(in.Code) {
		return valErr("법인 코드는 영문 대문자·숫자 2~6자로 입력하세요.")
	}
	if n := len([]rune(in.Name)); n < 1 || n > 100 {
		return valErr("법인 이름은 1~100자로 입력하세요.")
	}
	if in.BusinessNo != "" && !reBusinessNo.MatchString(in.BusinessNo) {
		return valErr("사업자번호는 000-00-00000 형식으로 입력하세요.")
	}
	if len([]rune(in.Representative)) > 50 || len([]rune(in.Address)) > 200 || len([]rune(in.Phone)) > 30 {
		return valErr("대표자·주소·전화 입력이 너무 깁니다.")
	}
	if in.FiscalStartMonth == 0 {
		in.FiscalStartMonth = 1
	}
	if in.FiscalStartMonth < 1 || in.FiscalStartMonth > 12 {
		return valErr("회계연도 시작월은 1~12 로 입력하세요.")
	}
	return nil
}

// Create 는 법인을 등록한다. 사업자번호는 다른 법인과 겹칠 수 없고, 주사업장(0000)이 함께 만들어진다. 루트 조직을 함께 고르면 연결한다.
func (s *Service) Create(ctx context.Context, in Input) (Entity, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Entity{}, err
	}
	if err := in.clean(true); err != nil {
		return Entity{}, err
	}
	var id int64
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		now := s.now()
		var root any
		if in.RootUnitID != 0 {
			if err := s.checkRoot(ctx, tx, in.RootUnitID, 0); err != nil {
				return err
			}
			root = in.RootUnitID
		}
		if err := s.checkUnique(ctx, tx, 0, in.Code, in.BusinessNo); err != nil {
			return err
		}
		var err error
		id, err = dbutil.InsertID(ctx, tx, `INSERT INTO entities (code, name, business_no, representative, address, phone, fiscal_start_month, root_unit_id, is_default, active, version, created_by, created_at, updated_by, updated_at)
			VALUES (?,?,?,?,?,?,?,?,0,1,1,?,?,?,?)`, in.Code, in.Name, in.BusinessNo, in.Representative, in.Address, in.Phone, in.FiscalStartMonth, root, p.UserID, now, p.UserID, now)
		if err != nil {
			if dbutil.IsUnique(err) { // 동시 등록과 겹친 경우(미리 확인한 뒤에 같은 값이 들어왔다)
				return valErr("같은 코드나 사업자번호의 법인이 이미 있습니다.")
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO entity_sites (entity_id, site_no, name, kind, vat_mode, created_at, updated_at) VALUES (?, '0000', '주사업장', 'head', 'own', ?, ?)`, id, now, now); err != nil {
			return err
		}
		if in.RootUnitID != 0 {
			if err := s.rebuildOrg(ctx, tx); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "entity.create", audit.TargetEntity, id, id,
			audit.DiffItem{Key: "entity_code", New: in.Code}, audit.DiffItem{Key: "entity_name", New: in.Name}, audit.DiffItem{Key: "business_no", New: in.BusinessNo})
	})
	if err != nil {
		return Entity{}, err
	}
	s.invalidate()
	out, err := s.get(ctx, s.o.DB, id)
	if err != nil {
		return out, err
	}
	out.Warnings = s.unplacedWarning(ctx)
	return out, nil
}

// unplacedWarning 은 활성 법인이 둘 이상이고 조직에 배치되지 않은 활성 사용자가 있으면 안내 문구를 돌려준다(명세 E2: 이 사용자의 own 범위 권한은 효력이 없다).
func (s *Service) unplacedWarning(ctx context.Context) []string {
	var active int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE active = 1`).Scan(&active); err != nil || active < 2 {
		return nil
	}
	day := string(contract.DayOf(s.o.Now()))
	var n int
	if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users u WHERE u.active = 1 AND NOT EXISTS (
		SELECT 1 FROM org_person_spans p WHERE p.user_id = u.id AND p.left_flag = 0 AND p.valid_from <= ?1 AND (p.valid_to IS NULL OR p.valid_to > ?1))`, day).Scan(&n); err != nil || n == 0 {
		return nil
	}
	return []string{fmt.Sprintf("조직에 배치되지 않은 사용자 %d명은 법인이 둘 이상이면 소속 법인 범위의 권한을 쓸 수 없습니다. 조직도에 배치하거나 법인 범위(법인 지정·그룹) 권한을 따로 부여하세요.", n)}
}

// checkUnique 는 코드·사업자번호(숫자만 비교) 중복을 미리 확인한다. 유일 제약 위반 뒤에는 PG 트랜잭션이 중단되어 원인을 다시 물을 수 없으므로 삽입 전에 한다.
func (s *Service) checkUnique(ctx context.Context, tx contract.Tx, selfID int64, code, bizNo string) error {
	var n int
	if code != "" {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE code = ? AND id <> ?`, code, selfID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return valErr("같은 코드의 법인이 이미 있습니다.")
		}
	}
	if bizNo != "" {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE replace(business_no, '-', '') = ? AND id <> ?`, strings.ReplaceAll(bizNo, "-", ""), selfID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return valErr("같은 사업자번호의 법인이 이미 있습니다.")
		}
	}
	return nil
}

// Update 는 법인 정보를 고친다(코드는 바꾸지 않는다: 문서 번호 접두가 코드다). 기본 법인의 회사 정보는 회사 설정 화면이 정본이라 여기서 고칠 수 없다.
func (s *Service) Update(ctx context.Context, id int64, in Input) (Entity, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Entity{}, err
	}
	in.Code = "XX" // 코드는 검사하지 않는다(바꾸지 않음)
	if err := in.clean(false); err != nil {
		return Entity{}, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		cur, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.IsDefault {
			return stateErr("기본 법인의 회사 정보는 관리 > 회사 설정에서 바꿉니다.")
		}
		if in.Version != cur.Version {
			return stateErr("다른 사람이 먼저 고쳤습니다. 화면을 새로 열어 다시 입력하세요.")
		}
		if err := s.checkUnique(ctx, tx, id, "", in.BusinessNo); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entities SET name = ?, business_no = ?, representative = ?, address = ?, phone = ?, fiscal_start_month = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = ?`,
			in.Name, in.BusinessNo, in.Representative, in.Address, in.Phone, in.FiscalStartMonth, p.UserID, s.now(), id); err != nil {
			if dbutil.IsUnique(err) {
				return valErr("같은 사업자번호의 법인이 이미 있습니다.")
			}
			return err
		}
		return s.record(ctx, tx, p, "entity.update", audit.TargetEntity, id, id,
			audit.DiffItem{Key: "entity_name", Old: cur.Name, New: in.Name}, audit.DiffItem{Key: "business_no", Old: cur.BusinessNo, New: in.BusinessNo})
	})
	if err != nil {
		return Entity{}, err
	}
	return s.get(ctx, s.o.DB, id)
}

// SetActive 는 법인을 켜거나 끈다. 기본 법인은 끌 수 없다.
func (s *Service) SetActive(ctx context.Context, id int64, on bool) (Entity, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Entity{}, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		cur, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if !on && cur.IsDefault {
			return stateErr("기본 법인은 비활성화할 수 없습니다.")
		}
		if cur.Active == on {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entities SET active = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = ?`, boolInt(on), p.UserID, s.now(), id); err != nil {
			return err
		}
		action := "entity.activate"
		if !on {
			action = "entity.deactivate"
		}
		return s.record(ctx, tx, p, action, audit.TargetEntity, id, id, audit.DiffItem{Key: "active", Old: yesNo(cur.Active), New: yesNo(on)})
	})
	if err != nil {
		return Entity{}, err
	}
	return s.get(ctx, s.o.DB, id)
}

// checkRoot 는 루트 조직으로 연결할 수 있는지 본다: 있는 조직이고 부모가 없고 다른 법인에 연결돼 있지 않아야 한다(selfEntity 는 이미 연결된 법인).
func (s *Service) checkRoot(ctx context.Context, tx contract.Tx, unitID, selfEntity int64) error {
	var parent, n int64
	err := tx.QueryRowContext(ctx, `SELECT parent_id FROM org_unit_spans WHERE unit_id = ? ORDER BY valid_from DESC LIMIT 1`, unitID).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return valErr("연결할 조직을 찾을 수 없습니다.")
	}
	if err != nil {
		return err
	}
	if parent != 0 {
		return valErr("법인의 루트 조직은 상위 조직이 없는 조직이어야 합니다.")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entities WHERE root_unit_id = ? AND id <> ?`, unitID, selfEntity).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return valErr("이미 다른 법인에 연결된 조직입니다.")
	}
	return nil
}

// SetRoot 는 법인의 루트 조직을 연결한다(0 이면 연결을 푼다: 조직이 기본 법인으로 돌아간다). 기본 법인의 연결은 바꿀 수 없다.
// 조직 구간의 법인 열을 다시 계산한다.
func (s *Service) SetRoot(ctx context.Context, id, unitID int64) (Entity, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Entity{}, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		cur, err := s.get(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.IsDefault {
			return stateErr("기본 법인의 루트 조직은 바꿀 수 없습니다.")
		}
		var root any
		if unitID != 0 {
			if err := s.checkRoot(ctx, tx, unitID, id); err != nil {
				return err
			}
			root = unitID
		}
		if cur.RootUnitID == unitID {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entities SET root_unit_id = ?, version = version + 1, updated_by = ?, updated_at = ? WHERE id = ?`, root, p.UserID, s.now(), id); err != nil {
			return err
		}
		if err := s.rebuildOrg(ctx, tx); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "entity.root_set", audit.TargetEntity, id, id, audit.DiffItem{Key: "root_unit", Old: cur.RootUnitName, New: s.unitName(ctx, tx, unitID)})
	})
	if err != nil {
		return Entity{}, err
	}
	s.invalidate()
	return s.get(ctx, s.o.DB, id)
}

func (s *Service) unitName(ctx context.Context, tx contract.Tx, unitID int64) string {
	var n string
	if unitID == 0 || tx.QueryRowContext(ctx, `SELECT name FROM org_unit_spans WHERE unit_id = ? ORDER BY valid_from DESC LIMIT 1`, unitID).Scan(&n) != nil {
		return "연결 없음"
	}
	return n
}

// rebuildOrg 는 조직 구간을 다시 써서 법인 열을 맞춘다(정본인 이벤트 로그는 바뀌지 않는다).
func (s *Service) rebuildOrg(ctx context.Context, tx contract.Tx) error {
	if _, err := org.Rebuild(ctx, tx); err != nil {
		return fmt.Errorf("조직 구간을 다시 계산하지 못했습니다: %w", err)
	}
	return nil
}

func (s *Service) invalidate() {
	if s.o.Org != nil {
		s.o.Org.Invalidate()
	}
}

// ---- 사업장 ----

// ListSites 는 법인의 사업장 목록이다(entity:manage).
func (s *Service) ListSites(ctx context.Context, entityID int64) ([]Site, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if _, err := s.get(ctx, s.o.DB, entityID); err != nil {
		return nil, err
	}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT id, entity_id, site_no, name, kind, address, vat_mode, active, updated_at FROM entity_sites WHERE entity_id = ? ORDER BY site_no`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Site{}
	for rows.Next() {
		var t Site
		var act int
		if err := rows.Scan(&t.ID, &t.EntityID, &t.SiteNo, &t.Name, &t.Kind, &t.Address, &t.VATMode, &act, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Active, t.KindName, t.VATName = act == 1, kindNames[t.Kind], vatNames[t.VATMode]
		out = append(out, t)
	}
	return out, rows.Err()
}

// SiteInput 은 사업장 등록·수정 입력이다.
type SiteInput struct {
	SiteNo  string `json:"siteNo"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Address string `json:"address"`
	VATMode string `json:"vatMode"`
	Active  *bool  `json:"active"`
}

func (in *SiteInput) clean() error {
	in.SiteNo, in.Name, in.Address = strings.TrimSpace(in.SiteNo), strings.TrimSpace(in.Name), strings.TrimSpace(in.Address)
	if in.VATMode == "" {
		in.VATMode = "own"
	}
	if !reSiteNo.MatchString(in.SiteNo) {
		return valErr("종사업장번호는 숫자 4자리로 입력하세요(주사업장은 0000).")
	}
	if n := len([]rune(in.Name)); n < 1 || n > 60 {
		return valErr("사업장 이름은 1~60자로 입력하세요.")
	}
	if _, ok := kindNames[in.Kind]; !ok {
		return valErr("사업장 종류를 고르세요(주사업장·지점·공장).")
	}
	if _, ok := vatNames[in.VATMode]; !ok {
		return valErr("부가세 신고 방식이 올바르지 않습니다.")
	}
	if len([]rune(in.Address)) > 200 {
		return valErr("주소가 너무 깁니다.")
	}
	if (in.Kind == "head") != (in.SiteNo == "0000") {
		return valErr("주사업장의 종사업장번호는 0000 이고, 다른 사업장은 0000 을 쓸 수 없습니다.")
	}
	return nil
}

// SaveSite 는 사업장을 등록(siteID 0)하거나 고친다. 주사업장은 법인마다 하나이고 종류를 바꿀 수 없다.
func (s *Service) SaveSite(ctx context.Context, entityID, siteID int64, in SiteInput) (Site, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Site{}, err
	}
	if err := in.clean(); err != nil {
		return Site{}, err
	}
	var id int64
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := s.get(ctx, tx, entityID); err != nil {
			return err
		}
		now := s.now()
		act := 1
		if in.Active != nil && !*in.Active {
			act = 0
		}
		if siteID == 0 {
			var err error
			id, err = dbutil.InsertID(ctx, tx, `INSERT INTO entity_sites (entity_id, site_no, name, kind, address, vat_mode, active, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
				entityID, in.SiteNo, in.Name, in.Kind, in.Address, in.VATMode, act, now, now)
			if err != nil {
				if dbutil.IsUnique(err) {
					return valErr("같은 종사업장번호가 이미 있거나 주사업장이 이미 있습니다.")
				}
				return err
			}
		} else {
			id = siteID
			var curKind string
			var curEntity int64
			if err := tx.QueryRowContext(ctx, `SELECT kind, entity_id FROM entity_sites WHERE id = ?`, siteID).Scan(&curKind, &curEntity); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return contract.ErrNotFound
				}
				return err
			}
			if curEntity != entityID {
				return contract.ErrNotFound
			}
			if curKind != in.Kind {
				return stateErr("사업장 종류는 바꿀 수 없습니다.")
			}
			if curKind == "head" && act == 0 {
				return stateErr("주사업장은 비활성화할 수 없습니다.")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE entity_sites SET site_no = ?, name = ?, address = ?, vat_mode = ?, active = ?, updated_at = ? WHERE id = ?`,
				in.SiteNo, in.Name, in.Address, in.VATMode, act, now, siteID); err != nil {
				if dbutil.IsUnique(err) {
					return valErr("같은 종사업장번호가 이미 있습니다.")
				}
				return err
			}
		}
		return s.record(ctx, tx, p, "site.save", audit.TargetEntitySite, id, entityID,
			audit.DiffItem{Key: "site_no", New: in.SiteNo}, audit.DiffItem{Key: "site_name", New: in.Name}, audit.DiffItem{Key: "vat_mode", New: vatNames[in.VATMode]})
	})
	if err != nil {
		return Site{}, err
	}
	sites, err := s.ListSites(ctx, entityID)
	if err != nil {
		return Site{}, err
	}
	for _, t := range sites {
		if t.ID == id {
			return t, nil
		}
	}
	return Site{}, contract.ErrNotFound
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func yesNo(b bool) string {
	if b {
		return "예"
	}
	return "아니오"
}
