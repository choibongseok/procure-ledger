-- 0085 (B46 1단계, 명세 4절): 평가 문항. 질문·기대·루브릭은 저장 전에 마스킹된 글뿐이다. 게시된 버전의 문항은 고칠 수 없다(트리거).
CREATE TABLE eval_items (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id   INTEGER NOT NULL REFERENCES eval_set_versions(id),
    seq          INTEGER NOT NULL CHECK (seq >= 1),
    question     TEXT NOT NULL CHECK (length(question) BETWEEN 1 AND 4000),
    context_json TEXT NOT NULL DEFAULT '' CHECK (length(context_json) <= 4000),
    expected     TEXT NOT NULL CHECK (length(expected) BETWEEN 1 AND 4000),
    grader       TEXT NOT NULL CHECK (grader IN ('exact','contains','number','llm')),
    tolerance    TEXT NOT NULL DEFAULT '' CHECK (length(tolerance) <= 20),
    rubric       TEXT NOT NULL DEFAULT '' CHECK (length(rubric) <= 2000),
    critical     INTEGER NOT NULL DEFAULT 0 CHECK (critical IN (0,1)),
    tags         TEXT NOT NULL DEFAULT '' CHECK (length(tags) <= 200),
    masked_count INTEGER NOT NULL DEFAULT 0 CHECK (masked_count >= 0),
    UNIQUE (version_id, seq)
);

CREATE TRIGGER trg_eval_items_published_no_insert BEFORE INSERT ON eval_items
WHEN (SELECT status FROM eval_set_versions WHERE id = NEW.version_id) = 'published'
BEGIN SELECT RAISE(ABORT, '게시된 버전은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_eval_items_published_no_update BEFORE UPDATE ON eval_items
WHEN (SELECT status FROM eval_set_versions WHERE id = OLD.version_id) = 'published'
BEGIN SELECT RAISE(ABORT, '게시된 버전은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_eval_items_published_no_delete BEFORE DELETE ON eval_items
WHEN (SELECT status FROM eval_set_versions WHERE id = OLD.version_id) = 'published'
BEGIN SELECT RAISE(ABORT, '게시된 버전은 고칠 수 없습니다'); END;
