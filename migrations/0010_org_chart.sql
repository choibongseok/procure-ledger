-- 0010: 조직도·인사 발령(B20).
--
-- 정본은 추가 전용 이벤트 로그(org_events)이고, 구간 테이블(org_*_spans)은 로그를 처음부터 다시 계산한 결과다(internal/org).
-- 구간 테이블과, 조직에 등록된 사용자의 users.department_id·users.position 은 조직 서비스가 쓰기 보호 행(org_write_guard)을
-- 같은 트랜잭션에 넣어 둔 동안에만 바꿀 수 있다. DB 파일에 직접 접근하는 사람은 트리거를 풀 수 있다(docs/architecture.md 한계 1).
-- 날짜는 한국 표준시(KST) 기준 'YYYY-MM-DD' 문자열이고 구간은 [valid_from, valid_to) 이다(valid_to NULL 이면 열려 있음).

-- 조직 단위 식별자. 번호와 코드는 한 번 정하면 바뀌지 않는다(이름·부모 같은 변하는 값은 구간 테이블에 있다).
CREATE TABLE org_units (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    code       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE TRIGGER trg_org_units_no_update BEFORE UPDATE ON org_units
BEGIN SELECT RAISE(ABORT, 'org_units 는 추가 전용입니다'); END;
CREATE TRIGGER trg_org_units_no_delete BEFORE DELETE ON org_units
BEGIN SELECT RAISE(ABORT, 'org_units 는 추가 전용입니다'); END;

-- 직위 표(사원~대표이사). 삭제 없음, 사용 여부만 바꾼다.
CREATE TABLE org_ranks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    sort_order INTEGER NOT NULL,
    active     INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1))
);
INSERT INTO org_ranks (name, sort_order) VALUES
    ('사원', 10), ('대리', 20), ('과장', 30), ('차장', 40), ('부장', 50), ('이사', 60), ('상무', 70), ('전무', 80), ('대표이사', 90);

-- 사람의 변하지 않는 인사 정보(사번)와 연락처. 사람 번호는 users.id 다.
CREATE TABLE org_people (
    user_id     INTEGER PRIMARY KEY REFERENCES users(id),
    employee_no TEXT NOT NULL UNIQUE,
    phone       TEXT NOT NULL DEFAULT '',
    extension   TEXT NOT NULL DEFAULT ''
);

