// Package module 은 업무 모듈(근태·휴가 같은 켜고 끄는 기능) 켜기·끄기 장치다. 4절.
//
// 모듈 선언은 코드 표 한 곳(defs)이고, 켜기 상태는 설치 단위 한 값이다(app_settings 키 `module.<키>` = on|off, 없으면 off).
// 꺼진 모듈은 없는 것과 같다: 라우트는 404(Gate), MCP 도구는 목록에서 빠지고(Enabled), 서버 작업은 건너뛴다. 데이터는 지우지 않는다.
// 켜려면 필수 점검 항목이 모두 통과해야 한다(Checker 가 모듈별 점검표를 만든다). 켜기·끄기는 감사에 남는다.
package module

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 모듈 키.
const KeyAttendance = "attendance"

// ActionSet 은 감사 동작 이름이다(auditLabels 와 같다).
const ActionSet = "module.set"

// Def 는 모듈 선언이다.
type Def struct {
	Key         string
	Name        string
	Description string
	// Permission 은 켜기·끄기·점검표 조회에 필요한 권한이다(위험 권한·세션 전용).
	Permission contract.Permission
	// Needs 는 먼저 켜져 있어야 하는 모듈 키다.
	Needs []string
}

var defs = []Def{
	{Key: KeyAttendance, Name: "근태·휴가", Description: "휴가 신청(전자결재)·연차 잔여·출퇴근 기록·휴직·부서 달력·월 근태 내보내기. 급여 계산은 하지 않습니다.", Permission: contract.PermAttendanceManageModule},
}

// Defs 는 모듈 선언 목록이다(복사본).
func Defs() []Def { return append([]Def(nil), defs...) }

// Lookup 은 키로 선언을 찾는다.
func Lookup(key string) (Def, bool) {
	for _, d := range defs {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

// Check 는 켜기 전 점검표의 한 항목이다.
type Check struct {
	Label    string
	Required bool
	OK       bool
	Detail   string // 상태를 한 줄로(예: "등록된 공휴일 12건")
}

// Checker 는 모듈의 점검표를 만든다. 다른 단계·패키지가 RegisterChecker 로 더한다.
type Checker func(ctx context.Context, q contract.Tx, now time.Time) ([]Check, error)

// Options 는 서비스 구성이다.
type Options struct {
	DB    contract.Tx
	Txs   contract.TxManager
	Audit audit.Recorder
	Now   func() time.Time
}

// Service 는 모듈 켜기·끄기 서비스다. 웹 핸들러·MCP·서버 작업이 같은 Enabled 를 쓴다.
type Service struct {
	o        Options
	mu       sync.RWMutex
	checkers map[string][]Checker
}

// New 는 서비스를 만든다. 기본 점검표(근태·휴가의 공휴일·조직 등록)가 들어 있다.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{o: o, checkers: map[string][]Checker{}}
	s.RegisterChecker(KeyAttendance, attendanceBaseChecks)
	return s
}

// RegisterChecker 는 모듈 점검표 생성기를 더한다(여러 개면 순서대로 이어 붙인다).
func (s *Service) RegisterChecker(key string, c Checker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkers[key] = append(s.checkers[key], c)
}

func settingKey(key string) string { return "module." + key }

// Enabled 는 모듈이 켜져 있는지다. 읽기에 실패하면 꺼짐으로 본다(fail-closed).
func (s *Service) Enabled(ctx context.Context, key string) bool {
	if s == nil || s.o.DB == nil {
		return false
	}
	var v string
	err := s.o.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, settingKey(key)).Scan(&v)
	return err == nil && v == "on"
}

// EnabledTx 는 Enabled 와 같되 호출자의 트랜잭션(또는 질의 핸들)으로 읽는다. 트랜잭션 안에서는 이쪽만 쓴다:
// SQLite 는 연결이 하나라 트랜잭션 밖 풀 연결로 읽으면 영구 대기한다(B50 판정 후속).
func (s *Service) EnabledTx(ctx context.Context, q contract.Tx, key string) bool {
	if s == nil || q == nil {
		return false
	}
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, settingKey(key)).Scan(&v)
	return err == nil && v == "on"
}

// Gate 는 모듈이 꺼져 있으면 권한이 있는 사람에게도 404 로 답하는 핸들러 래퍼다(꺼진 모듈은 없는 것과 같다).
func (s *Service) Gate(key string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Enabled(r.Context(), key) {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}
}

