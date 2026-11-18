package service

// JournalTransitionForTest 는 외부 시험 패키지가 상태 전이 표를 직접 확인하게 한다.
func JournalTransitionForTest(from, ev string) (string, error) {
	return journalTransition(from, journalEvent(ev))
}

// CardTransitionForTest 는 외부 시험 패키지가 카드 내역 상태 전이 표를 직접 확인하게 한다.
func CardTransitionForTest(from, ev string) (string, error) {
	return cardTransition(from, cardEvent(ev))
}

// PaymentTransitionForTest 는 외부 시험 패키지가 지급 건 상태 전이 표를 직접 확인하게 한다.
func PaymentTransitionForTest(from, ev string) (string, error) {
	return paymentTransition(from, paymentEvent(ev))
}
