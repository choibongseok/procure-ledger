-- 0078 (B44 2단계 platform, 명세 5절 변경 불가 불변식): 전표·지급 요청·세금계산서의 법인(entity_id)은 만든 뒤 바꿀 수 없다. 다른 법인으로 옮기는 일은 없다(취소·재작성으로만).
CREATE TRIGGER trg_journals_entity_immutable BEFORE UPDATE OF entity_id ON journals
WHEN NEW.entity_id <> OLD.entity_id
BEGIN SELECT RAISE(ABORT, '전표의 법인은 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_payment_requests_entity_immutable BEFORE UPDATE OF entity_id ON payment_requests
WHEN NEW.entity_id <> OLD.entity_id
BEGIN SELECT RAISE(ABORT, '지급 요청의 법인은 바꿀 수 없습니다'); END;
CREATE TRIGGER trg_tax_invoices_entity_immutable BEFORE UPDATE OF entity_id ON tax_invoices
WHEN NEW.entity_id <> OLD.entity_id
BEGIN SELECT RAISE(ABORT, '세금계산서의 법인은 바꿀 수 없습니다'); END;
