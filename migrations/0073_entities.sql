-- 0073 (B44 1단계 platform, 4·7·8절): 법인·사업장 표와 기본 법인 이관, 조직 구간의 법인 파생 열, 세션의 현재 법인.
--  - entities: 법인 표(사업자번호·회계연도 시작월 등 company_settings 와 같은 열). 기본 법인 = id 1(코드 MAIN)이고 기존 단일 회사(company_settings,
--    없으면 조직도 최상위 조직 이름)에서 만든다. 삭제는 없고 사용 여부만 바꾼다.
--  - company_settings 는 이관 증거이자 B34 회사 설정 화면의 저장소로 남는다. 저장하면 기본 법인 행이 같은 값으로 따라온다(아래 트리거).
--    (명세 D3 의 "쓰기 금지"는 회사 서비스가 entities 를 읽게 바뀌는 2단계에서 한다. 그때까지 동작 불변이 먼저다.)
--  - entity_sites: 법인의 사업장(부가세 신고 단위). 기본 법인에는 주사업장(0000)을 하나 만든다.
--  - org_unit_spans.entity_id: 조직의 법인(루트에서 물려받는 파생 값, 조직 서비스가 구간을 다시 쓸 때 채운다). 기본값 1.
--  - sessions.active_entity_id: 세션의 현재 법인(0 = 정하지 않음 → 소속 법인).
CREATE TABLE entities (
    id                 INTEGER PRIMARY KEY,
    code               TEXT NOT NULL UNIQUE CHECK (code GLOB '[A-Z0-9]*' AND length(code) BETWEEN 2 AND 6 AND code NOT GLOB '*[^A-Z0-9]*'),
    name               TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    business_no        TEXT NOT NULL DEFAULT '' CHECK (length(business_no) <= 20),
    representative     TEXT NOT NULL DEFAULT '' CHECK (length(representative) <= 50),
    address            TEXT NOT NULL DEFAULT '' CHECK (length(address) <= 200),
    phone              TEXT NOT NULL DEFAULT '' CHECK (length(phone) <= 30),
    fiscal_start_month INTEGER NOT NULL DEFAULT 1 CHECK (fiscal_start_month BETWEEN 1 AND 12),
    root_unit_id       INTEGER REFERENCES org_units(id),
    is_default         INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
    active             INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_by         INTEGER REFERENCES users(id),
    created_at         TEXT NOT NULL,
    updated_by         INTEGER REFERENCES users(id),
    updated_at         TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_entities_business_no ON entities(replace(business_no, '-', '')) WHERE business_no <> '';
CREATE UNIQUE INDEX idx_entities_root_unit ON entities(root_unit_id) WHERE root_unit_id IS NOT NULL;
CREATE UNIQUE INDEX idx_entities_default ON entities(is_default) WHERE is_default = 1;
CREATE TRIGGER trg_entities_no_delete BEFORE DELETE ON entities
BEGIN SELECT RAISE(ABORT, '법인은 지울 수 없습니다(사용 여부만 바꿉니다)'); END;
CREATE TRIGGER trg_entities_default_active BEFORE UPDATE ON entities
WHEN OLD.is_default = 1 AND (NEW.active = 0 OR NEW.is_default = 0)
BEGIN SELECT RAISE(ABORT, '기본 법인은 비활성화할 수 없습니다'); END;

INSERT INTO entities (id, code, name, business_no, representative, address, phone, fiscal_start_month, root_unit_id, is_default, active, version, created_at, updated_at)
SELECT 1, 'MAIN',
    COALESCE((SELECT name FROM company_settings WHERE id = 1),
             (SELECT s.name FROM org_unit_spans s WHERE s.parent_id = 0 AND s.valid_to IS NULL ORDER BY s.unit_id LIMIT 1), '회사'),
    COALESCE((SELECT business_no FROM company_settings WHERE id = 1), ''),
    COALESCE((SELECT representative FROM company_settings WHERE id = 1), ''),
    COALESCE((SELECT address FROM company_settings WHERE id = 1), ''),
    COALESCE((SELECT phone FROM company_settings WHERE id = 1), ''),
    COALESCE((SELECT fiscal_start_month FROM company_settings WHERE id = 1), 1),
    (SELECT s.unit_id FROM org_unit_spans s WHERE s.parent_id = 0 AND s.valid_to IS NULL ORDER BY s.unit_id LIMIT 1),
    1, 1, 1,
    strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now');

-- 회사 설정(B34)을 저장하면 기본 법인 행이 따라온다.
CREATE TRIGGER trg_company_settings_entity_ai AFTER INSERT ON company_settings
BEGIN
    UPDATE entities SET name = NEW.name, business_no = NEW.business_no, representative = NEW.representative, address = NEW.address, phone = NEW.phone,
        fiscal_start_month = NEW.fiscal_start_month, version = NEW.version, updated_by = NEW.updated_by, updated_at = NEW.updated_at WHERE id = 1;
END;
CREATE TRIGGER trg_company_settings_entity_au AFTER UPDATE ON company_settings
BEGIN
    UPDATE entities SET name = NEW.name, business_no = NEW.business_no, representative = NEW.representative, address = NEW.address, phone = NEW.phone,
        fiscal_start_month = NEW.fiscal_start_month, version = NEW.version, updated_by = NEW.updated_by, updated_at = NEW.updated_at WHERE id = 1;
END;

CREATE TABLE entity_sites (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_id  INTEGER NOT NULL REFERENCES entities(id),
    site_no    TEXT NOT NULL CHECK (length(site_no) = 4 AND site_no NOT GLOB '*[^0-9]*'),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    kind       TEXT NOT NULL CHECK (kind IN ('head','branch','factory')),
    address    TEXT NOT NULL DEFAULT '' CHECK (length(address) <= 200),
    vat_mode   TEXT NOT NULL DEFAULT 'own' CHECK (vat_mode IN ('own','consolidated')),
    active     INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (entity_id, site_no)
);
CREATE UNIQUE INDEX idx_entity_sites_head ON entity_sites(entity_id) WHERE kind = 'head';
CREATE TRIGGER trg_entity_sites_no_delete BEFORE DELETE ON entity_sites
BEGIN SELECT RAISE(ABORT, '사업장은 지울 수 없습니다(사용 여부만 바꿉니다)'); END;
INSERT INTO entity_sites (entity_id, site_no, name, kind, vat_mode, created_at, updated_at)
VALUES (1, '0000', '주사업장', 'head', 'own', strftime('%Y-%m-%dT%H:%M:%SZ','now'), strftime('%Y-%m-%dT%H:%M:%SZ','now'));

ALTER TABLE org_unit_spans ADD COLUMN entity_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE sessions ADD COLUMN active_entity_id INTEGER NOT NULL DEFAULT 0;
