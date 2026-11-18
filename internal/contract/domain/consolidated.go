package domain

import "context"

// B44 3단계 연결 재무 집계( 11.2). 합산만 한다: 내부 거래 상계·조정 분개·지분법은 없고 값은 전표 원본에서 매번 계산한다.
// 승인된 전표만 센다. 호출자가 consolidated:read 를 가진 법인(그룹 범위면 전체)만 합산하고, 현재 법인 선택과 무관하다.

// ConsolidatedFilter 는 집계 기간이다. Unit 은 month(Month), quarter(Year·Quarter), year(Year) 이고 기준은 전표의 전기일이다. 비면 이번 달이다.
type ConsolidatedFilter struct {
	Unit    string
	Month   string // YYYY-MM
	Year    int
	Quarter int // 1~4
}

// ConsolidatedEntity 는 표의 열 하나(법인)다. Closed 는 기간의 모든 달이 그 법인에서 마감된 경우다(아니면 화면이 "마감 전" 배지를 붙인다).
type ConsolidatedEntity struct {
	ID           int64
	Code, Name   string
	Closed       bool
	ClosedMonths int
	Months       int
}

// ConsolidatedAccount 는 표 1 의 한 계정 줄이다. Amounts 는 Consolidated.Entities 와 같은 순서이고 계정의 정상 방향(자산·비용은 차변 − 대변, 나머지는 대변 − 차변)이다.
// 자산·부채·자본은 기간 끝까지의 누계 잔액, 수익·비용은 기간 안의 발생액이다.
type ConsolidatedAccount struct {
	Code, Name string
	Kind       string // asset | liability | equity | revenue | expense
	Amounts    []int64
	Total      int64
}

// ConsolidatedSummaryRow 는 표 2 의 한 줄(5대 분류 또는 순손익)이다. 합계는 단순 합이다.
type ConsolidatedSummaryRow struct {
	Kind    string // asset | liability | equity | revenue | expense | net
	Label   string
	Amounts []int64
	Total   int64
}

// ConsolidatedPairLine 은 표 3 의 한 줄이다: 우리 법인이 연결 거래처를 상대로 기록한 전표 줄의 계정별 합(기간 안).
type ConsolidatedPairLine struct {
	Code, Name    string
	Debit, Credit int64
}

// ConsolidatedPair 는 표 3 의 법인 쌍(우리 법인 → 상대 법인)이다. 합계에 반영하지도 상계하지도 않는 참고 표다.
// Payable 은 From 이 To 를 거래처로 기록한 부채(2 계열) 순액, Receivable 은 To 가 From 을 거래처로 기록한 자산(1 계열) 순액이고, Gap 은 둘의 차이(표시만)다.
type ConsolidatedPair struct {
	FromID, ToID        int64
	FromCode, FromName  string
	ToCode, ToName      string
	Lines               []ConsolidatedPairLine
	Payable, Receivable int64
	Gap                 int64
	CounterpartRecorded bool // 상대 법인이 이쪽을 거래처로 기록한 줄이 있는가
}

// Consolidated 는 연결 집계 결과다.
type Consolidated struct {
	From, To     string
	Entities     []ConsolidatedEntity
	Trial        []ConsolidatedAccount
	Summary      []ConsolidatedSummaryRow
	Intercompany []ConsolidatedPair
	// PartialScope 는 호출자가 볼 수 있는 법인이 설치의 활성 법인 전체보다 적을 때 참이다(합계가 일부 법인만의 합임을 알린다).
	PartialScope bool
	// Pending 은 기간 안의 승인 전(임시·승인 요청) 전표 수다("집계에서 빠진 승인 대기 N건" 안내).
	Pending int
}

// ConsolidatedService 는 연결 재무 집계다. 조회 consolidated:read(재무·감사·관리자).
type ConsolidatedService interface {
	Summary(ctx context.Context, f ConsolidatedFilter) (Consolidated, error)
}
