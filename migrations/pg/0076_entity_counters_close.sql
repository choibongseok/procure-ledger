-- 0076 (B44 2단계 platform): SQLite 0076_entity_counters_close.sql 의 짝이다. PG 는 표를 다시 만들지 않고 열 추가와 기본 키 교체로 한다.
ALTER TABLE inventory_counters ADD COLUMN entity_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE inventory_counters DROP CONSTRAINT inventory_counters_pkey;
ALTER TABLE inventory_counters ADD PRIMARY KEY (entity_id, prefix, year);

ALTER TABLE document_counters ADD COLUMN entity_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE document_counters DROP CONSTRAINT document_counters_pkey;
ALTER TABLE document_counters ADD PRIMARY KEY (entity_id, form_id, scope_unit_id, year);

ALTER TABLE month_closes ADD COLUMN entity_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE month_closes DROP CONSTRAINT month_closes_pkey;
ALTER TABLE month_closes ADD PRIMARY KEY (entity_id, month);
ALTER TABLE month_close_events ADD COLUMN entity_id BIGINT NOT NULL DEFAULT 1;
ALTER TABLE month_close_lines ADD COLUMN entity_id BIGINT NOT NULL DEFAULT 1;

CREATE OR REPLACE FUNCTION trg_month_closes_update_guard_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF NOT (
  NEW.entity_id = OLD.entity_id AND NEW.month = OLD.month AND (
    (OLD.status = 'open' AND NEW.status = 'closed' AND NEW.version = OLD.version + 1 AND NEW.closed_at IS NOT NULL)
    OR
    (OLD.status = 'closed' AND NEW.status = 'open' AND NEW.version = OLD.version AND NEW.closed_by IS NOT DISTINCT FROM OLD.closed_by AND NEW.closed_at IS NOT DISTINCT FROM OLD.closed_at AND NEW.released_at IS NOT NULL)
  )
) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '월 마감 상태는 마감·해제 절차로만 바꿀 수 있습니다';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_journals_month_open_ins_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(NEW.posting_date, 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = NEW.entity_id AND status = 'closed' AND month = substr(NEW.posting_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_journals_month_open_upd_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(NEW.posting_date, 1, 7), 0));
  IF (NEW.status IN ('requested','approved') OR NEW.posting_date <> OLD.posting_date)
 AND EXISTS (SELECT 1 FROM month_closes WHERE entity_id = NEW.entity_id AND status = 'closed' AND month = substr(NEW.posting_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_ledger_month_open_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(COALESCE(NEW.book_date, to_char((NEW.created_at)::timestamp AT TIME ZONE 'UTC' AT TIME ZONE 'Asia/Seoul', 'YYYY-MM-DD')), 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT entity_id FROM warehouses WHERE id = NEW.warehouse_id), 1) AND status = 'closed' AND month = substr(COALESCE(NEW.book_date, to_char((NEW.created_at)::timestamp AT TIME ZONE 'UTC' AT TIME ZONE 'Asia/Seoul', 'YYYY-MM-DD')), 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_issue_month_open_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(NEW.issue_date, 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT entity_id FROM warehouses WHERE id = NEW.warehouse_id), 1) AND status = 'closed' AND month = substr(NEW.issue_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_count_month_open_ins_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(NEW.base_date, 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT entity_id FROM warehouses WHERE id = NEW.warehouse_id), 1) AND status = 'closed' AND month = substr(NEW.base_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_count_month_open_upd_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(NEW.base_date, 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT entity_id FROM warehouses WHERE id = NEW.warehouse_id), 1) AND status = 'closed' AND month = substr(NEW.base_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_valuation_month_open_upd_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(OLD.book_date, 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT w.entity_id FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id WHERE l.id = OLD.ledger_id), 1) AND status = 'closed' AND month = substr(OLD.book_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN NEW;
END
$fn$;

CREATE OR REPLACE FUNCTION trg_valuation_month_open_del_fn() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  PERFORM pg_advisory_xact_lock_shared(hashtextextended('procure-ledger:month:' || substr(OLD.book_date, 1, 7), 0));
  IF EXISTS (SELECT 1 FROM month_closes WHERE entity_id = COALESCE((SELECT w.entity_id FROM stock_ledger l JOIN warehouses w ON w.id = l.warehouse_id WHERE l.id = OLD.ledger_id), 1) AND status = 'closed' AND month = substr(OLD.book_date, 1, 7)) THEN
    RAISE EXCEPTION USING ERRCODE = 'P0001', MESSAGE = '마감된 월입니다. 재무팀에 마감 해제를 요청하세요.';
  END IF;
  RETURN OLD;
END
$fn$;
