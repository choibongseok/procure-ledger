-- 0049: 기초 데이터 가져오기의 기초 재고·미지급 잔액(B35,). (0048 은 B35 platform 의 묶음·행 표가 쓴다.)
--  - account_rules 에 규칙 키 opening.equity(기초 이월 계정, 기본 3210 이익잉여금)를 더한다. 규칙 계정이 중지되면 기초 전표를 만들 수 없다.
--  - opening_payable_lines: 기초 전표(종류 opening)의 외상매입금 줄이 가진 원 시스템의 문서 번호·발생일·만기일. 전표가 승인될 때 채무 항목의
--    발생일·만기일이 이 값이 되고(없으면 전표 거래일 + 거래처 결제 조건), 같은 (거래처, 문서 번호)를 다시 가져오지 않게 판정하는 기준이다. 추가 전용.
INSERT OR IGNORE INTO account_rules (key, account_id, updated_at)
SELECT 'opening.equity', id, strftime('%Y-%m-%dT%H:%M:%SZ','now') FROM accounts WHERE code = '3210';

CREATE TABLE opening_payable_lines (
    line_id    INTEGER PRIMARY KEY REFERENCES journal_lines(id),
    vendor_id  INTEGER NOT NULL REFERENCES vendors(id),
    doc_no     TEXT NOT NULL CHECK (length(doc_no) BETWEEN 1 AND 40),
    trade_date TEXT NOT NULL CHECK (trade_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    due_date   TEXT NOT NULL CHECK (due_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    batch_no   TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX idx_opening_payable_vendor_doc ON opening_payable_lines(vendor_id, doc_no);
CREATE TRIGGER trg_opening_payable_lines_no_update BEFORE UPDATE ON opening_payable_lines BEGIN SELECT RAISE(ABORT, '기초 미지급 항목은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_opening_payable_lines_no_delete BEFORE DELETE ON opening_payable_lines BEGIN SELECT RAISE(ABORT, '기초 미지급 항목은 지울 수 없습니다'); END;
