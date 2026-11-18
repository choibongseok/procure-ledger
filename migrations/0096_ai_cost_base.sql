-- 0096 (B49 1단계, 4절): 부서별 AI 비용 배부의 바탕.
--  1) llm_usage.entity_id: 호출 시점의 현재 법인(게이트웨이가 쓴다, 0 = 법인 미상). 기존 행은 부서(=조직 단위 번호)의 법인으로 채운다(조직 단위가 없으면 기본 법인 1).
--  2) ai_cost_fx: 월 하나의 고정 환율(USD→KRW x1000, 설치 전체). 확정된 실행이 있는 달의 잠금은 실행 표가 생기는 0098 이 건다.
--  3) ai_cost_adjustments: 월·공급자별 실제 청구액(원) 입력. 추가 전용이고 같은 (월, 공급자)는 최신 행이 쓰인다.
ALTER TABLE llm_usage ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 0;
UPDATE llm_usage SET entity_id = COALESCE((SELECT s.entity_id FROM org_unit_spans s WHERE s.unit_id = llm_usage.department_id ORDER BY s.valid_from DESC LIMIT 1), 1);
CREATE INDEX idx_llm_usage_entity_at ON llm_usage(entity_id, at);

CREATE TABLE ai_cost_fx (
    month           TEXT PRIMARY KEY CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    usd_krw_x1000   INTEGER NOT NULL CHECK (usd_krw_x1000 BETWEEN 1000 AND 100000000),
    set_by          INTEGER NOT NULL REFERENCES users(id),
    set_at          TEXT NOT NULL
);

CREATE TABLE ai_cost_adjustments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    month         TEXT NOT NULL CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    provider_code TEXT NOT NULL CHECK (length(provider_code) BETWEEN 1 AND 40),
    actual_krw    INTEGER NOT NULL CHECK (actual_krw >= 0),
    note          TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    entered_by    INTEGER NOT NULL REFERENCES users(id),
    entered_at    TEXT NOT NULL
);
CREATE INDEX idx_ai_cost_adjustments_month ON ai_cost_adjustments(month, provider_code, id);
CREATE TRIGGER trg_ai_cost_adjustments_no_update BEFORE UPDATE ON ai_cost_adjustments BEGIN SELECT RAISE(ABORT, '청구액 입력은 고칠 수 없습니다(새로 입력하세요)'); END;
CREATE TRIGGER trg_ai_cost_adjustments_no_delete BEFORE DELETE ON ai_cost_adjustments BEGIN SELECT RAISE(ABORT, '청구액 입력은 지울 수 없습니다'); END;
