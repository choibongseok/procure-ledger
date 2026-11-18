-- 0065 (B42 1단계, 5절·8절): 변경 피드. 증분 조회(/api/v1/changes)와 웹훅의 공통 원천이다.
-- 업무 서비스가 추적 대상(품목·거래처·발주·입고·전표·채무) 행을 쓰는 같은 트랜잭션에서 changes.Note 로 한 줄씩 쌓는다(롤백되면 줄도 없다).
-- **값은 담지 않는다**: 자원 종류·번호·동작·상태 코드·시각·바꾼 경로(채널)뿐이다. 소비자는 값을 API 로 읽는다.
-- 추가 전용이다(UPDATE·DELETE 트리거). 보존 정리(changes.keep_days)는 서버 주기 작업이 트랜잭션 안에서 트리거를 일시 해제해서만 한다(후속).
-- 번호(seq) 순서 = 커밋 순서: PG 는 Note 가 첫 호출에서 피드 전용 advisory lock(dbutil.LockChangeFeed)을 트랜잭션 끝까지 쥔 뒤 번호를 받는다.
CREATE TABLE change_events (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    at            TEXT NOT NULL,                     -- 고정 폭 UTC(2006-01-02T15:04:05.000000000Z)
    resource      TEXT NOT NULL CHECK (resource IN ('item','vendor','purchase_order','receipt','journal','payable')),
    object_id     INTEGER NOT NULL,
    op            TEXT NOT NULL CHECK (op IN ('created','updated','status_changed','canceled')),
    status        TEXT NOT NULL DEFAULT '',          -- 새 상태 코드(있는 자원만: 발주·전표)
    actor_channel TEXT NOT NULL DEFAULT 'web' CHECK (actor_channel IN ('web','mcp','llmgw','cli','scim','api'))
);
CREATE INDEX idx_change_events_object ON change_events(resource, object_id, seq);
CREATE TRIGGER trg_change_events_no_update BEFORE UPDATE ON change_events
BEGIN SELECT RAISE(ABORT, 'change_events 는 추가 전용입니다'); END;
CREATE TRIGGER trg_change_events_no_delete BEFORE DELETE ON change_events
BEGIN SELECT RAISE(ABORT, 'change_events 는 추가 전용입니다'); END;
