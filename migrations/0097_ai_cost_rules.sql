-- 0097 (B49 2단계, 4·7절): 배부 규칙 세트(버전 관리). 한 번 만든 버전은 풀·대상까지 고칠 수 없다(트리거).
--  ai_cost_rule_sets: 버전마다 한 줄(적용 시작 월). ai_cost_rule_pools: 공용 비용을 나눌 풀(고르는 조건과 배분 기준). ai_cost_rule_targets: 풀의 대상 부서(고정 비율은 weight_pct).
CREATE TABLE ai_cost_rule_sets (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    version          INTEGER NOT NULL UNIQUE CHECK (version >= 1),
    valid_from_month TEXT NOT NULL CHECK (valid_from_month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    note             TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    created_by       INTEGER NOT NULL REFERENCES users(id),
    created_at       TEXT NOT NULL
);

CREATE TABLE ai_cost_rule_pools (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_set_id   INTEGER NOT NULL REFERENCES ai_cost_rule_sets(id),
    seq           INTEGER NOT NULL CHECK (seq >= 1),
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
    selector_json TEXT NOT NULL CHECK (length(selector_json) <= 2000),
    driver        TEXT NOT NULL CHECK (driver IN ('headcount','usage','fixed')),
    scope         TEXT NOT NULL DEFAULT 'entity' CHECK (scope IN ('entity','group')),
    targets_all   INTEGER NOT NULL DEFAULT 0 CHECK (targets_all IN (0,1)),
    UNIQUE (rule_set_id, seq)
);

CREATE TABLE ai_cost_rule_targets (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    pool_id       INTEGER NOT NULL REFERENCES ai_cost_rule_pools(id),
    department_id INTEGER NOT NULL,
    weight_pct    INTEGER NOT NULL DEFAULT 0 CHECK (weight_pct BETWEEN 0 AND 100),
    UNIQUE (pool_id, department_id)
);

CREATE TRIGGER trg_ai_cost_rule_sets_no_update BEFORE UPDATE ON ai_cost_rule_sets BEGIN SELECT RAISE(ABORT, '배부 규칙 버전은 고칠 수 없습니다(새 버전으로 저장하세요)'); END;
CREATE TRIGGER trg_ai_cost_rule_sets_no_delete BEFORE DELETE ON ai_cost_rule_sets BEGIN SELECT RAISE(ABORT, '배부 규칙 버전은 지울 수 없습니다'); END;
CREATE TRIGGER trg_ai_cost_rule_pools_no_update BEFORE UPDATE ON ai_cost_rule_pools BEGIN SELECT RAISE(ABORT, '배부 규칙 버전은 고칠 수 없습니다(새 버전으로 저장하세요)'); END;
CREATE TRIGGER trg_ai_cost_rule_pools_no_delete BEFORE DELETE ON ai_cost_rule_pools BEGIN SELECT RAISE(ABORT, '배부 규칙 버전은 지울 수 없습니다'); END;
CREATE TRIGGER trg_ai_cost_rule_targets_no_update BEFORE UPDATE ON ai_cost_rule_targets BEGIN SELECT RAISE(ABORT, '배부 규칙 버전은 고칠 수 없습니다(새 버전으로 저장하세요)'); END;
CREATE TRIGGER trg_ai_cost_rule_targets_no_delete BEFORE DELETE ON ai_cost_rule_targets BEGIN SELECT RAISE(ABORT, '배부 규칙 버전은 지울 수 없습니다'); END;
