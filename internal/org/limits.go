// Package org 는 조직도·인사 발령(B20)이다.
//
// 정본은 추가 전용 이벤트 로그(org_events)이고, 구간 테이블(org_*_spans)은 로그를 처음부터 다시 계산(Fold)한 결과다.
// 조직·소속·직책을 쓰는 코드는 이 패키지에만 있어야 한다(소스 검사 테스트가 확인한다).
package org

// 업무 한도. 바꾸려면 이 파일의 값만 고친다(명세 15절 D1, D9).
const (
	// MaxConcurrent 는 주소속 외 겸직 최대 개수다.
	MaxConcurrent = 4
	// MaxHeadSeats 는 한 사람이 조직장일 수 있는 조직의 최대 개수다.
	MaxHeadSeats = 5
	// RetroConfirmDays 는 이 일수를 넘는 소급 발령에 확인 문구를 한 번 더 거치게 한다.
	RetroConfirmDays = 30
	// MaxFutureDays 는 예약 발령을 등록할 수 있는 오늘 이후 최대 일수다.
	MaxFutureDays = 365
)
