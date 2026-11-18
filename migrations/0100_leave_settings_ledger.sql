-- 0100: 근태·휴가(B50 12.1,). 휴가 종류·연차 규칙(효력일 이력)·휴가 원장. 모두 법인 소유 표다(entity_id, 기본 법인 1).
--  - leave_types: 휴가 종류. balance 는 이 종류가 차감하는 잔여 묶음('annual' 연차, 'compensatory' 대체휴가, '' 잔여 없음). 시스템 종류(system=1)는 코드·단위·잔여 묶음을 바꿀 수 없다(서비스가 막는다).
--    종류는 지우지 않고 사용 중지(active=0)만 한다. 법인마다 시스템 종류는 처음 읽을 때 서비스가 만든다(멱등).
--  - leave_policies: 연차 발생 규칙. vat_rates 와 같은 효력일 이력(추가 전용): 수정은 없고 아직 효력이 오지 않은 행만 취소(canceled)한다. 같은 효력일은 취소되지 않은 행끼리 하나만 있다.
--    carry_max_minutes -1 은 이월 한도 없음, 0 은 이월 없음.
--  - leave_ledger: 휴가 잔여 원장. 추가 전용(수정·삭제 트리거 거부). 잔여 = minutes 의 합(분 단위, 하루 = 규칙의 workday_minutes). 직접 고치는 경로는 없다.
--    kind: grant(발생)·carry(이월 발생)·expire(소멸)·reserve(예약)·release(복원)·use(사용)·adjust(인사 조정)·opening(초기 이관).
--    expires_on 이 있는 grant·carry 만 소멸 대상이다(adjust·opening 은 소멸이 없으면 빈 값). 발생·이월·소멸 줄은 (user, balance, kind, period_key)가 유일해 일일 작업이 몇 번 돌아도 한 번만 쓰인다.
CREATE TABLE leave_types (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id      INTEGER NOT NULL DEFAULT 1,
    code           TEXT NOT NULL CHECK (code GLOB '[a-z]*' AND length(code) BETWEEN 2 AND 30),
    name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 30),
    unit           TEXT NOT NULL CHECK (unit IN ('day','hour')),
    balance        TEXT NOT NULL DEFAULT '' CHECK (balance IN ('','annual','compensatory')),
    paid           INTEGER NOT NULL DEFAULT 1 CHECK (paid IN (0,1)),
    proof_required INTEGER NOT NULL DEFAULT 0 CHECK (proof_required IN (0,1)),
    max_days       INTEGER NOT NULL DEFAULT 0 CHECK (max_days >= 0),
    system         INTEGER NOT NULL DEFAULT 0 CHECK (system IN (0,1)),
    active         INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at     TEXT NOT NULL,
    UNIQUE (entity_id, code)
);
CREATE TRIGGER trg_leave_types_no_delete BEFORE DELETE ON leave_types BEGIN SELECT RAISE(ABORT, '휴가 종류는 지울 수 없습니다(사용 중지만 됩니다)'); END;

CREATE TABLE leave_policies (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id         INTEGER NOT NULL DEFAULT 1,
    effective_from    TEXT NOT NULL CHECK (effective_from GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    basis             TEXT NOT NULL CHECK (basis IN ('hire','fiscal')),
    workday_minutes   INTEGER NOT NULL CHECK (workday_minutes BETWEEN 360 AND 600),
    hour_unit_minutes INTEGER NOT NULL CHECK (hour_unit_minutes IN (30,60)),
    attendance_rule   INTEGER NOT NULL DEFAULT 1 CHECK (attendance_rule IN (0,1)),
    carry_max_minutes INTEGER NOT NULL DEFAULT 0 CHECK (carry_max_minutes >= -1),
    carry_valid_months INTEGER NOT NULL DEFAULT 12 CHECK (carry_valid_months BETWEEN 1 AND 24),
    expire_months     INTEGER NOT NULL DEFAULT 12 CHECK (expire_months BETWEEN 12 AND 60),
    first_year_mode   TEXT NOT NULL DEFAULT 'monthly' CHECK (first_year_mode IN ('monthly','upfront')),
    created_by        INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT NOT NULL,
    canceled          INTEGER NOT NULL DEFAULT 0 CHECK (canceled IN (0,1)),
    canceled_by       INTEGER NOT NULL DEFAULT 0,
    canceled_at       TEXT
);
CREATE UNIQUE INDEX idx_leave_policies_effective ON leave_policies(entity_id, effective_from) WHERE canceled = 0;
CREATE TRIGGER trg_leave_policies_no_delete BEFORE DELETE ON leave_policies BEGIN SELECT RAISE(ABORT, '휴가 규칙 이력은 지울 수 없습니다'); END;
CREATE TRIGGER trg_leave_policies_fixed BEFORE UPDATE ON leave_policies
WHEN NEW.entity_id IS NOT OLD.entity_id OR NEW.effective_from IS NOT OLD.effective_from OR NEW.basis IS NOT OLD.basis OR NEW.workday_minutes IS NOT OLD.workday_minutes
  OR NEW.hour_unit_minutes IS NOT OLD.hour_unit_minutes OR NEW.attendance_rule IS NOT OLD.attendance_rule OR NEW.carry_max_minutes IS NOT OLD.carry_max_minutes
  OR NEW.carry_valid_months IS NOT OLD.carry_valid_months OR NEW.expire_months IS NOT OLD.expire_months OR NEW.first_year_mode IS NOT OLD.first_year_mode
  OR NEW.created_by IS NOT OLD.created_by OR NEW.created_at IS NOT OLD.created_at OR NEW.canceled < OLD.canceled
BEGIN SELECT RAISE(ABORT, '휴가 규칙 이력은 고칠 수 없습니다(취소만 됩니다)'); END;

CREATE TABLE leave_ledger (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id  INTEGER NOT NULL DEFAULT 1,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    balance    TEXT NOT NULL CHECK (balance IN ('annual','compensatory')),
    kind       TEXT NOT NULL CHECK (kind IN ('grant','carry','expire','reserve','release','use','adjust','opening')),
    minutes    INTEGER NOT NULL CHECK (minutes <> 0),
    period_key TEXT NOT NULL DEFAULT '',
    expires_on TEXT NOT NULL DEFAULT '',
    ref_type   TEXT NOT NULL DEFAULT '',
    ref_id     INTEGER NOT NULL DEFAULT 0,
    reason     TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    created_by INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_leave_ledger_user ON leave_ledger(user_id, balance, id);
CREATE INDEX idx_leave_ledger_ref ON leave_ledger(ref_type, ref_id);
CREATE UNIQUE INDEX idx_leave_ledger_period ON leave_ledger(user_id, balance, kind, period_key) WHERE kind IN ('grant','carry','expire') AND period_key <> '';
CREATE TRIGGER trg_leave_ledger_no_update BEFORE UPDATE ON leave_ledger BEGIN SELECT RAISE(ABORT, '휴가 원장은 고칠 수 없습니다(추가 전용)'); END;
CREATE TRIGGER trg_leave_ledger_no_delete BEFORE DELETE ON leave_ledger BEGIN SELECT RAISE(ABORT, '휴가 원장은 지울 수 없습니다(추가 전용)'); END;
