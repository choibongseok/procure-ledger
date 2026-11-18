-- 0071 (B43 2단계, 4절·5.4): 매입 세금계산서 3자 대조(세금계산서 ↔ 발주 ↔ 입고)의 제안과 확정된 연결.
--  - tax_invoice_matches: 대조 제안 한 건. 상태 전이 proposed → confirmed | dismissed, confirmed → unlinked (트리거가 한 번 더 막는다). 확정·기각·해제는 사람이 한다.
--    receipt_key 는 연결 후보 입고 번호를 작은 순으로 이은 글자(예 "12,15")이고, 기각한 후보 조합을 다시 제안하지 않는 데 쓴다.
--    같은 세금계산서에 제안(proposed)은 한 건, 확정(confirmed)도 한 건이다(부분 유일 색인).
--  - tax_invoice_match_receipts: 제안·확정에 속한 입고. confirmed=1 은 확정된 연결이고 한 입고는 확정된 연결을 하나만 가진다(부분 유일 색인). journal_id 는 확정 때 그 입고의 매입 전표(있으면)다.
--    값(금액·이름·번호)은 diff_json 에 금액·건수·날짜뿐이다.
CREATE TABLE tax_invoice_matches (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    invoice_id  INTEGER NOT NULL REFERENCES tax_invoices(id),
    status      TEXT NOT NULL CHECK (status IN ('proposed','confirmed','dismissed','unlinked')),
    result      TEXT NOT NULL CHECK (result IN ('match','ambiguous','amount_diff','vendor_unknown','no_receipt')),
    receipt_key TEXT NOT NULL DEFAULT '',
    diff_json   TEXT NOT NULL DEFAULT '{}',
    warn_json   TEXT NOT NULL DEFAULT '[]',
    proposed_at TEXT NOT NULL,
    decided_by  INTEGER REFERENCES users(id),
    decided_at  TEXT NOT NULL DEFAULT '',
    comment     TEXT NOT NULL DEFAULT '' CHECK (length(comment) <= 200)
);
CREATE UNIQUE INDEX idx_tax_matches_one_proposed ON tax_invoice_matches(invoice_id) WHERE status = 'proposed';
CREATE UNIQUE INDEX idx_tax_matches_one_confirmed ON tax_invoice_matches(invoice_id) WHERE status = 'confirmed';
CREATE INDEX idx_tax_matches_status ON tax_invoice_matches(status, proposed_at);

CREATE TRIGGER trg_tax_matches_status BEFORE UPDATE OF status ON tax_invoice_matches
WHEN NEW.status <> OLD.status AND NOT (
    (OLD.status = 'proposed'  AND NEW.status IN ('confirmed','dismissed')) OR
    (OLD.status = 'confirmed' AND NEW.status = 'unlinked'))
BEGIN SELECT RAISE(ABORT, '세금계산서 대조의 상태 전이가 올바르지 않습니다'); END;
CREATE TRIGGER trg_tax_matches_fixed BEFORE UPDATE ON tax_invoice_matches
WHEN NEW.id <> OLD.id OR NEW.invoice_id <> OLD.invoice_id OR NEW.proposed_at <> OLD.proposed_at
  OR (OLD.status <> 'proposed' AND (NEW.result <> OLD.result OR NEW.receipt_key <> OLD.receipt_key OR NEW.diff_json <> OLD.diff_json OR NEW.warn_json <> OLD.warn_json))
BEGIN SELECT RAISE(ABORT, '확정·기각된 대조 결과는 고칠 수 없습니다'); END;
CREATE TRIGGER trg_tax_matches_no_delete BEFORE DELETE ON tax_invoice_matches
BEGIN SELECT RAISE(ABORT, '세금계산서 대조 기록은 지울 수 없습니다'); END;

CREATE TABLE tax_invoice_match_receipts (
    match_id   INTEGER NOT NULL REFERENCES tax_invoice_matches(id),
    po_id      INTEGER NOT NULL REFERENCES purchase_orders(id),
    receipt_id INTEGER NOT NULL REFERENCES receipts(id),
    journal_id INTEGER REFERENCES journals(id),
    confirmed  INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0,1)),
    PRIMARY KEY (match_id, receipt_id)
);
CREATE UNIQUE INDEX idx_tax_match_receipts_confirmed ON tax_invoice_match_receipts(receipt_id) WHERE confirmed = 1;
CREATE INDEX idx_tax_match_receipts_receipt ON tax_invoice_match_receipts(receipt_id);
CREATE TRIGGER trg_tax_match_receipts_fixed BEFORE UPDATE ON tax_invoice_match_receipts
WHEN NEW.match_id <> OLD.match_id OR NEW.po_id <> OLD.po_id OR NEW.receipt_id <> OLD.receipt_id
BEGIN SELECT RAISE(ABORT, '세금계산서 대조의 연결 입고는 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_tax_match_receipts_no_delete BEFORE DELETE ON tax_invoice_match_receipts
WHEN (SELECT status FROM tax_invoice_matches WHERE id = OLD.match_id) <> 'proposed'
BEGIN SELECT RAISE(ABORT, '확정·기각된 대조의 연결 입고는 지울 수 없습니다'); END;
