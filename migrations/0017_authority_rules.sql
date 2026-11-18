-- 0017: 전결 규정(B23 단계 3, 명세 3.13).
--  1) authority_rules: 양식(없으면 전체)·금액 구간별 최종 결재자 직책 층과 근거 규정(문서 slug·조항 번호). 관리자만 편집한다(서비스, authority:manage).
--  2) approval_lines.authority_json: 상신 때 적용한 전결 판정 결과(규정 번호·건너뛴 단계·경고) 스냅샷. 규정이 나중에 바뀌어도 진행 중 문서는 그대로다.
--  3) approval_steps.final_authority·skip_reason: 전결 최종 결재 단계 표시와 건너뜀 사유("전결").
--  4) 시드: 지출 결의서 1천만 원 이상 3천만 원 미만은 부서장(층 2)이 최종 결재자이고 재무 단계는 전결로 건너뛴다.
--     구매 품의서(form 1)에는 시드 규정을 두지 않아 기존 동작은 바뀌지 않는다.
CREATE TABLE authority_rules (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    form_id      INTEGER REFERENCES approval_forms(id),
    min_amount   INTEGER NOT NULL CHECK (min_amount >= 0),
    max_amount   INTEGER CHECK (max_amount IS NULL OR max_amount > min_amount),
    final_level  INTEGER NOT NULL CHECK (final_level BETWEEN 1 AND 4),
    policy_slug  TEXT NOT NULL DEFAULT '',
    clause_no    TEXT NOT NULL DEFAULT '',
    active       INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX idx_authority_rules_form ON authority_rules(form_id, active);

ALTER TABLE approval_lines ADD COLUMN authority_json TEXT NOT NULL DEFAULT '';
ALTER TABLE approval_steps ADD COLUMN final_authority INTEGER NOT NULL DEFAULT 0 CHECK (final_authority IN (0,1));
ALTER TABLE approval_steps ADD COLUMN skip_reason TEXT NOT NULL DEFAULT '';

INSERT INTO authority_rules (form_id, min_amount, max_amount, final_level, policy_slug, clause_no, active, created_at, updated_at)
SELECT id, 10000000, 30000000, 2, 'approval-authority', '제2조', 1, strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')
  FROM approval_forms WHERE code = 'EXP';
