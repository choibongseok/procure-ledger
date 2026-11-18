-- 0078 (B44 2단계 platform): SQLite 0078_entity_immutable.sql 의 짝이다.

CREATE FUNCTION trg_journals_entity_immutable_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.entity_id <> OLD.entity_id THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '전표의 법인은 바꿀 수 없습니다';
  END IF;
  RETURN NEW;
END
$fn$;
CREATE TRIGGER trg_journals_entity_immutable BEFORE UPDATE OF entity_id ON journals FOR EACH ROW EXECUTE FUNCTION trg_journals_entity_immutable_fn();

CREATE FUNCTION trg_payment_requests_entity_immutable_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.entity_id <> OLD.entity_id THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '지급 요청의 법인은 바꿀 수 없습니다';
  END IF;
  RETURN NEW;
END
$fn$;
CREATE TRIGGER trg_payment_requests_entity_immutable BEFORE UPDATE OF entity_id ON payment_requests FOR EACH ROW EXECUTE FUNCTION trg_payment_requests_entity_immutable_fn();

CREATE FUNCTION trg_tax_invoices_entity_immutable_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.entity_id <> OLD.entity_id THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '세금계산서의 법인은 바꿀 수 없습니다';
  END IF;
  RETURN NEW;
END
$fn$;
CREATE TRIGGER trg_tax_invoices_entity_immutable BEFORE UPDATE OF entity_id ON tax_invoices FOR EACH ROW EXECUTE FUNCTION trg_tax_invoices_entity_immutable_fn();
