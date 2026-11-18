-- 0048: 기초 데이터 가져오기 묶음·행(B35 platform 몫, 6절). (0047 은 B34 단계 2 가 쓴다.)
--  - import_batches: 파일 한 개를 올려 미리보기·반영까지 한 번 한 기록. 파일 내용·원래 파일 이름은 저장하지 않는다(해시·크기·행 수·건수만).
--    번호 IMP-YYYY-NNNN(UNIQUE). 상태 전이: previewed → applied | canceled | expired (끝 상태는 고칠 수 없고 지울 수도 없다).
--    token_hash 는 반영 토큰의 SHA-256 이다(토큰 원문은 미리보기 응답에서만 보인다). 끝 상태가 되면 비운다.
--    plan_sha256 은 미리보기 판정(행 번호·키·판정·바뀐 칸)의 지문이다. 반영 때 다시 계산해 같아야 한다(미리보기 뒤 데이터가 바뀌면 거절).
--  - import_rows: 행별 판정. 미리보기 때 만들고 반영되면 확정된다. 값은 어댑터가 가린 화면용 값만 담는다(계좌·연락처 원문 없음).
--    취소·만료된 묶음의 행은 지울 수 있지만 반영된 묶음의 행은 고치거나 지울 수 없다.
CREATE TABLE import_batches (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    no               TEXT NOT NULL UNIQUE CHECK (no GLOB 'IMP-[0-9][0-9][0-9][0-9]-[0-9]*'),
    target           TEXT NOT NULL CHECK (target IN ('account','warehouse','vendor','item','budget','opening_stock','opening_payable')),
    status           TEXT NOT NULL DEFAULT 'previewed' CHECK (status IN ('previewed','applied','expired','canceled')),
    file_sha256      TEXT NOT NULL CHECK (length(file_sha256) = 64),
    file_size        INTEGER NOT NULL CHECK (file_size >= 0),
    row_count        INTEGER NOT NULL CHECK (row_count >= 0),
    template_version INTEGER NOT NULL DEFAULT 0,
    base_date        TEXT NOT NULL DEFAULT '',
    counts_json      TEXT NOT NULL,
    summary_json     TEXT NOT NULL DEFAULT '[]',
    plan_sha256      TEXT NOT NULL,
    token_hash       TEXT NOT NULL DEFAULT '',
    journal_nos_json TEXT NOT NULL DEFAULT '[]',
    created_by       INTEGER NOT NULL REFERENCES users(id),
    created_at       TEXT NOT NULL,
    applied_at       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_import_batches_target ON import_batches(target, status, id);
CREATE INDEX idx_import_batches_created ON import_batches(created_at);
CREATE INDEX idx_import_batches_hash ON import_batches(target, file_sha256);

CREATE TRIGGER trg_import_batches_no_delete BEFORE DELETE ON import_batches BEGIN SELECT RAISE(ABORT, '가져오기 묶음은 지울 수 없습니다'); END;
CREATE TRIGGER trg_import_batches_final BEFORE UPDATE ON import_batches
WHEN OLD.status <> 'previewed'
BEGIN SELECT RAISE(ABORT, '끝난 가져오기 묶음은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_import_batches_fixed BEFORE UPDATE ON import_batches
WHEN NEW.no <> OLD.no OR NEW.target <> OLD.target OR NEW.file_sha256 <> OLD.file_sha256 OR NEW.plan_sha256 <> OLD.plan_sha256
  OR NEW.created_by <> OLD.created_by OR NEW.created_at <> OLD.created_at OR NEW.base_date <> OLD.base_date OR NEW.counts_json <> OLD.counts_json
BEGIN SELECT RAISE(ABORT, '가져오기 묶음의 파일·대상·판정은 고칠 수 없습니다'); END;

CREATE TABLE import_rows (
    batch_id    INTEGER NOT NULL REFERENCES import_batches(id),
    row_no      INTEGER NOT NULL CHECK (row_no >= 1),
    key_text    TEXT NOT NULL DEFAULT '',
    verdict     TEXT NOT NULL CHECK (verdict IN ('add','change','same','error')),
    reason      TEXT NOT NULL DEFAULT '',
    warn        TEXT NOT NULL DEFAULT '',
    change_json TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (batch_id, row_no)
) WITHOUT ROWID;
CREATE TRIGGER trg_import_rows_no_update BEFORE UPDATE ON import_rows BEGIN SELECT RAISE(ABORT, '가져오기 행은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_import_rows_no_delete BEFORE DELETE ON import_rows
WHEN (SELECT status FROM import_batches WHERE id = OLD.batch_id) = 'applied'
BEGIN SELECT RAISE(ABORT, '반영된 가져오기 행은 지울 수 없습니다'); END;
