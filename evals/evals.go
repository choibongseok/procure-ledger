// Package evals 는 평가 러너의 골든셋과 합격 기준이다.
//
// 합격 기준은 사람 말이 아니라 이 패키지의 상수와 JSON 이다. cmd/eval 이 이를 읽어 케이스별로 채점하고,
// 합격률이 임계치 미만이거나 필수(critical) 케이스가 하나라도 틀리면 0 이 아닌 코드로 끝난다.
//
// 파일
//
//	docverify.json  문서 판정 골든셋(28건): 발주·입고 명세(pos)와 케이스(기대 판정 + 기대 근거 항목)
//	tools.json      MCP 도구 선택 케이스(18건): 역할, 요청 문장, 기대 도구·인자·권한 결과
//	quotes.json     견적서 추출 케이스(B25): 거래처 마스터(vendors)와 케이스(기대 상태·값·경고)
//	journals.json   자동 매입 전표 규칙 케이스(B31): 발주·입고·증빙·확정 결과와 기대 전표·전표 대기
//	knowledge.json  사내 지식 질문하기 케이스(B47): 업로드 문서(공개 범위 역할·부서)와 호출자별 질문, 기대 찾음 여부·출처·답 안의 글, 보이면 안 되는 문서
//	policy.json     사내 규정 검색 케이스(B12): 호출자 역할·부서, 질의, 상위 N 안의 기대 조항, 보이면 안 되는 문서
//
// 채점
//   - 문서 판정: outcome 이 기대와 정확히 같고, 기대 근거 항목(field, result)이 모두 판정 근거에 있어야 통과.
//     critical 케이스(문서 안 지시문이 판정을 바꾸지 못하는지)는 하나라도 틀리면 합격률과 무관하게 실패.
//   - 규정 검색: 기대 조항이 모두 상위 N 안에 있고, 보이면 안 되는 문서의 조항이 결과에 하나도 없어야 통과.
//     expectEmpty 케이스는 0건이어야 한다. critical(열람 범위) 케이스는 하나라도 틀리면 합격률과 무관하게 실패.
//   - 도구 선택: 선택한 도구가 기대와 같고, 기대 인자가 모두 일치하고(숫자·불리언 정확 일치),
//     실제 MCP 호출의 허용/거부가 기대와 같아야 통과. 기대 도구가 ""이면 어떤 도구도 고르지 않아야 한다.
package evals

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/docverify/kit"
)

//go:embed docverify.json tools.json policy.json quotes.json journals.json knowledge.json baseline.json
var files embed.FS

// 합격 기준(코드). 임계치는 spec 5절 "문서 판정 90% 이상, 도구 선택 90% 이상" 이고 케이스 수 하한도 강제한다.
const (
	DocPassRate       = 0.90
	ToolPassRate      = 0.90
	PolicyPassRate    = 0.90
	MinDocCases       = 20
	MinToolCases      = 81 // 전체 도구 선택 케이스(현재 91)의 90%(내림). 케이스를 더하거나 뺄 때 같은 커밋에서 다시 계산한다
	MinPolicyCases    = 10
	QuotePassRate     = 0.90
	MinQuoteCases     = 10 // 견적서 추출 케이스(현재 12)의 90%(내림)
	JournalPassRate   = 0.90
	KnowledgePassRate = 0.90
	MinKnowledgeCases = 8  // 사내 지식 질문하기 케이스(현재 9)의 90%(내림). 열람 범위(critical) 케이스는 하나라도 틀리면 불합격
	MinJournalCases   = 10 // 자동 전표 케이스(현재 12)의 90%(내림). 전표 서비스(cmd/eval/journals_rig.go)에 연결되어 합격 기준에 든다(B31)
)

// EvidenceExpect 는 기대 근거 항목이다.
type EvidenceExpect struct {
	Field  string `json:"field"`
	Result string `json:"result"`
}

