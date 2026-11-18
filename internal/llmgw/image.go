package llmgw

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/llm"
)

// SettingImageTransmission 은 이미지 증빙을 LLM 으로 보낼지 정하는 관리자 설정 키다(app_settings, "true"/"false").
// 행이 없거나 읽을 수 없으면 꺼짐으로 본다(fail-closed). 변경 API·감사는 platform 관리자 핸들러 소유다.
// 키 문자열은 계약 상수 하나만 정본으로 둔다(관리자 변경 쪽과 어긋나 기본 꺼짐이 깨지는 것을 막는다).
const SettingImageTransmission = contract.SettingLLMImageTransmission

// 이미지 전송 관련 감사 동작. 이미지는 마스킹할 수 없어 전송 사실과 거부를 별도 동작으로 남긴다.
const (
	ActionImageSent    = "llm_image.sent"
	ActionImageBlocked = "llm_image.blocked"
)

// ImageTransmissionEnabled 는 이미지 전송 설정을 읽는다. 오류·행 없음·"true" 가 아닌 값은 모두 false.
func ImageTransmissionEnabled(ctx context.Context, db *sql.DB) bool {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = ?`, SettingImageTransmission).Scan(&v); err != nil {
		return false
	}
	return v == "true"
}

// ImageBlockedMessage 는 이미지 전송이 막혔을 때 사용자에게 보이는 안내다.
const ImageBlockedMessage = "이미지 증빙 전송이 꺼져 있어 이미지를 올릴 수 없습니다. 관리자가 설정에서 켜기 전에는 텍스트 문서나 텍스트가 있는 PDF 로 올려 주세요."

// guardImages 는 이미지가 든 요청의 전송 전 점검이다: 이미지 업로드 권한과 관리자 설정.
// 거부는 감사 로그에 남기고 LLM 을 부르지 않는다. 문서 서비스가 먼저 같은 점검을 하지만, 게이트웨이를 쓰는 다른 경로가
// 이미지를 실어도 우회되지 않게 여기서도 막는다.
func (g *Gateway) guardImages(ctx context.Context, p contract.Principal, req llm.Request) error {
	if len(req.Images) == 0 {
		return nil
	}
	reason := ""
	var err error
	switch {
	case !p.Can(contract.PermDocumentUploadImage):
		reason, err = "forbidden_role", fmt.Errorf("%w: 이미지 증빙은 재무 담당만 올릴 수 있습니다", contract.ErrForbidden)
	case !ImageTransmissionEnabled(ctx, g.o.DB):
		reason, err = "disabled", fmt.Errorf("%w: %s", contract.ErrForbidden, ImageBlockedMessage)
	default:
		return nil
	}
	actor := p
	actor.Channel = contract.ChannelLLMGW
	if aerr := g.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return g.o.Audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: ActionImageBlocked, TargetType: audit.TargetLLMCall, TargetID: "0",
			Result: audit.ResultDenied, Diff: []audit.DiffItem{{Key: "reason", New: reason}, {Key: "image_count", New: strconv.Itoa(len(req.Images))}}})
	}); aerr != nil {
		log.Printf("llmgw: 이미지 차단 감사 기록 실패: %v", aerr)
	}
	return err
}

// recordImageSent 는 예약 트랜잭션 안에서 이미지 전송 사실을 감사 로그에 남긴다. 기록이 실패하면 예약 트랜잭션이
// 롤백되어 호출이 일어나지 않는다. 이미지 바이트는 기록하지 않고 개수·크기·모델만 남긴다.
func (g *Gateway) recordImageSent(ctx context.Context, tx contract.Tx, p contract.Principal, req llm.Request, model string, usageID int64) error {
	if len(req.Images) == 0 {
		return nil
	}
	total := 0
	for _, im := range req.Images {
		total += len(im.Data)
	}
	actor := p
	actor.Channel = contract.ChannelLLMGW
	return g.o.Audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: ActionImageSent, TargetType: audit.TargetLLMCall,
		TargetID: strconv.FormatInt(usageID, 10), Result: audit.ResultSuccess,
		Diff: []audit.DiffItem{
			{Key: "image_count", New: strconv.Itoa(len(req.Images))},
			{Key: "image_bytes", New: strconv.Itoa(total)},
			{Key: "model", New: model},
			{Key: "purpose", New: req.Purpose},
		}})
}
