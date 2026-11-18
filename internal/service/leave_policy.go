package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 휴가 종류·연차 규칙(B50 5절). 설정은 attendance:policy(관리자)만 바꾸고 모든 변경은 같은 트랜잭션의 감사에 남는다. 규칙은 효력일 이력(추가 전용)이다.

// ModuleGate 는 모듈 켜기 상태를 묻는 최소 인터페이스다(internal/module.Service 가 구현한다).
type ModuleGate interface {
	Enabled(ctx context.Context, key string) bool
	// EnabledTx 는 호출자의 트랜잭션 핸들로 읽는다. 트랜잭션 안에서는 Enabled 대신 이것만 쓴다(SQLite 는 풀 연결로 읽으면 영구 대기한다).
	EnabledTx(ctx context.Context, q contract.Tx, key string) bool
}

// LeaveService 는 domain.LeaveService 의 구현이다.
type LeaveService struct {
	Base
	Repo repository.SQLLeave
	Docs *ApprovalDocService
	// OverdueReturns 는 예정 복직일이 지났는데 복직 발령이 없는 사람을 인사 담당·직속 조직장에게 알리는 일일 작업이다(B50 8.1, 조직 서비스가 구현하고 서버 조립이 꽂는다).
	OverdueReturns func(ctx context.Context, asOf time.Time) (int, error)
}

var _ domain.LeaveService = (*LeaveService)(nil)

// NewLeaveService 는 서비스를 만든다. docs 는 휴가 신청서(LEAVE) 문서를 만드는 데 쓴다.
func NewLeaveService(b Base, docs *ApprovalDocService) *LeaveService {
	return &LeaveService{Base: b, Docs: docs}
}

// requireModule 은 모듈이 꺼져 있으면 없는 것과 같은 ErrNotFound 를 돌려준다.
func (s *LeaveService) requireModule(ctx context.Context) error {
	if !s.moduleOn(ctx) {
		return contract.ErrNotFound
	}
	return nil
}

var leaveCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,29}$`)

// Types 는 현재 법인의 휴가 종류다(시스템 종류는 처음 읽을 때 만든다).
func (s *LeaveService) Types(ctx context.Context) ([]domain.LeaveType, error) {
	if _, err := contract.Authorize(ctx, contract.PermAttendanceSelf); err != nil {
		return nil, err
	}
	if err := s.requireModule(ctx); err != nil {
		return nil, err
	}
	if err := s.Txs.WithTx(ctx, func(tx contract.Tx) error { return s.Repo.EnsureTypes(ctx, tx, contract.WriteEntityID(ctx), s.now()) }); err != nil {
		return nil, err
	}
	all, err := s.Repo.Types(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	var out []domain.LeaveType
	for _, t := range all {
		if t.Active {
			out = append(out, t)
		}
	}
	return out, nil
}

// AdminTypes 는 설정 화면용 휴가 종류(사용 중지 포함)다. 규칙·종류 설정은 모듈을 켜기 전에 하므로 모듈이 꺼져 있어도 읽을 수 있다.
func (s *LeaveService) AdminTypes(ctx context.Context) ([]domain.LeaveType, error) {
	if _, err := contract.Authorize(ctx, contract.PermAttendancePolicy); err != nil {
		return nil, err
	}
	if err := s.Txs.WithTx(ctx, func(tx contract.Tx) error { return s.Repo.EnsureTypes(ctx, tx, contract.WriteEntityID(ctx), s.now()) }); err != nil {
		return nil, err
	}
	return s.Repo.Types(ctx, s.DB)
}

// SaveType 은 휴가 종류를 추가(id 0)하거나 고친다. 시스템 종류는 이름·증빙·유급·사용 여부만 바뀐다. 종류는 지우지 않는다.
func (s *LeaveService) SaveType(ctx context.Context, id int64, in domain.LeaveTypeInput) (domain.LeaveType, error) {
	p, err := s.authorize(ctx, contract.PermAttendancePolicy, "leave.type_save", audit.TargetLeavePolicy)
	if err != nil {
		return domain.LeaveType{}, err
	}
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 30 {
		return domain.LeaveType{}, validation("휴가 종류 이름은 1~30자로 입력하세요.")
	}
	if in.MaxDays < 0 || in.MaxDays > 365 {
		return domain.LeaveType{}, validation("일수 상한은 0~365 사이로 입력하세요(0 은 상한 없음).")
	}
	var out domain.LeaveType
	err = s.write(ctx, p, "leave.type_save", audit.TargetLeavePolicy, id, func(tx contract.Tx) error {
		if err := s.Repo.EnsureTypes(ctx, tx, contract.WriteEntityID(ctx), s.now()); err != nil {
			return err
		}
		if id == 0 {
			if !leaveCodeRe.MatchString(in.Code) {
				return validation("종류 코드는 영문 소문자로 시작하는 2~30자(영문·숫자·밑줄)입니다.")
			}
			if in.Unit != "day" && in.Unit != "hour" {
				return validation("단위는 일 또는 시간 중에서 고르세요.")
			}
			if in.Balance != "" && in.Balance != domain.LeaveBalanceAnnual && in.Balance != domain.LeaveBalanceCompensatory {
				return validation("차감할 잔여는 연차·대체휴가 중에서 고르세요.")
			}
			// 회사가 만든 종류가 법정 연차를 몰래 줄이지 못하게, 연차 잔여를 차감하는 새 종류는 만들 수 없다(연차·시간 단위 연차는 시스템 종류뿐이다).
			if in.Balance == domain.LeaveBalanceAnnual {
				return validation("연차를 차감하는 종류는 새로 만들 수 없습니다. 연차·시간 단위 연차를 쓰세요.")
			}
			if _, err := s.Repo.TypeByCode(ctx, tx, in.Code); err == nil {
				return validation("같은 코드의 휴가 종류가 이미 있습니다.")
			} else if !errors.Is(err, contract.ErrNotFound) {
				return err
			}
			in.Active = true
			newID, err := s.Repo.InsertType(ctx, tx, in, s.now())
			if err != nil {
				return err
			}
			id = newID
		} else {
			cur, err := s.Repo.TypeByID(ctx, tx, id)
			if err != nil {
				return err
			}
			if cur.Code == "annual" && !in.Active {
				return validation("연차는 사용 중지할 수 없습니다.")
			}
			if !cur.System && in.Balance != cur.Balance && in.Balance == domain.LeaveBalanceAnnual {
				return validation("연차를 차감하는 종류로 바꿀 수 없습니다.")
			}
			if err := s.Repo.UpdateType(ctx, tx, id, in, cur.System); err != nil {
				return err
			}
		}
		t, err := s.Repo.TypeByID(ctx, tx, id)
		if err != nil {
			return err
		}
		out = t
		return s.record(ctx, tx, p, "leave.type_save", audit.TargetLeavePolicy, id, audit.DiffItem{Key: "leave_code", New: t.Code}, audit.DiffItem{Key: "enabled", New: map[bool]string{true: "켜짐", false: "꺼짐"}[t.Active]})
	})
	return out, err
}

// Policies 는 현재 법인의 연차 규칙 이력이다(최근 효력일 먼저).
func (s *LeaveService) Policies(ctx context.Context) ([]domain.LeavePolicy, error) {
	if _, err := contract.Authorize(ctx, contract.PermAttendancePolicy); err != nil {
		return nil, err
	}
	return s.Repo.Policies(ctx, s.DB)
}

// validatePolicy 는 규칙 입력을 검사한다. 법정 하한보다 불리한 값은 거부한다(소멸 시효 12개월 미만 등, 5.1). 법정보다 유리한 값은 허용한다.
func validatePolicy(in *domain.LeavePolicyInput) error {
	if in.Basis != "hire" && in.Basis != "fiscal" {
		return validation("연차 발생 기준은 입사일 기준 또는 회계연도 기준 중에서 고르세요.")
	}
	if in.WorkdayMinutes < 360 || in.WorkdayMinutes > 600 {
		return validation("하루 분량은 6시간(360분)~10시간(600분) 사이로 입력하세요.")
	}
	if in.HourUnitMinutes != 30 && in.HourUnitMinutes != 60 {
		return validation("시간 단위 휴가의 최소 단위는 30분 또는 60분입니다.")
	}
	if in.FirstYearMode != "monthly" && in.FirstYearMode != "upfront" {
		return validation("입사 첫해 발생 방식을 고르세요.")
	}
	if in.CarryMaxMinutes < -1 {
		return validation("이월 한도가 올바르지 않습니다.")
	}
	if in.CarryValidMonths < 1 || in.CarryValidMonths > 24 {
		return validation("이월분 사용 기한은 1~24개월입니다.")
	}
	if in.ExpireMonths < legalMinimum.MinExpireMonths {
		return validation("%s", fmt.Sprintf("연차는 법정 기준(발생 후 %d개월 안에 사용할 수 있어야 함)보다 짧게 소멸시킬 수 없습니다.", legalMinimum.MinExpireMonths))
	}
	if in.ExpireMonths > 60 {
		return validation("소멸 기한은 60개월까지 정할 수 있습니다.")
	}
	return nil
}

// SavePolicy 는 연차 규칙을 새 이력 행으로 저장한다. 효력일은 오늘 이후(오늘 포함)여야 하고(처음 저장하는 규칙은 과거 날짜도 되며 그 날짜부터 발생을 따라잡는다), 같은 효력일의 규칙이 있으면 거부한다.
func (s *LeaveService) SavePolicy(ctx context.Context, in domain.LeavePolicyInput) (domain.LeavePolicy, error) {
	p, err := s.authorize(ctx, contract.PermAttendancePolicy, "leave.policy_set", audit.TargetLeavePolicy)
	if err != nil {
		return domain.LeavePolicy{}, err
	}
	if err := validatePolicy(&in); err != nil {
		return domain.LeavePolicy{}, err
	}
	if _, err := lvParseDay(in.EffectiveFrom); err != nil {
		return domain.LeavePolicy{}, validation("효력일을 YYYY-MM-DD 로 입력하세요.")
	}
	var out domain.LeavePolicy
	err = s.write(ctx, p, "leave.policy_set", audit.TargetLeavePolicy, 0, func(tx contract.Tx) error {
		first, err := s.Repo.FirstPolicyDate(ctx, tx)
		if err != nil {
			return err
		}
		today := string(contract.DayOf(s.now()))
		if first != "" && in.EffectiveFrom < today {
			return validation("효력일은 오늘 이후여야 합니다. 이미 발생한 연차는 새 규칙으로 바뀌지 않습니다.")
		}
		cur, okCur, err := s.Repo.PolicyOn(ctx, tx, today)
		if err != nil {
			return err
		}
		id, err := s.Repo.InsertPolicy(ctx, tx, in, p.UserID, s.now())
		if err != nil {
			if isUniqueViolation(err) {
				return validation("같은 효력일의 규칙이 이미 있습니다. 다른 효력일로 저장하세요.")
			}
			return err
		}
		pol, err := s.Repo.PolicyByID(ctx, tx, id)
		if err != nil {
			return err
		}
		out = pol
		diff := []audit.DiffItem{{Key: "effective_from", New: pol.EffectiveFrom}, {Key: "basis", New: map[string]string{"hire": "입사일 기준", "fiscal": "회계연도 기준"}[pol.Basis]}}
		if okCur {
			diff[1].Old = map[string]string{"hire": "입사일 기준", "fiscal": "회계연도 기준"}[cur.Basis]
		}
		diff = append(diff, audit.DiffItem{Key: "workday_minutes", Old: map[bool]string{true: fmt.Sprint(cur.WorkdayMinutes), false: ""}[okCur], New: fmt.Sprint(pol.WorkdayMinutes)})
		return s.record(ctx, tx, p, "leave.policy_set", audit.TargetLeavePolicy, id, diff...)
	})
	return out, err
}

// CancelPolicy 는 아직 효력이 오지 않은 규칙을 취소한다(이미 효력이 있는 규칙은 취소할 수 없다).
func (s *LeaveService) CancelPolicy(ctx context.Context, id int64) error {
	p, err := s.authorize(ctx, contract.PermAttendancePolicy, "leave.policy_cancel", audit.TargetLeavePolicy)
	if err != nil {
		return err
	}
	return s.write(ctx, p, "leave.policy_cancel", audit.TargetLeavePolicy, id, func(tx contract.Tx) error {
		cur, err := s.Repo.PolicyByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Canceled {
			return fmt.Errorf("%w: 이미 취소된 규칙입니다", contract.ErrInvalidState)
		}
		if cur.EffectiveFrom <= string(contract.DayOf(s.now())) {
			return fmt.Errorf("%w: 이미 효력이 시작된 규칙은 취소할 수 없습니다. 더 늦은 효력일로 새 규칙을 저장하세요", contract.ErrInvalidState)
		}
		if err := s.Repo.CancelPolicy(ctx, tx, id, p.UserID, s.now()); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "leave.policy_cancel", audit.TargetLeavePolicy, id, audit.DiffItem{Key: "effective_from", Old: cur.EffectiveFrom})
	})
}
