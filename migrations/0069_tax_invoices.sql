-- 0069 (B43 1단계, 4절): 전자세금계산서 발행·수신 공통 모델.
--  - tax_invoices: 매출(sale: 우리가 발행)·매입(purchase: 우리가 수신). 매출은 draft → issued → sent → accepted(실패 failed, 취소 canceled),
--    매입은 승인번호가 있는 accepted 로 생긴다(수기 등록 건만 연결 전에 canceled 가능). 상태 전이는 서비스 전이 표와 같은 모양의 트리거가 한 번 더 막는다.
--    승인번호는 부분 유일 색인으로 전체에서 유일하다(중복 거부의 마지막 보루). 접수 뒤(draft 가 아닌 뒤) 금액·작성일·당사자 스냅샷은 고칠 수 없고, 삭제는 어떤 상태에서도 없다.
--  - tax_invoice_lines: 품목 줄(draft 일 때만 고친다).
--  - tax_invoice_transmissions: 대행사 호출 기록(추가 전용). 본문·응답 본문·주소·키는 담지 않는다(결과 코드만).
CREATE TABLE tax_invoices (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    seq_no              INTEGER NOT NULL UNIQUE,
    no                  TEXT NOT NULL UNIQUE,
    direction           TEXT NOT NULL CHECK (direction IN ('sale','purchase')),
    tax_type            TEXT NOT NULL CHECK (tax_type IN ('taxable','zero','exempt')),
    status              TEXT NOT NULL CHECK (status IN ('draft','issued','sent','accepted','failed','canceled')),
    approval_no         TEXT NOT NULL DEFAULT '' CHECK (approval_no = '' OR length(approval_no) = 24),
    issue_date          TEXT NOT NULL CHECK (issue_date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    supplier_biz_no     TEXT NOT NULL DEFAULT '',
    supplier_branch_no  TEXT NOT NULL DEFAULT '',
    supplier_name       TEXT NOT NULL DEFAULT '',
    supplier_rep        TEXT NOT NULL DEFAULT '',
    supplier_addr       TEXT NOT NULL DEFAULT '',
    supplier_email      TEXT NOT NULL DEFAULT '',
    buyer_biz_no        TEXT NOT NULL DEFAULT '',
    buyer_branch_no     TEXT NOT NULL DEFAULT '',
    buyer_name          TEXT NOT NULL DEFAULT '',
    buyer_rep           TEXT NOT NULL DEFAULT '',
    buyer_addr          TEXT NOT NULL DEFAULT '',
    buyer_email         TEXT NOT NULL DEFAULT '',
    supply_total        INTEGER NOT NULL DEFAULT 0,
    tax_total           INTEGER NOT NULL DEFAULT 0,
    total               INTEGER NOT NULL DEFAULT 0,
    amend_of_id         INTEGER REFERENCES tax_invoices(id),
    amend_reason        TEXT NOT NULL DEFAULT '' CHECK (amend_reason IN ('','entry_error','amount_change','return','contract_cancel','lc_after','double_issue')),
    charge_type         TEXT NOT NULL DEFAULT 'claim' CHECK (charge_type IN ('receipt','claim')),
    remark              TEXT NOT NULL DEFAULT '' CHECK (length(remark) <= 200),
    source              TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','asp')),
    provider_ref        TEXT NOT NULL DEFAULT '',
    attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    resends             INTEGER NOT NULL DEFAULT 0 CHECK (resends >= 0),
    next_try_at         TEXT NOT NULL DEFAULT '',
    claimed_at          TEXT NOT NULL DEFAULT '',
    last_code           TEXT NOT NULL DEFAULT '',
    created_by          INTEGER NOT NULL REFERENCES users(id),
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    issued_by           INTEGER REFERENCES users(id),
    issued_at           TEXT NOT NULL DEFAULT '',
    sent_at             TEXT NOT NULL DEFAULT '',
    accepted_at         TEXT NOT NULL DEFAULT '',
    canceled_by         INTEGER REFERENCES users(id),
    canceled_at         TEXT NOT NULL DEFAULT '',
    cancel_reason       TEXT NOT NULL DEFAULT '',
    CHECK (supply_total + tax_total = total),
    CHECK (amend_reason <> '' OR (supply_total >= 0 AND tax_total >= 0))
);
CREATE UNIQUE INDEX idx_tax_invoices_approval ON tax_invoices(approval_no) WHERE approval_no <> '';
CREATE INDEX idx_tax_invoices_list ON tax_invoices(direction, issue_date);
CREATE INDEX idx_tax_invoices_due ON tax_invoices(status, next_try_at);
CREATE INDEX idx_tax_invoices_supplier ON tax_invoices(supplier_biz_no, issue_date);
CREATE INDEX idx_tax_invoices_buyer ON tax_invoices(buyer_biz_no, issue_date);
CREATE INDEX idx_tax_invoices_amend ON tax_invoices(amend_of_id);

CREATE TRIGGER trg_tax_invoices_status BEFORE UPDATE OF status ON tax_invoices
WHEN NEW.status <> OLD.status AND NOT (
    (OLD.direction = 'sale' AND (
        (OLD.status = 'draft'  AND NEW.status IN ('issued','canceled')) OR
        (OLD.status = 'issued' AND NEW.status IN ('sent','failed')) OR
        (OLD.status = 'sent'   AND NEW.status IN ('accepted','failed')) OR
        (OLD.status = 'failed' AND NEW.status IN ('issued','canceled'))))
    OR (OLD.direction = 'purchase' AND OLD.status = 'accepted' AND NEW.status = 'canceled'))
BEGIN SELECT RAISE(ABORT, '세금계산서의 상태 전이가 올바르지 않습니다'); END;

CREATE TRIGGER trg_tax_invoices_fixed BEFORE UPDATE ON tax_invoices
WHEN NEW.id <> OLD.id OR NEW.seq_no <> OLD.seq_no OR NEW.no <> OLD.no OR NEW.direction <> OLD.direction OR NEW.tax_type <> OLD.tax_type
  OR NEW.created_by <> OLD.created_by OR NEW.created_at <> OLD.created_at
  OR (OLD.approval_no <> '' AND NEW.approval_no <> OLD.approval_no)
  OR (OLD.status <> 'draft' AND (NEW.issue_date <> OLD.issue_date OR NEW.supply_total <> OLD.supply_total OR NEW.tax_total <> OLD.tax_total OR NEW.total <> OLD.total
      OR NEW.supplier_biz_no <> OLD.supplier_biz_no OR NEW.supplier_name <> OLD.supplier_name OR NEW.buyer_biz_no <> OLD.buyer_biz_no OR NEW.buyer_name <> OLD.buyer_name
      OR NEW.amend_of_id IS NOT OLD.amend_of_id OR NEW.amend_reason <> OLD.amend_reason))
BEGIN SELECT RAISE(ABORT, '접수된 세금계산서의 금액·작성일·당사자·승인번호는 고칠 수 없습니다'); END;

CREATE TRIGGER trg_tax_invoices_no_delete BEFORE DELETE ON tax_invoices
BEGIN SELECT RAISE(ABORT, '세금계산서는 지울 수 없습니다(취소만 됩니다)'); END;

CREATE TABLE tax_invoice_lines (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    invoice_id  INTEGER NOT NULL REFERENCES tax_invoices(id),
    seq         INTEGER NOT NULL CHECK (seq >= 1),
    day         TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    spec        TEXT NOT NULL DEFAULT '' CHECK (length(spec) <= 50),
    qty         INTEGER NOT NULL DEFAULT 0,
    unit_price  INTEGER NOT NULL DEFAULT 0,
    supply      INTEGER NOT NULL DEFAULT 0,
    tax         INTEGER NOT NULL DEFAULT 0,
    remark      TEXT NOT NULL DEFAULT '' CHECK (length(remark) <= 100),
    UNIQUE (invoice_id, seq)
);
CREATE TRIGGER trg_tax_invoice_lines_update BEFORE UPDATE ON tax_invoice_lines
WHEN (SELECT status FROM tax_invoices WHERE id = OLD.invoice_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '작성 중이 아닌 세금계산서의 품목 줄은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_tax_invoice_lines_delete BEFORE DELETE ON tax_invoice_lines
WHEN (SELECT status FROM tax_invoices WHERE id = OLD.invoice_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, '작성 중이 아닌 세금계산서의 품목 줄은 지울 수 없습니다'); END;

CREATE TABLE tax_invoice_transmissions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    invoice_id   INTEGER NOT NULL REFERENCES tax_invoices(id),
    attempt_no   INTEGER NOT NULL CHECK (attempt_no >= 1),
    at           TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('issue','status','sync')),
    result_code  TEXT NOT NULL,
    http_status  INTEGER NOT NULL DEFAULT 0,
    provider_ref TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_tax_invoice_transmissions_invoice ON tax_invoice_transmissions(invoice_id, id);
CREATE TRIGGER trg_tax_invoice_transmissions_no_update BEFORE UPDATE ON tax_invoice_transmissions
BEGIN SELECT RAISE(ABORT, '세금계산서 전송 기록은 고칠 수 없습니다'); END;
CREATE TRIGGER trg_tax_invoice_transmissions_no_delete BEFORE DELETE ON tax_invoice_transmissions
BEGIN SELECT RAISE(ABORT, '세금계산서 전송 기록은 지울 수 없습니다'); END;