// EnabledKeys 는 켜져 있는 모듈 키 목록이다(/api/me 가 화면에 알린다).
func (s *Service) EnabledKeys(ctx context.Context) []string {
	out := []string{}
	for _, d := range defs {
		if s.Enabled(ctx, d.Key) {
			out = append(out, d.Key)
		}
	}
	return out
}

// View 는 화면용 모듈 한 건이다.
type View struct {
	Key         string
	Name        string
	Description string
	Enabled     bool
	Needs       []string
}

func (s *Service) authorize(ctx context.Context, d Def) (contract.Principal, error) {
	return contract.Authorize(ctx, d.Permission)
}

// List 는 호출자가 켜기·끄기를 할 수 있는 모듈 목록이다. 권한이 없는 모듈은 없는 것과 같다.
func (s *Service) List(ctx context.Context) ([]View, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return nil, contract.ErrUnauthenticated
	}
	var out []View
	for _, d := range defs {
		if !p.Can(d.Permission) {
			continue
		}
		out = append(out, View{Key: d.Key, Name: d.Name, Description: d.Description, Enabled: s.Enabled(ctx, d.Key), Needs: d.Needs})
	}
	if len(out) == 0 {
		return nil, contract.ErrForbidden
	}
	return out, nil
}

// Checklist 는 모듈의 켜기 전 점검표다(필수 먼저).
func (s *Service) Checklist(ctx context.Context, key string) ([]Check, error) {
	d, ok := Lookup(key)
	if !ok {
		return nil, contract.ErrNotFound
	}
	if _, err := s.authorize(ctx, d); err != nil {
		return nil, err
	}
	return s.checks(ctx, d)
}

func (s *Service) checks(ctx context.Context, d Def) ([]Check, error) {
	s.mu.RLock()
	cs := append([]Checker(nil), s.checkers[d.Key]...)
	s.mu.RUnlock()
	var out []Check
	for _, c := range cs {
		items, err := c(ctx, s.o.DB, s.o.Now())
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Required && !out[j].Required })
	return out, nil
}

// SetEnabled 는 모듈을 켜거나 끈다. 켜려면 필요한 모듈이 켜져 있고 필수 점검 항목이 모두 통과해야 한다. 같은 상태로 바꾸는 요청은 아무것도 하지 않는다.
// 끄는 것은 항상 된다(데이터는 그대로). 변경은 같은 트랜잭션에서 감사에 이전·새 값으로 남는다.
func (s *Service) SetEnabled(ctx context.Context, key string, on bool) error {
	d, ok := Lookup(key)
	if !ok {
		return contract.ErrNotFound
	}
	p, err := s.authorize(ctx, d)
	if err != nil {
		return err
	}
	cur := s.Enabled(ctx, key)
	if cur == on {
		return nil
	}
	if on {
		for _, n := range d.Needs {
			if !s.Enabled(ctx, n) {
				nd, _ := Lookup(n)
				return fmt.Errorf("%w: 먼저 「%s」 모듈을 켜야 합니다", contract.ErrInvalidState, nd.Name)
			}
		}
		items, err := s.checks(ctx, d)
		if err != nil {
			return err
		}
		var miss []string
		for _, c := range items {
			if c.Required && !c.OK {
				miss = append(miss, c.Label)
			}
		}
		if len(miss) > 0 {
			return fmt.Errorf("%w: 필수 점검 항목을 먼저 마치세요: %s", contract.ErrInvalidState, strings.Join(miss, ", "))
		}
	} else {
		// 이 모듈을 필요로 하는 켜진 모듈이 있으면 끌 수 없다.
		for _, o := range defs {
			for _, n := range o.Needs {
				if n == key && s.Enabled(ctx, o.Key) {
					return fmt.Errorf("%w: 「%s」 모듈이 이 모듈을 쓰고 있어 끌 수 없습니다", contract.ErrInvalidState, o.Name)
				}
			}
		}
	}
	val, label := "off", "꺼짐"
	oldLabel := "켜짐"
	if on {
		val, label, oldLabel = "on", "켜짐", "꺼짐"
	}
	now := s.o.Now().UTC().Format(time.RFC3339)
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`, settingKey(key), val, p.UserID, now); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: ActionSet, TargetType: audit.TargetModule, TargetID: key, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "enabled", Old: oldLabel, New: label}}})
	})
}
