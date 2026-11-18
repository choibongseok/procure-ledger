package audit

// SkipChainLockForTest 는 체인 잠금을 건너뛰는 "틀린 구현"을 만든다(동시성 시험이 그것을 잡아내는지 보려는 용도).
func (s *Store) SkipChainLockForTest() { s.noLock = true }
