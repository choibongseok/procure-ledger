package setup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/auth"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/service"
)

// DemoDepartment 는 데모 데이터를 고른 설치에서 첫 관리자가 속하는 부서 이름이다. internal/org 의 데모 루트 조직 이름(SeedDemoOrg 의 회사)과 같아야 하고,
// 다르면 데모 시드가 "기존 부서 번호의 이름이 다릅니다"로 실패한다(cmd/server 의 데모 시험이 지킨다).
const DemoDepartment = "데모 주식회사"

// ErrAlreadyDone 은 다른 요청이 먼저 설치를 끝냈다는 충돌이다(HTTP 409). 부분 쓰기는 없다.
var ErrAlreadyDone = errors.New("이미 초기 설정이 끝났습니다")

// Input 은 마법사가 마지막 확인에서 한 번에 보내는 입력 전체다.
type Input struct {
	Company            domain.CompanyInput
	FiscalStartMonth   int // 0 이면 1
	AdminName          string
	AdminEmail         string
	AdminPassword      string
	Department         string // 선택. 비면 기본 부서 이름을 쓴다
	Demo               bool
	DemoConfirmCompany string // 데모를 고를 때 회사명을 다시 입력한 값
}

// FieldErrors 는 필드별 입력 오류다(필드 이름 → 한국어 문구). contract.ErrValidation 이다.
type FieldErrors map[string]string

func (e FieldErrors) Error() string {
	for _, v := range e {
		return v
	}
	return "입력이 올바르지 않습니다"
}
func (e FieldErrors) Unwrap() error { return contract.ErrValidation }

// DemoSeeder 는 데모 업무 샘플 세트를 넣는다(구현은 internal/demo.Seed). 데모 서비스들이 각자 트랜잭션을 열므로(SQLite 는 연결이 하나라
// 설치 완료 트랜잭션 안에서 부르면 교착) **설치 완료가 커밋된 뒤** 호출한다. nil 이면 데모 표시(demo_mode)만 켠다.
// 실패해도 설치는 완료로 남는다: 결과의 DemoErr 로 화면·로그에 알리고, 부분 실패는 데모 표시(demo_seed)가 남아 다시 넣을 수 없다 — 새 DB 로 다시 설치한다.
type DemoSeeder func(ctx context.Context, adminID int64) error

// Result 는 Complete 의 결과다. DemoErr 가 있으면 설치는 끝났지만 데모 데이터는 넣지 못한 것이다(완료는 되돌리지 않는다).
type Result struct {
	AdminID int64
	DemoErr error
}

// Service 는 설치 상태·설치 코드·완료 트랜잭션이다.
type Service struct {
	db    *sql.DB
	txs   contract.TxManager
	audit audit.Recorder
	auth  *auth.Service
	guard *Guard
	now   func() time.Time
	demo  DemoSeeder
}

// Options 는 서비스 조립 입력이다.
type Options struct {
	DB    *sql.DB
	Txs   contract.TxManager
	Audit audit.Recorder
	Auth  *auth.Service
	Code  string // PL_SETUP_CODE(선택)
	Now   func() time.Time
	Demo  DemoSeeder
}

// New 는 서비스를 만든다. pending 이면 설치 코드를 정해 서버 로그에 한 번 출력한다(done 이면 코드를 만들지 않는다).
func New(ctx context.Context, o Options) (*Service, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{db: o.DB, txs: o.Txs, audit: o.Audit, auth: o.Auth, now: o.Now, demo: o.Demo}
	st, err := Status(ctx, o.DB)
	if err != nil {
		return nil, err
	}
	if st != StatusPending {
		return s, nil
	}
	g, gen, err := NewGuard(o.Code, o.Now)
	if err != nil {
		return nil, err
	}
	s.guard = g
	if gen != "" {
		// 설치 코드는 서버 로그(표준 출력)에만 한 번 나온다. 화면·API·감사·DB 평문에는 없다.
		log.Printf("setup: 초기 설정이 필요합니다. 브라우저에서 /setup 을 열고 설치 코드를 입력하세요: %s", gen)
	} else {
		log.Printf("setup: 초기 설정이 필요합니다. 브라우저에서 /setup 을 열고 PL_SETUP_CODE 로 지정한 설치 코드를 입력하세요")
	}
	return s, nil
}

