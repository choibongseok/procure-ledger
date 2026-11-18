package domain

import (
	"context"
	"time"
)

// B29 ④: 결재 독촉( 5절). 기안자만, 진행 중 문서의 단계 시작 뒤 24시간이 지난 단계에 대해서만, 같은 단계는 KST 하루 1회.

// NudgeRecord 는 독촉 기록 한 건이다(기안자만 본다).
type NudgeRecord struct {
	At             time.Time
	Day            string
	StepTitle      string
	Recipients     int
	RecipientNames []string
}

// NudgeStatus 는 문서 상세의 독촉 영역이 쓰는 값이다. Delayed 가 거짓이면 독촉할 단계가 없다(단추를 그리지 않는다).
type NudgeStatus struct {
	Delayed        bool
	StepTitle      string
	WaitingMinutes int64
	NoApprover     bool       // 결재자가 없어 독촉할 수 없다
	Holiday        bool       // 오늘이 휴일(주말·공휴일)이다: 독촉은 보낼 수 있지만 안내만 보인다(B34)
	DoneToday      *time.Time // 오늘 이미 독촉한 시각(있으면 단추 비활성)
	Records        []NudgeRecord
}

// NudgeResult 는 독촉한 결과다.
type NudgeResult struct {
	StepTitle      string
	Recipients     int
	WaitingMinutes int64
}

// NudgeService 는 결재 독촉이다(권한 draft:create, 서비스가 기안자인지 판정).
type NudgeService interface {
	Nudge(ctx context.Context, docID int64) (NudgeResult, error)
	Status(ctx context.Context, docID int64) (NudgeStatus, error)
}
