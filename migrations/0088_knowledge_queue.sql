-- 0088 (B47 1단계, 명세 4.2절): 색인 대기열(아웃박스)과 세대 번호. 업무 트랜잭션이 같은 트랜잭션에서 줄을 넣고 워커가 추출·색인을 한다.
-- generation 은 색인이 바뀔 때마다 올라가고 서버·erp-mcp 프로세스가 주기적으로 읽어 자기 메모리 색인을 갱신한다.
CREATE TABLE knowledge_queue (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    op          TEXT NOT NULL CHECK (op IN ('upsert','remove')),
    source_kind TEXT NOT NULL CHECK (source_kind IN ('approval_doc','attachment','upload')),
    source_id   INTEGER NOT NULL,
    entity_id   INTEGER NOT NULL DEFAULT 1,
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    claimed_at  TEXT,
    done_at     TEXT,
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_knowledge_queue_open ON knowledge_queue(done_at, id);
CREATE TABLE knowledge_state (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    generation INTEGER NOT NULL,
    changed_at TEXT NOT NULL
);
INSERT INTO knowledge_state (id, generation, changed_at) VALUES (1, 1, strftime('%Y-%m-%dT%H:%M:%SZ','now'));
