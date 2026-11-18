-- 0080 (B45 1단계, 4절): LLM 공급자. 설정(키·자격 증명·주소)은 봉인(용도 llm-provider)으로만 저장한다.
-- 환경변수 기본 경로(PL_LLM_PROVIDER)는 행 없이 가상 공급자 default 로 다룬다. 행이 없는 설치는 동작이 이전과 같다.
CREATE TABLE llm_providers (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    code                   TEXT NOT NULL UNIQUE CHECK (length(code) BETWEEN 2 AND 30 AND code NOT GLOB '*[^a-z0-9-]*'),
    kind                   TEXT NOT NULL CHECK (kind IN ('mock','anthropic','bedrock','vertex','openai_compat')),
    name                   TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
    default_model          TEXT NOT NULL DEFAULT '' CHECK (length(default_model) <= 120),
    secret_sealed          TEXT NOT NULL DEFAULT '',
    hint                   TEXT NOT NULL DEFAULT '' CHECK (length(hint) <= 200),
    mode                   TEXT NOT NULL DEFAULT '' CHECK (length(mode) <= 30),
    external               INTEGER NOT NULL DEFAULT 1 CHECK (external IN (0,1)),
    supports_images        INTEGER NOT NULL DEFAULT 0 CHECK (supports_images IN (0,1)),
    price_in_micro_per_mtok  INTEGER NOT NULL DEFAULT 0 CHECK (price_in_micro_per_mtok >= 0),
    price_out_micro_per_mtok INTEGER NOT NULL DEFAULT 0 CHECK (price_out_micro_per_mtok >= 0),
    timeout_ms             INTEGER NOT NULL DEFAULT 30000 CHECK (timeout_ms BETWEEN 1000 AND 120000),
    active                 INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
    version                INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_by             INTEGER REFERENCES users(id),
    created_at             TEXT NOT NULL,
    updated_by             INTEGER REFERENCES users(id),
    updated_at             TEXT NOT NULL
);