// DocExpect 는 문서 판정 기대값이다.
type DocExpect struct {
	Outcome  string           `json:"outcome"`
	Evidence []EvidenceExpect `json:"evidence"`
}

// DocCase 는 문서 판정 케이스다.
type DocCase struct {
	ID       string    `json:"id"`
	Desc     string    `json:"desc"`
	PO       string    `json:"po"`  // docverify.json 의 pos 키
	Doc      string    `json:"doc"` // 문서 텍스트
	AsImage  bool      `json:"asImage,omitempty"`
	Format   string    `json:"format,omitempty"` // "pdf"(한글 텍스트 레이어 PDF) | "pdf_garbled"(글꼴 매핑 없는 깨진 PDF). 비우면 텍스트
	Critical bool      `json:"critical,omitempty"`
	Expect   DocExpect `json:"expect"`
}

// DocSet 은 문서 판정 골든셋이다.
type DocSet struct {
	POs   map[string]kit.POSpec `json:"pos"`
	Cases []DocCase             `json:"cases"`
}

// ToolCase 는 도구 선택 케이스다.
type ToolCase struct {
	ID            string         `json:"id"`
	Role          string         `json:"role"`
	Request       string         `json:"request"`
	ExpectTool    string         `json:"expectTool"`
	ExpectArgs    map[string]any `json:"expectArgs,omitempty"`
	ExpectAllowed bool           `json:"expectAllowed"`
	Critical      bool           `json:"critical,omitempty"` // 문서 안 지시문이 도구 선택을 바꾸지 못하는지 같은 필수 케이스. 하나라도 틀리면 불합격
}

// PolicyCase 는 규정 검색 케이스다. Dept 는 부서 이름(구매팀·경영지원팀·영업팀)이다.
type PolicyCase struct {
	ID          string   `json:"id"`
	Desc        string   `json:"desc,omitempty"`
	Role        string   `json:"role"`
	Dept        string   `json:"dept"`
	Query       string   `json:"query,omitempty"`
	Related     bool     `json:"related,omitempty"` // true 면 질의 검색이 아니라 작성 화면용 관련 조항 조회(Amount·Items)를 채점한다
	Amount      int64    `json:"amount,omitempty"`
	Items       []string `json:"items,omitempty"`
	ExpectTop   []string `json:"expectTop,omitempty"`  // "문서slug/제N조". 모두 상위 TopN 안에 있어야 한다
	TopN        int      `json:"topN,omitempty"`       // 기본 3
	ExpectNone  []string `json:"expectNone,omitempty"` // 이 문서 slug 의 조항은 결과 어디에도 없어야 한다
	ExpectEmpty bool     `json:"expectEmpty,omitempty"`
	SameSetAs   string   `json:"sameSetAs,omitempty"` // 이 질의(조사·어미를 뗀 어간 질의)와 결과 문서·조항 집합이 같아야 한다(순서는 무관)
	Critical    bool     `json:"critical,omitempty"`
}

// QuoteExpect 는 견적서 추출 기대값이다. 포인터 칸은 비우면 채점하지 않는다.
type QuoteExpect struct {
	Status              string   `json:"status"` // ok | unreadable | unsupported
	Supplier            string   `json:"supplier,omitempty"`
	SupplierContains    string   `json:"supplierContains,omitempty"`
	SupplierNotContains []string `json:"supplierNotContains,omitempty"`
	LineTotal           *int64   `json:"lineTotal,omitempty"`
	DocTotal            *int64   `json:"docTotal,omitempty"`
	NoDocTotal          bool     `json:"noDocTotal,omitempty"`
	LeadDays            *int     `json:"leadDays,omitempty"`
	LeadText            string   `json:"leadText,omitempty"`
	NoLead              bool     `json:"noLead,omitempty"`
	Warn                []string `json:"warn,omitempty"` // 경고 문장에 들어 있어야 하는 구절
	NoWarn              bool     `json:"noWarn,omitempty"`
	BusinessNo          string   `json:"businessNo,omitempty"`
	VendorID            *int64   `json:"vendorId,omitempty"` // 거래처 마스터 연결 결과(0 = 연결 못 함)
}

