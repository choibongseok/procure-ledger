-- 0084 (B46 1단계, 4절): 회사별 평가 세트와 세트 버전. 게시된 버전은 불변이다(트리거).
CREATE TABLE eval_sets (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    code            TEXT NOT NULL UNIQUE CHECK (length(code) BETWEEN 2 AND 30 AND code NOT GLOB '*[^a-z0-9-]*'),
    name            TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    purpose         TEXT NOT NULL CHECK (purpose IN ('doc','policy','tool','text')),
    description     TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    current_version INTEGER NOT NULL DEFAULT 0,
    baseline_run_id INTEGER NOT NULL DEFAULT 0,
    gate_drop_pct   INTEGER NOT NULL DEFAULT 2 CHECK (gate_drop_pct BETWEEN 0 AND 100),
    active          INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_by      INTEGER REFERENCES users(id),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE eval_set_versions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    set_id       INTEGER NOT NULL REFERENCES eval_sets(id),
    version      INTEGER NOT NULL CHECK (version >= 1),
    status       TEXT NOT NULL CHECK (status IN ('draft','published')),
    revision     INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    items_hash   TEXT NOT NULL DEFAULT '',
    note         TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    published_by INTEGER REFERENCES users(id),
    published_at TEXT NOT NULL DEFAULT '',
    created_by   INTEGER REFERENCES users(id),
    created_at   TEXT NOT NULL,
    UNIQUE (set_id, version)
);
CREATE UNIQUE INDEX idx_eval_set_versions_one_draft ON eval_set_versions(set_id) WHERE status = 'draft';

CREATE TRIGGER trg_eval_set_versions_published_no_update BEFORE UPDATE ON eval_set_versions
WHEN OLD.status = 'published'
BEGIN SELECT RAISE(ABORT, '게시된 버전은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_eval_set_versions_published_no_delete BEFORE DELETE ON eval_set_versions
WHEN OLD.status = 'published'
BEGIN SELECT RAISE(ABORT, '게시된 버전은 고칠 수 없습니다'); END;
