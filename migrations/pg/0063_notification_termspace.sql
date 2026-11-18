-- 0063 (B36 후속): 알림 웹훅 대상 종류에 'termspace' 추가. SQLite 0063_notification_termspace.sql 의 짝이다.
-- PG 는 제약만 교체한다(표를 다시 만들지 않는다). 기존 행·색인·트리거에 영향이 없다.
ALTER TABLE notification_targets DROP CONSTRAINT notification_targets_type_check;
ALTER TABLE notification_targets ADD CONSTRAINT notification_targets_type_check CHECK (type IN ('slack','teams','kakaowork','termspace'));
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check CHECK (channel IN ('email','slack','teams','kakaowork','termspace'));
