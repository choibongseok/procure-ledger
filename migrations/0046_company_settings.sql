-- 0046: 회사 설정(B34 단계 1,). 회사 정보(단일 행)와 직인·로고 이미지(교체 이력 보관).
--  - company_settings: id=1 한 행. 한 번도 저장하지 않았으면 행이 없고(회사명은 조직도 최상위 조직 이름, 현행 동작), 삭제는 막는다.
--    fiscal_start_month 는 단계 2 가 쓴다(기본 1 = 역년). version 은 낙관적 잠금이다.
--  - company_assets: 로고·직인 이미지. 종류별 현재 한 행(current=1)만 있고 교체하면 이전 행은 이력으로 남는다(삭제·내용 수정 금지).
CREATE TABLE company_settings (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    name               TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    business_no        TEXT NOT NULL DEFAULT '' CHECK (length(business_no) <= 20),
    representative     TEXT NOT NULL DEFAULT '' CHECK (length(representative) <= 50),
    address            TEXT NOT NULL DEFAULT '' CHECK (length(address) <= 200),
    phone              TEXT NOT NULL DEFAULT '' CHECK (length(phone) <= 30),
    fiscal_start_month INTEGER NOT NULL DEFAULT 1 CHECK (fiscal_start_month BETWEEN 1 AND 12),
    version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    updated_by         INTEGER REFERENCES users(id),
    updated_at         TEXT NOT NULL
);
CREATE TRIGGER trg_company_settings_no_delete BEFORE DELETE ON company_settings BEGIN SELECT RAISE(ABORT, '회사 설정은 지울 수 없습니다'); END;

CREATE TABLE company_assets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT NOT NULL CHECK (kind IN ('logo','seal')),
    content      BLOB NOT NULL,
    content_type TEXT NOT NULL CHECK (content_type IN ('image/png','image/jpeg')),
    size         INTEGER NOT NULL CHECK (size > 0),
    sha256       TEXT NOT NULL,
    width        INTEGER NOT NULL,
    height       INTEGER NOT NULL,
    uploaded_by  INTEGER REFERENCES users(id),
    uploaded_at  TEXT NOT NULL,
    current      INTEGER NOT NULL DEFAULT 1 CHECK (current IN (0,1))
);
CREATE UNIQUE INDEX idx_company_assets_current ON company_assets(kind) WHERE current = 1;
CREATE TRIGGER trg_company_assets_no_delete BEFORE DELETE ON company_assets BEGIN SELECT RAISE(ABORT, '회사 이미지는 지울 수 없습니다'); END;
CREATE TRIGGER trg_company_assets_fixed BEFORE UPDATE ON company_assets
WHEN NEW.kind IS NOT OLD.kind OR NEW.content IS NOT OLD.content OR NEW.sha256 IS NOT OLD.sha256 OR NEW.size IS NOT OLD.size OR NEW.uploaded_at IS NOT OLD.uploaded_at OR NEW.current > OLD.current
BEGIN SELECT RAISE(ABORT, '회사 이미지는 고칠 수 없습니다(현재 여부만 바뀝니다)'); END;
