package fake

import (
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// 가짜가 계약을 만족하는지 컴파일 시점에 확인한다.
var (
	_ audit.Recorder          = (*Recorder)(nil)
	_ audit.Reader            = (*Recorder)(nil)
	_ contract.TxManager      = (*TxManager)(nil)
	_ contract.Authenticator  = (*Authenticator)(nil)
	_ contract.Masker         = Masker{}
	_ llm.Client              = (*MockLLM)(nil)
	_ domain.PurchaseService  = (*ERP)(nil)
	_ domain.ApprovalService  = (*ERP)(nil)
	_ domain.InventoryService = (*ERP)(nil)
	_ domain.DocumentService  = (*Documents)(nil)
)

// Bundle 은 가짜 의존성 한 벌이다. erp·ai 테스트가 deps.Deps 를 한 줄로 얻는 용도.
type Bundle struct {
	Audit     *Recorder
	Txs       *TxManager
	Auth      *Authenticator
	LLM       *MockLLM
	ERP       *ERP
	Documents *Documents
}

// NewBundle 은 서로 연결된(같은 감사 기록기를 공유하는) 가짜 한 벌을 만든다.
func NewBundle() *Bundle {
	rec := NewRecorder()
	return &Bundle{
		Audit: rec, Txs: &TxManager{}, Auth: NewAuthenticator(), LLM: &MockLLM{},
		ERP: NewERP(rec), Documents: NewDocuments(rec),
	}
}

// Deps 는 Bundle 을 deps.Deps 로 바꾼다(DB 는 nil).
func (b *Bundle) Deps() deps.Deps {
	return deps.Deps{
		Txs: b.Txs, Audit: b.Audit, AuditReader: b.Audit, Authenticator: b.Auth, Masker: Masker{},
		LLM: b.LLM, Documents: b.Documents,
		Purchase: b.ERP, Approval: b.ERP, Delegation: b.ERP, Inventory: b.ERP,
	}
}