// Pending 은 마법사가 열려 있는지다(DB 를 매번 읽는다: 다른 프로세스·복구 뒤에도 정확하다).
func (s *Service) Pending(ctx context.Context) bool {
	st, err := Status(ctx, s.db)
	return err == nil && st == StatusPending && s.guard != nil
}

// CheckCode 는 설치 코드를 확인한다. 이 시도로 잠금이 시작되면 감사 한 행(setup.code_lockout)을 남긴다.
func (s *Service) CheckCode(ctx context.Context, code string) error {
	if s.guard == nil {
		return ErrBadCode
	}
	locked, err := s.guard.Check(code)
	if locked {
		s.recordLockout(ctx)
	}
	return err
}

func (s *Service) recordLockout(ctx context.Context) {
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: contract.Principal{Role: "system", Channel: contract.ChannelWeb}, Action: "setup.code_lockout", TargetType: audit.TargetSetup, TargetID: "1",
			Result: audit.ResultDenied, Diff: []audit.DiffItem{{Key: "locked_minutes", New: fmt.Sprint(int(lockoutFor / time.Minute))}},
		})
	})
	if err != nil {
		log.Printf("setup: 설치 코드 잠금 감사 기록 실패: %v", err)
	}
	log.Printf("setup: 설치 코드가 연속 %d회 틀려 %d분 동안 모든 시도를 거부합니다", maxFails, int(lockoutFor/time.Minute))
}

// TOTPKeyConfigured 는 2단계 인증 비밀키가 설정됐는지다(마법사 3단계 안내).
func (s *Service) TOTPKeyConfigured() bool { return s.auth != nil && s.auth.TOTPAvailable() }

