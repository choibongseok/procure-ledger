-- 0013: 전자결재 표준 단계 1 (B23, 4절).
-- 1) 양식(approval_forms)과 양식 버전(approval_form_versions). 게시된 버전은 내용을 바꿀 수 없다(상태 전이만 허용, 삭제 불가).
-- 2) purchase_requests 표에 양식 열을 더한다. 표 이름은 바꾸지 않는다(일반 문서도 이 표에 들어간다, 명세 3.1 D1).
-- 3) 기본 양식 4종 시드와 기존 구매요청 채움: 모든 기존 건은 시스템 양식 "구매 품의서" 버전 1 에 묶이고 amount 는 합계 금액이다.
-- 4) 표 밖에서 들어오는 구매요청(개발 시드·검증 도구)도 양식 열이 비지 않도록 INSERT 뒤 트리거가 구매 품의서로 채우고,
--    구매 품의서의 amount 는 합계 금액(total_amount)을 따라간다.
-- 기존 동작은 바뀌지 않는다. 열이 추가될 뿐 기존 SQL 은 그대로 동작한다.

CREATE TABLE approval_forms (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    code                TEXT NOT NULL UNIQUE CHECK (code GLOB '[A-Z0-9]*' AND length(code) <= 20),
    category            TEXT NOT NULL CHECK (category IN ('general','purchase','finance','cooperation','other')),
    name                TEXT NOT NULL CHECK (length(name) > 0),
    kind                TEXT NOT NULL CHECK (kind IN ('general','purchase')),
    active              INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    required_permission TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL
);

CREATE TABLE approval_form_versions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    form_id      INTEGER NOT NULL REFERENCES approval_forms(id),
    version      INTEGER NOT NULL CHECK (version >= 1),
    status       TEXT NOT NULL CHECK (status IN ('draft','published','retired')),
    definition   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    published_at TEXT,
    UNIQUE (form_id, version)
);
CREATE INDEX idx_approval_form_versions_form ON approval_form_versions(form_id, status);

-- 게시된 버전(published·retired)의 내용은 바꿀 수 없다. 상태는 draft → published → retired 로만 간다.
CREATE TRIGGER trg_form_versions_content_fixed BEFORE UPDATE OF form_id, version, definition, created_at ON approval_form_versions
WHEN OLD.status <> 'draft'
BEGIN SELECT RAISE(ABORT, '게시된 양식 버전은 수정할 수 없습니다'); END;

CREATE TRIGGER trg_form_versions_status_flow BEFORE UPDATE OF status ON approval_form_versions
WHEN NOT ((OLD.status = 'draft' AND NEW.status IN ('draft','published'))
       OR (OLD.status = 'published' AND NEW.status IN ('published','retired'))
       OR (OLD.status = 'retired' AND NEW.status = 'retired'))
BEGIN SELECT RAISE(ABORT, '양식 버전의 상태를 되돌릴 수 없습니다'); END;

CREATE TRIGGER trg_form_versions_no_delete BEFORE DELETE ON approval_form_versions
WHEN OLD.status <> 'draft'
BEGIN SELECT RAISE(ABORT, '게시된 양식 버전은 삭제할 수 없습니다'); END;

