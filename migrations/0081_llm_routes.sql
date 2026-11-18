-- 0081 (B45 1단계, 명세 4절): 용도·부서·역할별 라우팅 규칙. 판정 순서는 명세 5절(부서 > 용도 구체성 > 역할 > 순서 > 번호). 규칙이 없으면 기본 경로다.
CREATE TABLE llm_routes (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    sort_order           INTEGER NOT NULL DEFAULT 0,
    purpose_pattern      TEXT NOT NULL CHECK (length(purpose_pattern) BETWEEN 1 AND 80),
    department_id        INTEGER NOT NULL DEFAULT 0,
    role                 TEXT NOT NULL DEFAULT '' CHECK (length(role) <= 40),
    primary_provider_id  INTEGER NOT NULL REFERENCES llm_providers(id),
    primary_model        TEXT NOT NULL DEFAULT '' CHECK (length(primary_model) <= 120),
    fallback_provider_id INTEGER REFERENCES llm_providers(id),
    fallback_model       TEXT NOT NULL DEFAULT '' CHECK (length(fallback_model) <= 120),
    max_attempts         INTEGER NOT NULL DEFAULT 2 CHECK (max_attempts BETWEEN 1 AND 3),
    total_timeout_ms     INTEGER NOT NULL DEFAULT 60000 CHECK (total_timeout_ms BETWEEN 1000 AND 300000),
    active               INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_by           INTEGER REFERENCES users(id),
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    CHECK (fallback_provider_id IS NULL OR fallback_provider_id <> primary_provider_id)
);
CREATE INDEX idx_llm_routes_active ON llm_routes(active, sort_order);