// Validate 는 상태를 바꾸지 않고 입력 전체를 서버 규칙으로 검사한다. 필드별 문구를 돌려준다(없으면 nil).
func (s *Service) Validate(in *Input) FieldErrors {
	errs := FieldErrors{}
	if err := service.ValidateCompanyInput(&in.Company); err != nil {
		errs["company"] = strings.TrimPrefix(strings.TrimPrefix(err.Error(), contract.ErrValidation.Error()+": "), "입력값이 올바르지 않습니다: ")
	}
	if in.FiscalStartMonth == 0 {
		in.FiscalStartMonth = 1
	}
	if in.FiscalStartMonth < 1 || in.FiscalStartMonth > 12 {
		errs["fiscalStartMonth"] = "회계연도 시작월은 1월부터 12월 사이여야 합니다."
	}
	fa := auth.FirstAdminInput{Name: in.AdminName, Email: in.AdminEmail, Password: in.AdminPassword}
	for f, m := range auth.ValidateFirstAdminAll(&fa) {
		errs[f] = m
	}
	in.AdminName, in.AdminEmail = fa.Name, fa.Email
	in.Department = strings.TrimSpace(in.Department)
	if utf8.RuneCountInString(in.Department) > 50 {
		errs["department"] = "부서 이름은 50자 이내로 입력하세요."
	}
	if in.Demo && strings.TrimSpace(in.DemoConfirmCompany) != in.Company.Name {
		errs["demoConfirm"] = "데모 데이터를 넣으려면 확인 칸에 회사명을 똑같이 입력하세요."
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Complete 는 설치를 한 트랜잭션으로 끝낸다(명세 4.5): 입력 재검증 → 회사 정보·첫 관리자·(데모) → setup_state pending→done(영향 행 정확히 1) → 감사 setup.complete.
// 어느 단계가 실패해도 아무것도 남지 않는다. 다른 요청이 먼저 끝냈으면 ErrAlreadyDone 이다. 세션은 만들지 않는다.
func (s *Service) Complete(ctx context.Context, in Input) (Result, error) {
	if errs := s.Validate(&in); errs != nil {
		return Result{}, errs
	}
	hash, err := s.auth.HashPassword(ctx, in.AdminPassword) // 트랜잭션 밖(연결을 오래 잡지 않는다)
	if err != nil {
		return Result{}, err
	}
	var createdAdmin int64
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		// PG 는 연결이 여럿이라 완료 요청들이 동시에 일할 수 있다. 먼저 잠금을 잡아 하나씩 처리하고, 뒤따른 요청은 끝난 상태(done)를 보고 물러난다.
		if err := dbutil.AcquireTxLock(ctx, tx, dbutil.DialectOf(s.db), dbutil.LockSetupComplete); err != nil {
			return err
		}
		st, err := Status(ctx, tx)
		if err != nil {
			return err
		}
		if st != StatusPending {
			return ErrAlreadyDone
		}
		var users int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
			return err
		}
		if users > 0 { // 사용자가 있으면 마법사는 열리지 않는 것이 규칙이다(다음 기동 때 Reconcile 이 done 으로 고친다)
			return ErrAlreadyDone
		}
		dept := in.Department
		if dept == "" {
			dept = "본사"
		}
		if in.Demo {
			// 데모 조직(org.SeedDemoOrg)은 루트 회사 조직이 첫 부서 번호와 같은 이름이어야 만들어진다(기존 부서와 이름이 다르면 거부). 첫 관리자의 부서를 그 이름으로 둔다.
			dept = DemoDepartment
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO departments (name) VALUES (?) ON CONFLICT DO NOTHING`, dept); err != nil {
			return err
		}
		var deptID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM departments WHERE name = ?`, dept).Scan(&deptID); err != nil {
			return err
		}
		adminID, err := s.auth.CreateFirstAdminTx(ctx, tx, auth.FirstAdminInput{Name: in.AdminName, Email: in.AdminEmail}, hash, deptID)
		if err != nil {
			return err
		}
		now := s.now()
		repo := repository.SQLCompany{}
		// 회사 정보 첫 저장(SQLCompany.Save 의 version 0 경로와 같은 행·같은 규칙). repository 의 구문이 방언마다 달라질 수 있어
		// 설치 트랜잭션은 두 방언 공통 구문(ON CONFLICT DO NOTHING)을 직접 쓴다. 이후 수정은 회사 설정 화면(CompanyService.Update)이 한다.
		res, err := tx.ExecContext(ctx, `INSERT INTO company_settings (id, name, business_no, representative, address, phone, version, updated_by, updated_at)
			VALUES (1, ?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT DO NOTHING`, in.Company.Name, in.Company.BusinessNo, in.Company.Representative, in.Company.Address, in.Company.Phone, adminID, now.UTC().Format(time.RFC3339))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrAlreadyDone // 설치 전에 회사 정보 행이 이미 있으면 정상 상태가 아니다(동시 완료 방어)
		}
		if in.FiscalStartMonth != 1 {
			_, ver, _, err := repo.FiscalStart(ctx, tx)
			if err != nil {
				return err
			}
			if ok, err := repo.SetFiscalStart(ctx, tx, in.FiscalStartMonth, ver, adminID, now); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("회계연도 시작월을 저장하지 못했습니다")
			}
		}
		demo := "0"
		if in.Demo {
			demo = "1"
			if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES ('setup.demo_mode', '1', ?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
				adminID, now.UTC().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		createdAdmin = adminID
		// 마지막에 한 번: 영향 행이 정확히 1 일 때만 커밋한다(동시에 끝낸 다른 요청이 있으면 0 → 전체 롤백).
		res, err = tx.ExecContext(ctx, `UPDATE setup_state SET status = 'done', completed_at = ?, completed_by = ? WHERE id = 1 AND status = 'pending'`,
			now.UTC().Format(time.RFC3339Nano), adminID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrAlreadyDone
		}
		// 이메일·비밀번호는 감사에 남기지 않는다.
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor:  contract.Principal{UserID: adminID, Role: contract.RoleAdmin, Channel: contract.ChannelWeb},
			Action: "setup.complete", TargetType: audit.TargetSetup, TargetID: "1", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "demo_mode", New: demo}},
		})
	})
	if err != nil {
		if dbutil.IsUnique(err) { // 방어: 동시 완료가 같은 이메일·부서를 만든 경우
			return Result{}, ErrAlreadyDone
		}
		return Result{}, err
	}
	s.guard.Revoke() // 코드 폐기(재기동 뒤에도 done 은 DB 에서 읽는다)
	log.Printf("setup: 초기 설정이 완료되었습니다. 설치 코드는 폐기되었습니다")
	res := Result{AdminID: createdAdmin}
	// 데모 데이터는 완료가 커밋된 뒤 넣는다. 실패해도 설치는 완료로 둔다(되돌리지 않는다): 화면·로그에 알린다.
	if in.Demo && s.demo != nil {
		if derr := s.demo(ctx, createdAdmin); derr != nil {
			log.Printf("setup: 설치는 완료되었지만 데모 데이터를 넣지 못했습니다: %v", derr)
			res.DemoErr = derr
		}
	}
	return res, nil
}