// QuoteCase 는 견적서 추출 케이스다.
type QuoteCase struct {
	ID       string      `json:"id"`
	Desc     string      `json:"desc,omitempty"`
	Doc      string      `json:"doc"`
	Format   string      `json:"format,omitempty"` // "pdf" | "pdf_garbled" | "pdf_blank". 비우면 텍스트 파일
	Critical bool        `json:"critical,omitempty"`
	Expect   QuoteExpect `json:"expect"`
}

// QuoteVendor 는 견적서 케이스가 쓰는 거래처 마스터 한 줄이다.
type QuoteVendor struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	BusinessNo string `json:"businessNo"`
}

// QuoteSet 은 견적서 추출 골든셋이다.
type QuoteSet struct {
	Vendors []QuoteVendor `json:"vendors"`
	Cases   []QuoteCase   `json:"cases"`
}

// LoadQuoteSet 은 내장 견적서 추출 골든셋을 읽는다.
func LoadQuoteSet() (QuoteSet, error) {
	var s QuoteSet
	if err := decode("quotes.json", &s); err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		switch c.Format {
		case "", "pdf", "pdf_garbled", "pdf_blank":
		default:
			return s, fmt.Errorf("견적 케이스 %s: 알 수 없는 형식 %q", c.ID, c.Format)
		}
		switch c.Expect.Status {
		case "ok", "unreadable", "unsupported":
		default:
			return s, fmt.Errorf("견적 케이스 %s: 알 수 없는 기대 상태 %q", c.ID, c.Expect.Status)
		}
		if seen[c.ID] {
			return s, fmt.Errorf("견적 케이스 id 중복: %s", c.ID)
		}
		seen[c.ID] = true
	}
	return s, nil
}

// JournalDoc 은 증빙 한 건과 재무의 확정 결과다. Confirm 은 match | mismatch | hold | none(확정하지 않음)이다.
type JournalDoc struct {
	Kind    string `json:"kind"` // transaction_statement | tax_invoice
	Text    string `json:"text"`
	Confirm string `json:"confirm"`
}

// JournalLine 은 기대 전표 줄이다.
type JournalLine struct {
	Account string `json:"account"`
	Side    string `json:"side"` // D 차변 | C 대변
	Amount  int64  `json:"amount"`
}

// JournalExpect1 은 기대 전표 한 건이다.
type JournalExpect1 struct {
	Kind     string        `json:"kind"`   // auto_purchase
	Status   string        `json:"status"` // draft
	Vendor   string        `json:"vendor"`
	Evidence int           `json:"evidence"` // 연결된 증빙 수
	Lines    []JournalLine `json:"lines"`
}

// JournalQueueExpect 는 기대 전표 대기 한 행이다(Reason 은 코드).
type JournalQueueExpect struct {
	Reason string `json:"reason"`
}

// JournalExpect 는 자동 전표 기대값이다. Queue 가 nil 이면 대기는 채점하지 않고, 빈 목록이면 대기가 없어야 한다.
type JournalExpect struct {
	Journals        []JournalExpect1     `json:"journals,omitempty"`
	NoJournal       bool                 `json:"noJournal,omitempty"`
	Queue           []JournalQueueExpect `json:"queue,omitempty"`
	ConfirmSucceeds bool                 `json:"confirmSucceeds,omitempty"`
}

// JournalRules 는 케이스가 바꾸는 자동 전표 규칙 상태다.
type JournalRules struct {
	InactiveAccounts []string `json:"inactiveAccounts,omitempty"`
}

