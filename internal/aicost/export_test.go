package aicost

// MicroToKRWForTest 는 외부 시험 패키지가 환산 함수를 확인하는 통로다.
func MicroToKRWForTest(micro, fx int64) int64 { return microToKRW(micro, fx) }