-- ── 기본 양식 4종(명세 3.5) ─────────────────────────────────────────────
INSERT INTO approval_forms (id, code, category, name, kind, active, required_permission, created_at) VALUES
  (1, 'PUR',  'purchase',    '구매 품의서', 'purchase', 1, 'purchase:create', strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  (2, 'GEN',  'general',     '일반 기안서', 'general',  1, 'draft:create',    strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  (3, 'EXP',  'finance',     '지출 결의서', 'general',  1, 'draft:create',    strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  (4, 'COOP', 'cooperation', '업무 협조전', 'general',  1, 'draft:create',    strftime('%Y-%m-%dT%H:%M:%SZ','now'));

-- 구매 품의서(시스템 양식): 필드(거래처·사유·품목 표)와 본문은 코드가 정한다. 결재선은 금액 구간 규칙(approval_line_rules).
INSERT INTO approval_form_versions (form_id, version, status, definition, created_at, published_at) VALUES
 (1, 1, 'published', '{"system":true,"fields":[],"body":"","line":{"rule":"amount_tiers"},"recipients":{"cc":[],"receive":[]},"numbering":{"pattern":"PR-{id:6}","assign_at":"create","reset":"never"},"retention_years":5,"dept_limit":[],"settings":{"line_edit":"add_only","objection_action":"record","amount_field":"","dept_visible":true,"urgent_allowed":true}}',
  strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 (2, 1, 'published', '{"system":false,"fields":[{"key":"effective_date","label":"시행 일자","type":"date","required":false},{"key":"content","label":"본문","type":"textarea","required":true,"maxLength":5000},{"key":"attachments","label":"첨부","type":"attachment","required":false,"maxFiles":20}],"body":"","line":{"steps":[{"role":"approve","kind":"team_lead","source":"org_level","level":1,"required":true},{"role":"approve","kind":"dept_head","source":"org_level","level":2,"required":true}]},"recipients":{"cc":[],"receive":[]},"numbering":{"pattern":"{unit}-{yyyy}-{seq:4}","assign_at":"submit","reset":"yearly"},"retention_years":5,"dept_limit":[],"settings":{"line_edit":"free","objection_action":"record","amount_field":"","dept_visible":false,"urgent_allowed":true}}',
  strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 (3, 1, 'published', '{"system":false,"fields":[{"key":"spend_date","label":"지출 일자","type":"date","required":true},{"key":"spend_kind","label":"지출 구분","type":"select","required":true,"options":["복리후생","출장","소모품","교육","기타"]},{"key":"payee","label":"지급 대상","type":"text","required":true,"maxLength":200,"sensitive":true},{"key":"items","label":"지출 내역","type":"table","required":true,"minRows":1,"maxRows":30,"columns":[{"key":"name","label":"항목","type":"text","required":true},{"key":"amount","label":"금액","type":"amount","required":true}]},{"key":"total","label":"금액","type":"amount","required":true},{"key":"evidence","label":"증빙","type":"attachment","required":true,"maxFiles":10},{"key":"reason","label":"사유","type":"textarea","required":false,"maxLength":2000}],"body":"","line":{"rule":"amount_tiers"},"recipients":{"cc":[],"receive":[{"unit_code":"FIN"}]},"numbering":{"pattern":"EXP-{yyyy}-{seq:5}","assign_at":"submit","reset":"yearly"},"retention_years":5,"dept_limit":[],"settings":{"line_edit":"add_only","objection_action":"record","amount_field":"total","dept_visible":false,"urgent_allowed":true}}',
  strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now')),
 (4, 1, 'published', '{"system":false,"fields":[{"key":"target_unit","label":"협조 요청 부서","type":"unit","required":true},{"key":"request","label":"요청 사항","type":"textarea","required":true,"maxLength":5000},{"key":"reply_by","label":"회신 희망일","type":"date","required":false},{"key":"attachments","label":"첨부","type":"attachment","required":false,"maxFiles":20}],"body":"","line":{"steps":[{"role":"approve","kind":"team_lead","source":"org_level","level":1,"required":true},{"role":"approve","kind":"dept_head","source":"org_level","level":2,"required":true},{"role":"agree","kind":"dept_head","source":"field_unit","field":"target_unit","required":true},{"role":"receive","source":"field_unit","field":"target_unit","required":true}]},"recipients":{"cc":[],"receive":[]},"numbering":{"pattern":"{unit}-협조-{yyyy}-{seq:4}","assign_at":"submit","reset":"yearly"},"retention_years":3,"dept_limit":[],"settings":{"line_edit":"add_only","objection_action":"reject","amount_field":"","dept_visible":false,"urgent_allowed":true}}',
  strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now'));

-- ── 구매요청 표에 양식 열 추가(표 이름은 그대로) ────────────────────────
ALTER TABLE purchase_requests ADD COLUMN form_id INTEGER REFERENCES approval_forms(id);
ALTER TABLE purchase_requests ADD COLUMN form_version_id INTEGER REFERENCES approval_form_versions(id);
ALTER TABLE purchase_requests ADD COLUMN fields TEXT NOT NULL DEFAULT '{}';
ALTER TABLE purchase_requests ADD COLUMN amount INTEGER;
ALTER TABLE purchase_requests ADD COLUMN urgent INTEGER NOT NULL DEFAULT 0 CHECK (urgent IN (0,1));
ALTER TABLE purchase_requests ADD COLUMN retention_until TEXT;
ALTER TABLE purchase_requests ADD COLUMN completed_at TEXT;
CREATE INDEX idx_pr_form ON purchase_requests(form_id);

-- 기존 구매요청은 모두 구매 품의서 버전 1 이다.
UPDATE purchase_requests
   SET form_id = 1,
       form_version_id = (SELECT id FROM approval_form_versions WHERE form_id = 1 AND version = 1),
       amount = total_amount;

-- 표 밖에서 넣은 행(개발 시드·검증 도구·옛 코드)도 구매 품의서로 채운다. 구매 품의서의 amount 는 합계 금액을 따라간다.
CREATE TRIGGER trg_pr_form_default AFTER INSERT ON purchase_requests
WHEN NEW.form_id IS NULL
BEGIN
    UPDATE purchase_requests
       SET form_id = 1,
           form_version_id = (SELECT id FROM approval_form_versions WHERE form_id = 1 AND version = 1),
           amount = NEW.total_amount
     WHERE id = NEW.id;
END;

CREATE TRIGGER trg_pr_amount_follows_total AFTER UPDATE OF total_amount ON purchase_requests
WHEN NEW.form_id = 1
BEGIN
    UPDATE purchase_requests SET amount = NEW.total_amount WHERE id = NEW.id;
END;