// JournalCase 는 자동 매입 전표 규칙 케이스다. 증빙 업로드 → 판정(mock 추출) → 확정 → 전표 생성까지 실제 파이프라인을 돌려 채점한다.
type JournalCase struct {
	ID       string        `json:"id"`
	Desc     string        `json:"desc,omitempty"`
	Critical bool          `json:"critical,omitempty"`
	PO       kit.POSpec    `json:"po"`
	Docs     []JournalDoc  `json:"docs"`
	Rules    JournalRules  `json:"rules,omitempty"`
	Expect   JournalExpect `json:"expect"`
}

// JournalSet 은 자동 전표 골든셋이다.
type JournalSet struct {
	Accounts map[string]string `json:"accounts"`
	Cases    []JournalCase     `json:"cases"`
}

var journalReasons = map[string]bool{"mismatch_confirmed": true, "hold_confirmed": true, "rule_account_inactive": true, "total_differs": true}

// LoadJournalSet 은 내장 자동 전표 골든셋을 읽고 구조를 검증한다(기대 전표는 차대가 맞아야 한다).
func LoadJournalSet() (JournalSet, error) {
	var s JournalSet
	if err := decode("journals.json", &s); err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if seen[c.ID] {
			return s, fmt.Errorf("전표 케이스 id 중복: %s", c.ID)
		}
		seen[c.ID] = true
		if len(c.PO.Lines) == 0 || len(c.PO.Receipt) == 0 {
			return s, fmt.Errorf("전표 케이스 %s: 발주 줄과 입고 줄이 필요합니다", c.ID)
		}
		if len(c.Docs) == 0 {
			return s, fmt.Errorf("전표 케이스 %s: 증빙이 없습니다", c.ID)
		}
		for _, d := range c.Docs {
			switch d.Kind {
			case "transaction_statement", "tax_invoice":
			default:
				return s, fmt.Errorf("전표 케이스 %s: 알 수 없는 증빙 종류 %q", c.ID, d.Kind)
			}
			switch d.Confirm {
			case "match", "mismatch", "hold", "none":
			default:
				return s, fmt.Errorf("전표 케이스 %s: 알 수 없는 확정 값 %q", c.ID, d.Confirm)
			}
		}
		for _, a := range c.Rules.InactiveAccounts {
			if _, ok := s.Accounts[a]; !ok {
				return s, fmt.Errorf("전표 케이스 %s: 알 수 없는 계정 %q", c.ID, a)
			}
		}
		e := c.Expect
		if (len(e.Journals) > 0) == e.NoJournal {
			return s, fmt.Errorf("전표 케이스 %s: 기대 전표(journals)와 noJournal 중 하나만 있어야 합니다", c.ID)
		}
		for _, q := range e.Queue {
			if !journalReasons[q.Reason] {
				return s, fmt.Errorf("전표 케이스 %s: 알 수 없는 대기 사유 %q", c.ID, q.Reason)
			}
		}
		for _, j := range e.Journals {
			if j.Kind != "auto_purchase" || j.Status != "draft" || len(j.Lines) < 2 {
				return s, fmt.Errorf("전표 케이스 %s: 기대 전표는 임시 매입 전표 두 줄 이상이어야 합니다", c.ID)
			}
			var d, cr int64
			for _, l := range j.Lines {
				if _, ok := s.Accounts[l.Account]; !ok {
					return s, fmt.Errorf("전표 케이스 %s: 알 수 없는 계정 %q", c.ID, l.Account)
				}
				if l.Amount <= 0 {
					return s, fmt.Errorf("전표 케이스 %s: 줄 금액은 양수여야 합니다", c.ID)
				}
				switch l.Side {
				case "D":
					d += l.Amount
				case "C":
					cr += l.Amount
				default:
					return s, fmt.Errorf("전표 케이스 %s: 알 수 없는 방향 %q", c.ID, l.Side)
				}
			}
			if d != cr {
				return s, fmt.Errorf("전표 케이스 %s: 기대 전표의 차변(%d)과 대변(%d)이 다릅니다", c.ID, d, cr)
			}
		}
	}
	return s, nil
}

