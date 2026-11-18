-- 0091 (B46 판정 후속, 명세 46 22절): ① 끝난 실행의 문항 결과는 고치거나 지울 수 없다(진행 중 실행에 대한 INSERT·DELETE 는 허용: 실패 정리). ② llm 채점 문항은 기대 답이 비어도 된다(루브릭의 필수·금지 줄로만 채점).
CREATE TRIGGER trg_eval_run_items_done_no_update BEFORE UPDATE ON eval_run_items
WHEN (SELECT status FROM eval_runs WHERE id = OLD.run_id) <> 'running'
BEGIN SELECT RAISE(ABORT, '끝난 실행의 문항 결과는 고칠 수 없습니다'); END;
CREATE TRIGGER trg_eval_run_items_done_no_delete BEFORE DELETE ON eval_run_items
WHEN (SELECT status FROM eval_runs WHERE id = OLD.run_id) <> 'running'
BEGIN SELECT RAISE(ABORT, '끝난 실행의 문항 결과는 고칠 수 없습니다'); END;

-- SQLite 는 제약을 바꿀 수 없어 eval_items 를 다시 만든다(expected 의 최소 길이 1 을 0 으로). 행·번호는 그대로 옮기고 트리거를 다시 만든다(0085 와 같은 모양).
CREATE TABLE eval_items_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    version_id   INTEGER NOT NULL REFERENCES eval_set_versions(id),
    seq          INTEGER NOT NULL CHECK (seq >= 1),
    question     TEXT NOT NULL CHECK (length(question) BETWEEN 1 AND 4000),
    context_json TEXT NOT NULL DEFAULT '' CHECK (length(context_json) <= 4000),
    expected     TEXT NOT NULL CHECK (length(expected) <= 4000),
    grader       TEXT NOT NULL CHECK (grader IN ('exact','contains','number','llm')),
    tolerance    TEXT NOT NULL DEFAULT '' CHECK (length(tolerance) <= 20),
    rubric       TEXT NOT NULL DEFAULT '' CHECK (length(rubric) <= 2000),
    critical     INTEGER NOT NULL DEFAULT 0 CHECK (critical IN (0,1)),
    tags         TEXT NOT NULL DEFAULT '' CHECK (length(tags) <= 200),
    masked_count INTEGER NOT NULL DEFAULT 0 CHECK (masked_count >= 0),
    UNIQUE (version_id, seq)
);
INSERT INTO eval_items_new (id, version_id, seq, question, context_json, expected, grader, tolerance, rubric, critical, tags, masked_count)
    SELECT id, version_id, seq, question, context_json, expected, grader, tolerance, rubric, critical, tags, masked_count FROM eval_items;
DROP TABLE eval_items;
ALTER TABLE eval_items_new RENAME TO eval_items;

CREATE TRIGGER trg_eval_items_published_no_insert BEFORE INSERT ON eval_items
WHEN (SELECT status FROM eval_set_versions WHERE id = NEW.version_id) = 'published'
BEGIN SELECT RAISE(ABORT, '게시된 버전은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_eval_items_published_no_update BEFORE UPDATE ON eval_items
WHEN (SELECT status FROM eval_set_versions WHERE id = OLD.version_id) = 'published'
BEGIN SELECT RAISE(ABORT, '게시된 버전은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_eval_items_published_no_delete BEFORE DELETE ON eval_items
WHEN (SELECT status FROM eval_set_versions WHERE id = OLD.version_id) = 'published'
BEGIN SELECT RAISE(ABORT, '게시된 버전은 고칠 수 없습니다'); END;
