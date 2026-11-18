package docverify

import (
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// maskSpan 은 마스킹이 원문의 한 구간을 자리표시자로 바꾼 자리다(룬 오프셋, 반개구간).
type maskSpan struct {
	rawStart, rawEnd int
	mStart, mEnd     int
}

// maskView 는 원문과 모델이 본 마스킹 텍스트 사이의 대응이다.
// 모델의 인용은 마스킹 텍스트 기준이므로, 대조는 마스킹 텍스트에서 하고 위치는 원문 구간으로 되돌린다.
type maskView struct {
	raw, masked []rune
	spans       []maskSpan
}

// identityView 는 마스킹이 없는 대응이다(raw == masked).
func identityView(text string) *maskView {
	rs := []rune(text)
	return &maskView{raw: rs, masked: rs}
}

// maxMaskedRun 은 자리표시자 하나가 가릴 수 있다고 보는 원문 최대 길이(룬)다. 이메일이 가장 길다.
const maxMaskedRun = 320

// buildMaskView 는 m.MaskText(raw) 의 치환 구간 목록을 만든다. Masker 계약은 마스킹된 문자열만 주므로,
// 자리표시자를 마스킹 결과에서 알아내 그 사이의 글자를 원문과 맞춰 본다. 맞출 수 없으면 ok=false 다
// (호출한 쪽은 판단을 보류한다. 틀린 위치로 인용을 인정하지 않는다).
func buildMaskView(m contract.Masker, raw string) (*maskView, bool) {
	if m == nil {
		return identityView(raw), true
	}
	masked := m.MaskText(raw)
	if masked == raw {
		return identityView(raw), true
	}
	ph := m.MaskText("010-1234-5678")
	if ph == "" || ph == "010-1234-5678" || !strings.Contains(masked, ph) {
		return nil, false
	}
	lits := strings.Split(masked, ph)
	rs := []rune(raw)
	v := &maskView{raw: rs, masked: []rune(masked)}
	first := []rune(lits[0])
	if !hasPrefix(rs, 0, first) {
		return nil, false
	}
	pos, mpos := len(first), len(first)
	phLen := len([]rune(ph))
	for k := 1; k < len(lits); k++ {
		lit := []rune(lits[k])
		last := k == len(lits)-1
		end := -1
		for e := pos + 1; e <= len(rs) && e-pos <= maxMaskedRun; e++ {
			if last {
				if e+len(lit) != len(rs) {
					continue
				}
			} else if !hasPrefix(rs, e, lit) {
				continue
			}
			if !hasPrefix(rs, e, lit) || m.MaskText(string(rs[pos:e])) != ph {
				continue
			}
			end = e
			break
		}
		if end < 0 {
			return nil, false
		}
		v.spans = append(v.spans, maskSpan{rawStart: pos, rawEnd: end, mStart: mpos, mEnd: mpos + phLen})
		pos = end + len(lit)
		mpos += phLen + len(lit)
	}
	return v, true
}

func hasPrefix(rs []rune, at int, lit []rune) bool {
	if at+len(lit) > len(rs) {
		return false
	}
	for i, r := range lit {
		if rs[at+i] != r {
			return false
		}
	}
	return true
}

// toRaw 는 마스킹 텍스트의 구간 [s,e) 를 원문 구간으로 되돌린다. 경계가 자리표시자 안에 걸리면
// 그 자리표시자가 가린 원문 전체로 넓힌다(일부만 잘라 내지 않는다).
func (v *maskView) toRaw(s, e int) (int, int) {
	return v.pos(s, false), v.pos(e, true)
}

func (v *maskView) pos(m int, isEnd bool) int {
	shift := 0
	for _, sp := range v.spans {
		switch {
		case m >= sp.mEnd:
			shift += (sp.rawEnd - sp.rawStart) - (sp.mEnd - sp.mStart)
		case m > sp.mStart:
			if isEnd {
				return sp.rawEnd
			}
			return sp.rawStart
		default:
			return m + shift
		}
	}
	return m + shift
}
