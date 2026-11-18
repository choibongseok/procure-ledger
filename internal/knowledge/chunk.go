// Package knowledge 는 사내 지식 검색(B47)의 색인·워커·검색 서비스다. 색인에는 권한 정보가 없고, 열람 판정은 조회 때 결재 문서 열람 조건(ViewScope)으로 한다.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 조각 크기(명세 2절·5.1절): 800자 안팎, 앞 조각과 100자 겹침. 한 지식 문서의 텍스트는 200KB, 조각은 500개까지.
const (
	ChunkRunes     = 800
	ChunkOverlap   = 100
	MaxTextBytes   = 200 << 10
	MaxChunksOfDoc = 500
)

// StateTooLong 은 한도를 넘어 앞부분만 색인했다는 표시다(지식 문서의 extract_state).
const (
	StateReady   = "ready"
	StateTooLong = "too_long"
)

// SplitLines 는 "소제목: 글" 줄들을 조각으로 자른다. 짧은 줄은 한 조각에 모아 쓰고(조각의 소제목은 첫 줄의 것), 긴 줄은 겹치게 잘라 나눈다.
// 위치 표지는 locatorKind·locator 로 모든 조각에 같다(결재 문서 본문은 body). 한도를 넘으면 앞부분만 쓰고 tooLong 을 참으로 돌려준다.
func SplitLines(lines []repository.KLine, locatorKind, locator string) (chunks []repository.KChunk, tooLong bool) {
	total := 0
	var cur strings.Builder
	curHeading := ""
	curRunes := 0
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, repository.KChunk{LocatorKind: locatorKind, Locator: locator, Heading: curHeading, Text: strings.TrimSpace(cur.String())})
		}
		cur.Reset()
		curHeading, curRunes = "", 0
	}
	add := func(heading, text string) bool {
		if len(chunks) >= MaxChunksOfDoc {
			return false
		}
		if curRunes > 0 && curRunes+1+utf8.RuneCountInString(text) > ChunkRunes {
			flush()
			if len(chunks) >= MaxChunksOfDoc {
				return false
			}
		}
		if curRunes == 0 {
			curHeading = heading
		} else {
			cur.WriteString("\n")
			curRunes++
		}
		cur.WriteString(text)
		curRunes += utf8.RuneCountInString(text)
		return true
	}
	for _, l := range lines {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		line := text
		if h := strings.TrimSpace(l.Heading); h != "" {
			line = h + ": " + text
		}
		total += len(line)
		if total > MaxTextBytes {
			tooLong = true
			break
		}
		r := []rune(line)
		if len(r) <= ChunkRunes {
			if !add(l.Heading, line) {
				tooLong = true
				break
			}
			continue
		}
		// 긴 줄: 현재 조각을 닫고 겹치게 자른다.
		flush()
		for start := 0; start < len(r); {
			end := start + ChunkRunes
			if end > len(r) {
				end = len(r)
			}
			if len(chunks) >= MaxChunksOfDoc {
				tooLong = true
				break
			}
			chunks = append(chunks, repository.KChunk{LocatorKind: locatorKind, Locator: locator, Heading: l.Heading, Text: string(r[start:end])})
			if end == len(r) {
				break
			}
			start = end - ChunkOverlap
		}
		if tooLong {
			break
		}
	}
	flush()
	return chunks, tooLong
}

// ContentHash 는 조각 전체의 해시다(같으면 재색인을 건너뛴다). 제목·번호가 바뀌어도 다시 색인되도록 머리말을 함께 넣는다.
func ContentHash(head string, chunks []repository.KChunk) string {
	h := sha256.New()
	h.Write([]byte(head))
	for _, c := range chunks {
		h.Write([]byte{0})
		h.Write([]byte(c.LocatorKind + "|" + c.Locator + "|" + c.Heading + "|" + c.Text))
	}
	return hex.EncodeToString(h.Sum(nil))
}
