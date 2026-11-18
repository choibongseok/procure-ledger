-- 0045: 계좌번호 암호화 저장(B32 D1 후속). (0042~0044 는 B33 이 쓴다.)
--  - bank_accounts.account_no_enc: 계좌번호 암호문(AES-256-GCM, 머리말 enc:v1:, 키는 환경변수 PL_ACCOUNT_SECRET_KEY 이고 DB 에 없다).
--    기존 account_no 칸은 화면에 쓰는 마스킹 값(끝 4자리)만 담는다. 기존 평문 행은 서버 기동 때 한 번 봉하고 account_no 를 마스킹 값으로 바꾼다(멱등).
--  - vendors.account_no·payment_requests.payee_account_enc 는 같은 칸에 암호문을 담는다(칸 이름은 그대로).
--  - 지급 요청의 받는 계좌 고정 트리거는 "평문 → 암호문" 한 번의 이관 갱신만 예외로 허용한다(그 밖의 변경은 여전히 거부).
ALTER TABLE bank_accounts ADD COLUMN account_no_enc TEXT NOT NULL DEFAULT '';

DROP TRIGGER trg_payment_requests_fixed;
CREATE TRIGGER trg_payment_requests_fixed BEFORE UPDATE ON payment_requests
WHEN OLD.status <> 'draft' AND (NEW.vendor_id <> OLD.vendor_id OR NEW.total <> OLD.total OR NEW.no <> OLD.no
  OR (NEW.payee_account_enc <> OLD.payee_account_enc AND NOT (OLD.payee_account_enc NOT LIKE 'enc:v1:%' AND NEW.payee_account_enc LIKE 'enc:v1:%')))
BEGIN SELECT RAISE(ABORT, '승인 요청 이후에는 지급 요청의 거래처·금액·받는 계좌를 바꿀 수 없습니다'); END;
