package evals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// BaselineVersion 은 기준선 파일 형식 버전이다.
const BaselineVersion = 1

// CaseState 는 케이스 한 건의 통과 여부다. 기준선과 이번 실행을 같은 모양으로 비교한다.
type CaseState struct {
	Suite string `json:"suite"` // docs | tools
	ID    string `json:"id"`
	Pass  bool   `json:"pass"`
}

// Key 는 "구분/케이스" 형태의 식별자다.
func (c CaseState) Key() string { return c.Suite + "/" + c.ID }

// Baseline 은 저장소에 커밋하는 기준선이다(evals/baseline.json). 회귀 감지의 기준이다.
// CI 와 로컬이 같은 파일과 비교해야 하므로 바이너리에도 내장한다. 갱신은 명시적 플래그로만 한다.
type Baseline struct {
	Version  int         `json:"version"`
	Provider string      `json:"provider"`
	Cases    []CaseState `json:"cases"`
}

// Sort 는 파일이 항상 같은 순서로 저장되게 케이스를 정렬한다.
func (b *Baseline) Sort() {
	sort.Slice(b.Cases, func(i, j int) bool { return b.Cases[i].Key() < b.Cases[j].Key() })
}

// Marshal 은 사람이 diff 로 읽을 수 있는 형태(들여쓰기, 끝 줄바꿈)로 직렬화한다.
func (b Baseline) Marshal() ([]byte, error) {
	b.Sort()
	out, err := json.MarshalIndent(b, "", " ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func parseBaseline(data []byte, src string) (Baseline, error) {
	var b Baseline
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return b, fmt.Errorf("기준선 %s: %w", src, err)
	}
	if b.Version != BaselineVersion {
		return b, fmt.Errorf("기준선 %s: 지원하지 않는 버전 %d", src, b.Version)
	}
	seen := map[string]bool{}
	for _, c := range b.Cases {
		if c.Suite == "" || c.ID == "" || seen[c.Key()] {
			return b, fmt.Errorf("기준선 %s: 케이스 식별자가 비었거나 중복입니다(%q)", src, c.Key())
		}
		seen[c.Key()] = true
	}
	return b, nil
}

// LoadBaseline 은 바이너리에 내장된(= 커밋된) 기준선을 읽는다.
func LoadBaseline() (Baseline, error) {
	data, err := files.ReadFile("baseline.json")
	if err != nil {
		return Baseline{}, err
	}
	return parseBaseline(data, "baseline.json")
}

// ReadBaseline 은 파일에서 기준선을 읽는다.
func ReadBaseline(path string) (Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Baseline{}, err
	}
	return parseBaseline(data, path)
}

// Diff 는 기준선과 이번 실행의 차이다. 값은 "구분/케이스" 식별자(정렬됨)다.
type Diff struct {
	NewFailures []string // 기준선에서 통과했거나 기준선에 없던 케이스가 이번에 실패
	NewPasses   []string // 기준선에서 실패했는데 이번에 통과
	Removed     []string // 기준선에 있는데 이번 실행에 없음(경고용, 실패로 세지 않는다)
}

// Compare 는 기준선과 현재 결과를 비교한다. suites 는 이번에 실행한 구분이며, 실행하지 않은 구분의 케이스는
// "사라짐"으로 세지 않는다(--only).
func Compare(base Baseline, cur []CaseState, suites map[string]bool) Diff {
	prev := map[string]bool{}
	for _, c := range base.Cases {
		prev[c.Key()] = c.Pass
	}
	var d Diff
	seen := map[string]bool{}
	for _, c := range cur {
		seen[c.Key()] = true
		was, known := prev[c.Key()]
		switch {
		case !c.Pass && (!known || was):
			d.NewFailures = append(d.NewFailures, c.Key())
		case c.Pass && known && !was:
			d.NewPasses = append(d.NewPasses, c.Key())
		}
	}
	for _, c := range base.Cases {
		if suites[c.Suite] && !seen[c.Key()] {
			d.Removed = append(d.Removed, c.Key())
		}
	}
	sort.Strings(d.NewFailures)
	sort.Strings(d.NewPasses)
	sort.Strings(d.Removed)
	return d
}