-- 이벤트 로그: 조직 변경과 인사 발령 모두. 추가 전용이다.
-- subject_type: unit(조직 변경, 직무대행 포함) | person(인사 발령). cancels_id 가 있으면 그 이벤트를 취소하는 이벤트다.
-- corrects_id 가 있으면 그 이벤트를 정정하는 새 이벤트다(같은 트랜잭션에서 취소와 함께 등록).
CREATE TABLE org_events (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    subject_type   TEXT NOT NULL CHECK (subject_type IN ('unit','person')),
    subject_id     INTEGER NOT NULL,
    kind           TEXT NOT NULL,
    effective_date TEXT NOT NULL CHECK (effective_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
    payload        TEXT NOT NULL DEFAULT '{}',
    reason         TEXT NOT NULL DEFAULT '',
    cancels_id     INTEGER REFERENCES org_events(id),
    corrects_id    INTEGER REFERENCES org_events(id),
    created_by     INTEGER NOT NULL DEFAULT 0,        -- 0 은 시스템
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX idx_org_events_subject ON org_events(subject_type, subject_id, effective_date);
CREATE INDEX idx_org_events_date ON org_events(effective_date);
CREATE INDEX idx_org_events_cancels ON org_events(cancels_id);
CREATE TRIGGER trg_org_events_no_update BEFORE UPDATE ON org_events
BEGIN SELECT RAISE(ABORT, 'org_events 는 추가 전용입니다'); END;
CREATE TRIGGER trg_org_events_no_delete BEFORE DELETE ON org_events
BEGIN SELECT RAISE(ABORT, 'org_events 는 추가 전용입니다'); END;

-- 구간 테이블(계산 결과). 조직 서비스만 바꾼다.
CREATE TABLE org_unit_spans (
    unit_id      INTEGER NOT NULL REFERENCES org_units(id),
    valid_from   TEXT NOT NULL,
    valid_to     TEXT,
    code         TEXT NOT NULL,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('company','division','department','team')),
    parent_id    INTEGER NOT NULL DEFAULT 0,
    sort_order   INTEGER NOT NULL DEFAULT 0,
    closed       INTEGER NOT NULL DEFAULT 0 CHECK (closed IN (0,1)),
    head_user_id INTEGER NOT NULL DEFAULT 0,         -- 그 기간의 실효 조직장(조직장 또는 직무대행자, 없으면 0)
    head_acting  INTEGER NOT NULL DEFAULT 0 CHECK (head_acting IN (0,1)),
    PRIMARY KEY (unit_id, valid_from)
);
CREATE INDEX idx_org_unit_spans_range ON org_unit_spans(valid_from, valid_to);
CREATE INDEX idx_org_unit_spans_parent ON org_unit_spans(parent_id, valid_from);
CREATE INDEX idx_org_unit_spans_head ON org_unit_spans(head_user_id, valid_from);

CREATE TABLE org_person_spans (
    user_id         INTEGER NOT NULL REFERENCES users(id),
    valid_from      TEXT NOT NULL,
    valid_to        TEXT,
    left_flag       INTEGER NOT NULL DEFAULT 0 CHECK (left_flag IN (0,1)),   -- 1 이면 퇴사 상태
    rank_id         INTEGER NOT NULL REFERENCES org_ranks(id),
    primary_unit_id INTEGER NOT NULL REFERENCES org_units(id),
    PRIMARY KEY (user_id, valid_from)
);
CREATE INDEX idx_org_person_spans_range ON org_person_spans(valid_from, valid_to);
CREATE INDEX idx_org_person_spans_unit ON org_person_spans(primary_unit_id, valid_from);

CREATE TABLE org_concurrent_spans (
    user_id    INTEGER NOT NULL REFERENCES users(id),
    unit_id    INTEGER NOT NULL REFERENCES org_units(id),
    valid_from TEXT NOT NULL,
    valid_to   TEXT,
    PRIMARY KEY (user_id, unit_id, valid_from)
);
CREATE INDEX idx_org_concurrent_unit ON org_concurrent_spans(unit_id, valid_from);

CREATE TABLE org_acting_spans (
    unit_id    INTEGER NOT NULL REFERENCES org_units(id),
    user_id    INTEGER NOT NULL REFERENCES users(id),
    valid_from TEXT NOT NULL,
    valid_to   TEXT,
    PRIMARY KEY (unit_id, user_id, valid_from)
);
CREATE INDEX idx_org_acting_user ON org_acting_spans(user_id, valid_from);

-- 쓰기 보호 행. 조직 서비스가 트랜잭션 안에서 넣고 끝나기 전에 지운다(롤백되면 함께 사라진다).
CREATE TABLE org_write_guard (
    id INTEGER PRIMARY KEY CHECK (id = 1)
);

CREATE TRIGGER trg_org_unit_spans_guard_ins BEFORE INSERT ON org_unit_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;
CREATE TRIGGER trg_org_unit_spans_guard_upd BEFORE UPDATE ON org_unit_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;
CREATE TRIGGER trg_org_unit_spans_guard_del BEFORE DELETE ON org_unit_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;

CREATE TRIGGER trg_org_person_spans_guard_ins BEFORE INSERT ON org_person_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;
CREATE TRIGGER trg_org_person_spans_guard_upd BEFORE UPDATE ON org_person_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;
CREATE TRIGGER trg_org_person_spans_guard_del BEFORE DELETE ON org_person_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;

CREATE TRIGGER trg_org_concurrent_spans_guard_ins BEFORE INSERT ON org_concurrent_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;
CREATE TRIGGER trg_org_concurrent_spans_guard_upd BEFORE UPDATE ON org_concurrent_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;
CREATE TRIGGER trg_org_concurrent_spans_guard_del BEFORE DELETE ON org_concurrent_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;

CREATE TRIGGER trg_org_acting_spans_guard_ins BEFORE INSERT ON org_acting_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;
CREATE TRIGGER trg_org_acting_spans_guard_upd BEFORE UPDATE ON org_acting_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;
CREATE TRIGGER trg_org_acting_spans_guard_del BEFORE DELETE ON org_acting_spans
WHEN NOT EXISTS (SELECT 1 FROM org_write_guard)
BEGIN SELECT RAISE(ABORT, '조직 구간 테이블은 조직 서비스만 바꿀 수 있습니다'); END;

-- 호환 투영: 조직에 등록된 사용자(org_person_spans 에 행이 있는 사용자)의 부서·직책은 조직 서비스만 바꾼다.
-- 조직에 등록되지 않은 사용자(기존 단위 테스트의 시드 등)는 이전처럼 바꿀 수 있다.
CREATE TRIGGER trg_users_org_projection_guard BEFORE UPDATE OF department_id, position ON users
WHEN EXISTS (SELECT 1 FROM org_person_spans WHERE user_id = NEW.id)
 AND NOT EXISTS (SELECT 1 FROM org_write_guard)
 AND (NEW.department_id IS NOT OLD.department_id OR NEW.position IS NOT OLD.position)
BEGIN SELECT RAISE(ABORT, '소속·직책은 인사 발령으로만 바꿀 수 있습니다'); END;
