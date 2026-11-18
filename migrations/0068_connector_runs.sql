-- 0068 (B42 3단계, 명세 11절·13.2): 커넥터 실행 이력과 쓰기 API 분당 한도 창.
--  - connector_runs: erp-admin connector run 한 번의 기록(매핑 이름·방향·시각·행 수·결과 코드·감사 번호). 행의 값·토큰·주소는 담지 않는다.
--    추가 전용(고치기·지우기 거부)이다. 매핑 파일 자체는 서버에 저장하지 않고 해시만 남긴다.
--  - api_write_windows: 쓰기 POST 의 토큰별 분 창 카운터(읽기 한도 api_rate_windows 와 따로, 기본 분당 30).
CREATE TABLE connector_runs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    mapping       TEXT NOT NULL CHECK (length(mapping) BETWEEN 1 AND 60),
    mapping_hash  TEXT NOT NULL,
    direction     TEXT NOT NULL CHECK (direction IN ('in','out')),
    target        TEXT NOT NULL,
    dry_run       INTEGER NOT NULL DEFAULT 0 CHECK (dry_run IN (0,1)),
    started_at    TEXT NOT NULL,
    finished_at   TEXT NOT NULL,
    rows_total    INTEGER NOT NULL DEFAULT 0 CHECK (rows_total >= 0),
    rows_ok       INTEGER NOT NULL DEFAULT 0 CHECK (rows_ok >= 0),
    rows_error    INTEGER NOT NULL DEFAULT 0 CHECK (rows_error >= 0),
    result_code   TEXT NOT NULL CHECK (result_code IN ('ok','dry_run','row_errors','failed')),
    audit_seq     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_connector_runs_started ON connector_runs(started_at);
CREATE TRIGGER trg_connector_runs_no_update BEFORE UPDATE ON connector_runs
BEGIN SELECT RAISE(ABORT, '커넥터 실행 이력은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_connector_runs_no_delete BEFORE DELETE ON connector_runs
BEGIN SELECT RAISE(ABORT, '커넥터 실행 이력은 지울 수 없습니다'); END;

CREATE TABLE api_write_windows (
    token_id INTEGER NOT NULL REFERENCES api_tokens(id),
    minute   INTEGER NOT NULL,
    calls    INTEGER NOT NULL DEFAULT 0,
    denied   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (token_id, minute)
);

INSERT OR IGNORE INTO app_settings (key, value) VALUES ('api.write_per_min', '30');