// LoadPolicyCases 는 내장 규정 검색 케이스를 읽는다.
func LoadPolicyCases() ([]PolicyCase, error) {
	var w struct {
		Cases []PolicyCase `json:"cases"`
	}
	if err := decode("policy.json", &w); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, c := range w.Cases {
		if seen[c.ID] {
			return nil, fmt.Errorf("규정 검색 케이스 id 중복: %s", c.ID)
		}
		seen[c.ID] = true
		if len(c.ExpectTop) == 0 && len(c.ExpectNone) == 0 && !c.ExpectEmpty && c.SameSetAs == "" {
			return nil, fmt.Errorf("규정 검색 케이스 %s: 기대값이 없습니다", c.ID)
		}
	}
	return w.Cases, nil
}

// LoadDocSet 은 내장 문서 판정 골든셋을 읽는다. 알 수 없는 필드·없는 발주 키는 오류다.
func LoadDocSet() (DocSet, error) {
	var s DocSet
	if err := decode("docverify.json", &s); err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if _, ok := s.POs[c.PO]; !ok {
			return s, fmt.Errorf("케이스 %s: 알 수 없는 발주 %q", c.ID, c.PO)
		}
		switch c.Format {
		case "", "pdf", "pdf_garbled":
		default:
			return s, fmt.Errorf("케이스 %s: 알 수 없는 형식 %q", c.ID, c.Format)
		}
		if seen[c.ID] {
			return s, fmt.Errorf("케이스 id 중복: %s", c.ID)
		}
		seen[c.ID] = true
	}
	return s, nil
}

// LoadToolCases 는 내장 도구 선택 케이스를 읽는다.
func LoadToolCases() ([]ToolCase, error) {
	var w struct {
		Cases []ToolCase `json:"cases"`
	}
	if err := decode("tools.json", &w); err != nil {
		return nil, err
	}
	return w.Cases, nil
}

func decode(name string, v any) error {
	b, err := files.ReadFile(name)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// KnowledgeDoc 은 질문하기 케이스가 올려 두는 업로드 문서 한 건이다(관리자가 올린 것과 같은 경로).
type KnowledgeDoc struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"` // manual | regulation
	Title       string `json:"title"`
	Roles       string `json:"roles"`
	Departments string `json:"departments"`
	Filename    string `json:"filename"`
	Content     string `json:"content"`
}

// KnowledgeCase 는 질문하기 케이스다. Dept 는 부서 이름(구매팀·영업팀)이다.
type KnowledgeCase struct {
	ID           string   `json:"id"`
	Role         string   `json:"role"`
	Dept         string   `json:"dept"`
	Question     string   `json:"question"`
	ExpectFound  bool     `json:"expectFound"`
	ExpectSource string   `json:"expectSource,omitempty"` // 출처 한 줄에 들어 있어야 하는 구절(문서 제목)
	ExpectText   string   `json:"expectText,omitempty"`   // 답 안에 있어야 하는 글
	ExpectNone   []string `json:"expectNone,omitempty"`   // 이 제목의 문서는 출처 어디에도 없어야 한다
	ExpectNoText []string `json:"expectNoText,omitempty"` // 답 안에 없어야 하는 글(권한 밖 내용·검증에서 버려야 할 문장)
	Critical     bool     `json:"critical,omitempty"`
}

// KnowledgeSet 은 사내 지식 질문하기 골든셋이다.
type KnowledgeSet struct {
	Docs  []KnowledgeDoc  `json:"docs"`
	Cases []KnowledgeCase `json:"cases"`
}

// LoadKnowledgeSet 은 내장 질문하기 골든셋을 읽는다.
func LoadKnowledgeSet() (KnowledgeSet, error) {
	var s KnowledgeSet
	if err := decode("knowledge.json", &s); err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, c := range s.Cases {
		if seen[c.ID] {
			return s, fmt.Errorf("질문하기 케이스 id 중복: %s", c.ID)
		}
		seen[c.ID] = true
	}
	return s, nil
}
