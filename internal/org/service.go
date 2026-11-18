package org

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// Options 는 Service 구성이다.
type Options struct {
	DB     *sql.DB
	Txs    contract.TxManager
	Audit  audit.Recorder
	Masker contract.Masker
	Now    func() time.Time // 테스트에서 시계를 주입한다. 비면 time.Now
	// Privacy 는 개인정보 원문 열람 기록기다(B41). 연락처·사번 원문이 응답에 실리는 경로가 응답 전에 부른다. nil 이면 기록 없이 통과한다(서버 조립은 항상 꽂는다).
	Privacy privacy.Noter
}

// Service 는 조직도·인사 발령 서비스다. 쓰기(Register·Cancel·Correct·RefreshProjection)와
// 조회(contract.OrgDirectory)를 모두 제공한다.
type Service struct {
	db     *sql.DB
	txs    contract.TxManager
	audit  audit.Recorder
	masker contract.Masker
	priv   privacy.Noter
	now    func() time.Time

	absenceHook AbsenceHook // 휴직 연동(휴가 연차 복원). 서버 조립이 꽂는다

	cacheMu sync.Mutex
	cached  *cache

	// CSV 일괄 반영(B22)의 미리보기 서명 키(서버가 시작할 때 무작위로 만든다, 저장하지 않는다)와 이미 반영된 미리보기 번호.
	importOnce sync.Once
	importKey  []byte
	importMu   sync.Mutex
	importUsed map[string]bool
}

type cache struct {
	maxID int64
	res   *Result
	snaps map[contract.Day]*Snap
}

// New 는 Service 를 만든다.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil || o.Masker == nil {
		return nil, errors.New("org: DB·Txs·Audit·Masker 가 필요합니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{db: o.DB, txs: o.Txs, audit: o.Audit, masker: o.Masker, priv: o.Privacy, now: o.Now}, nil
}

// Today 는 지금의 KST 날짜다.
func (s *Service) Today() contract.Day { return contract.DayOf(s.now()) }

// userError 는 사용자에게 그대로 보이는 업무 문구 오류다. errors.Is(ErrValidation) 이다(HTTP 400).
type userError struct{ msg string }

func (e userError) Error() string        { return e.msg }
func (e userError) Is(target error) bool { return target == contract.ErrValidation }

func verr(msg string) error { return userError{msg} }

// ConfirmError 는 30일 넘는 소급처럼 사용자가 한 번 더 확인해야 하는 경우다. errors.Is(ErrInvalidState) 이다(HTTP 409).
type ConfirmError struct {
	Days int
	Msg  string
}

func (e *ConfirmError) Error() string        { return e.Msg }
func (e *ConfirmError) Is(target error) bool { return target == contract.ErrInvalidState }

// current 는 이벤트 로그가 가리키는 최신 구간 계산 결과다. 저장된 구간 테이블을 읽어 프로세스 안에 두고,
// org_events 의 최대 번호가 바뀌면 다시 읽는다(다른 프로세스가 쓴 변경도 다음 호출에 반영된다).
func (s *Service) current(ctx context.Context, q Querier) (*Result, int64, error) {
	var maxID int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM org_events`).Scan(&maxID); err != nil {
		return nil, 0, err
	}
	s.cacheMu.Lock()
	if c := s.cached; c != nil && c.maxID == maxID {
		s.cacheMu.Unlock()
		return c.res, maxID, nil
	}
	s.cacheMu.Unlock()
	res, err := LoadSpans(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	s.cacheMu.Lock()
	s.cached = &cache{maxID: maxID, res: res, snaps: map[contract.Day]*Snap{}}
	s.cacheMu.Unlock()
	return res, maxID, nil
}

// snap 은 기준일의 색인이다(같은 상태에서는 기준일별로 재사용한다).
func (s *Service) snap(ctx context.Context, d contract.Day) (*Snap, error) {
	if d == "" {
		d = s.Today()
	}
	res, maxID, err := s.current(ctx, s.db)
	if err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cached == nil || s.cached.maxID != maxID {
		return NewSnap(res, d), nil
	}
	if sn, ok := s.cached.snaps[d]; ok {
		return sn, nil
	}
	sn := NewSnap(res, d)
	if len(s.cached.snaps) > 32 {
		s.cached.snaps = map[contract.Day]*Snap{}
	}
	s.cached.snaps[d] = sn
	return sn, nil
}

func (s *Service) invalidate() {
	s.cacheMu.Lock()
	s.cached = nil
	s.cacheMu.Unlock()
}

// systemActor 는 사용자 없는 시스템 작업의 감사 행위자다(번호 0).
// 감사 테이블의 경로 값은 web·mcp·llmgw 뿐이라(CHECK 제약) 경로는 웹으로 두고, 행위자 번호 0·역할 system 으로 시스템을 나타낸다.
// 감사 화면은 번호 0 을 사용자 "시스템"으로 보인다.
var systemActor = contract.Principal{Role: "system", Channel: contract.ChannelWeb}

// Invalidate 는 조직 투영 캐시를 버린다. 법인 서비스가 조직 구간을 다시 쓴 뒤(루트 연결) 부른다(B44).
func (s *Service) Invalidate() { s.invalidate() }

// SetPrivacy 는 서버 조립이 기록기를 나중에 꽂을 때 쓴다(org 서비스를 기록기보다 먼저 만드는 조립 순서용).
func (s *Service) SetPrivacy(n privacy.Noter) { s.priv = n }

// AbsenceHook 은 휴직 구간이 바뀐 사람에 대해 휴직 등록·복직·취소·정정과 같은 트랜잭션에서 불리는 연동 함수다(B50 16절: 승인된 휴가 기간의 연차 복원).
// 조직 패키지는 휴가 서비스를 모르므로 서버 조립이 꽂는다. 오류를 돌려주면 발령 전체가 롤백된다.
type AbsenceHook func(ctx context.Context, tx contract.Tx, actor contract.Principal, userID int64) error

// SetAbsenceHook 은 휴직 연동 훅을 꽂는다(nil 이면 아무것도 하지 않는다).
func (s *Service) SetAbsenceHook(h AbsenceHook) { s.absenceHook = h }

func isAbsenceKind(k string) bool { return k == KindAbsenceStart || k == KindAbsenceEnd }

// runAbsenceHook 은 사람 번호 중복을 없애 훅을 부른다.
func (s *Service) runAbsenceHook(ctx context.Context, tx contract.Tx, actor contract.Principal, users ...int64) error {
	if s.absenceHook == nil {
		return nil
	}
	seen := map[int64]bool{}
	for _, u := range users {
		if u == 0 || seen[u] {
			continue
		}
		seen[u] = true
		if err := s.absenceHook(ctx, tx, actor, u); err != nil {
			return err
		}
	}
	return nil
}
