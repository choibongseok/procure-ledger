package org

import "unicode/utf8"

// 휴직 구분과 근로기준법 관련 값(B50 8절·5.1). 한 곳에서만 읽는다.
//
// 운영 전 노무 검토 필요: 아래 "출근 간주" 표시는 근로기준법 제60조 제6항(업무상 부상·질병 휴업, 출산전후휴가, 육아휴직 기간은 출근한 것으로 본다)을
// 공개 법령 수준으로 요약한 값이다. 이 모듈의 "질병" 휴직은 업무상 재해인지 일반 질병인지 구분하지 않으므로 보수적으로 출근 간주에서 뺐다.
// 검토 결과로 값이 바뀌면 이 표만 고친다(연차 발생 계산이 AbsenceCountsAsAttended 로 읽는다).

// 휴직 구분 값(org_person_spans.absence_kind 의 CHECK 열거와 같다).
const (
	AbsenceIllness    = "illness"
	AbsenceChildcare  = "childcare"
	AbsenceFamilyCare = "family_care"
	AbsencePersonal   = "personal"
	AbsenceOther      = "other"
)

// MaxAbsenceReasonRunes 는 휴직 사유 입력의 글자 수 상한이다(자유 입력은 선택이고 최소 수집, B50 11절).
const MaxAbsenceReasonRunes = 200

type absenceInfo struct {
	label    string
	attended bool // 연차 출근율 산정에서 출근한 것으로 보는가(노무 검토 필요)
}

var absenceKinds = map[string]absenceInfo{
	AbsenceIllness:    {label: "질병"},
	AbsenceChildcare:  {label: "육아", attended: true},
	AbsenceFamilyCare: {label: "가족 돌봄"},
	AbsencePersonal:   {label: "개인"},
	AbsenceOther:      {label: "기타"},
}

// absenceOrder 는 화면 선택지 순서다.
var absenceOrder = []string{AbsenceIllness, AbsenceChildcare, AbsenceFamilyCare, AbsencePersonal, AbsenceOther}

// AbsenceKindOption 은 휴직 구분 선택지 한 줄이다.
type AbsenceKindOption struct {
	Key   string
	Label string
}

// AbsenceKinds 는 휴직 구분 선택지를 화면 순서대로 돌려준다.
func AbsenceKinds() []AbsenceKindOption {
	out := make([]AbsenceKindOption, 0, len(absenceOrder))
	for _, k := range absenceOrder {
		out = append(out, AbsenceKindOption{Key: k, Label: absenceKinds[k].label})
	}
	return out
}

// KnownAbsenceKind 는 휴직 구분 값이 올바른지다.
func KnownAbsenceKind(k string) bool { _, ok := absenceKinds[k]; return ok }

// AbsenceLabel 은 휴직 구분의 한국어 이름이다. 모르는 값은 "기타"로 보인다.
func AbsenceLabel(k string) string {
	if i, ok := absenceKinds[k]; ok {
		return i.label
	}
	return "기타"
}

// AbsenceCountsAsAttended 는 이 휴직 구분의 기간을 연차 출근율 계산에서 출근으로 보는지다. 운영 전 노무 검토 필요.
func AbsenceCountsAsAttended(k string) bool { return absenceKinds[k].attended }

func absenceReasonTooLong(s string) bool { return utf8.RuneCountInString(s) > MaxAbsenceReasonRunes }
