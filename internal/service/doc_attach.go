package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// AttachLimits 는 첨부 상한이다(명세 3.15·D16). 기본값은 파일당 10MB·문서당 20개·합계 50MB 이고 PL_ATTACH_* 설정(config.Config)으로 바꾼다.
type AttachLimits struct {
	MaxFileBytes  int64 // PL_ATTACH_MAX_FILE_BYTES
	MaxFiles      int   // PL_ATTACH_MAX_FILES
	MaxTotalBytes int64 // PL_ATTACH_MAX_TOTAL_BYTES
}

// DefaultAttachLimits 는 기본 상한이다(파일당 10MB·문서당 20개·합계 50MB).
func DefaultAttachLimits() AttachLimits {
	return AttachLimits{MaxFileBytes: 10 << 20, MaxFiles: 20, MaxTotalBytes: 50 << 20}
}

// OrDefault 는 비어 있거나 0 이하인 항목을 기본값으로 채운다(설정 값은 기동 때 config 가 검증해 넘긴다).
func (l AttachLimits) OrDefault() AttachLimits {
	d := DefaultAttachLimits()
	if l.MaxFileBytes > 0 {
		d.MaxFileBytes = l.MaxFileBytes
	}
	if l.MaxFiles > 0 {
		d.MaxFiles = l.MaxFiles
	}
	if l.MaxTotalBytes > 0 {
		d.MaxTotalBytes = l.MaxTotalBytes
	}
	return d
}

// 허용 확장자와 서버가 정하는 내용 유형. 내용은 해석하지 않는다.
var attachTypes = map[string]string{
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".txt":  "text/plain; charset=utf-8",
	".csv":  "text/csv; charset=utf-8",
	".hwp":  "application/x-hwp",
}

// sniffOK 는 확장자와 내용 서명이 맞는지다: pdf·png·jpg 는 매직 바이트, docx·xlsx·pptx 는 zip 서명, hwp 는 OLE2 또는 zip, txt·csv 는 NUL 없는 UTF-8.
func sniffOK(ext string, data []byte) bool {
	switch ext {
	case ".pdf":
		return bytes.HasPrefix(data, []byte("%PDF-"))
	case ".png":
		return bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})
	case ".jpg", ".jpeg":
		return bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF})
	case ".docx", ".xlsx", ".pptx":
		return bytes.HasPrefix(data, []byte{'P', 'K', 0x03, 0x04})
	case ".hwp":
		return bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0}) || bytes.HasPrefix(data, []byte{'P', 'K', 0x03, 0x04})
	case ".txt", ".csv":
		return utf8.Valid(data) && !bytes.Contains(data, []byte{0})
	}
	return false
}

// SanitizeFilename 은 저장·표시용 파일 이름이다: 경로 문자와 제어 문자를 없애고 100자로 자른다.
func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	var b strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' || r == '"' || r == '<' || r == '>' || r == '|' || r == '?' || r == '*' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(strings.Trim(b.String(), ". "))
	rs := []rune(out)
	if len(rs) > 100 {
		ext := path.Ext(out)
		rs = []rune(strings.TrimSuffix(string(rs[:100]), ext) + ext)
	}
	return string(rs)
}

// AttachmentService 는 양식 문서 첨부의 업로드·내려받기·정리다(B23 명세 3.15).
type AttachmentService struct {
	Base
	Limits AttachLimits
	Docs   *ApprovalDocService
}

// NewAttachmentService 는 첨부 서비스를 만든다.
func NewAttachmentService(b Base, docs *ApprovalDocService) *AttachmentService {
	return &AttachmentService{Base: b, Limits: b.Attach.OrDefault(), Docs: docs}
}

var _ domain.AttachmentService = (*AttachmentService)(nil)

// Upload 는 파일 하나를 올린다(아직 문서에 붙지 않은 상태, 올린 사람만 보인다). 확장자·크기·내용 서명을 검사한다.
func (s *AttachmentService) Upload(ctx context.Context, filename string, data []byte) (domain.Attachment, error) {
	p, err := s.authorize(ctx, contract.PermDraftCreate, "doc_attachment.upload", audit.TargetDocAttachment)
	if err != nil {
		return domain.Attachment{}, err
	}
	name := SanitizeFilename(filename)
	ext := strings.ToLower(path.Ext(name))
	ctype, ok := attachTypes[ext]
	switch {
	case name == "":
		return domain.Attachment{}, validation("파일 이름이 올바르지 않습니다")
	case !ok:
		return domain.Attachment{}, validation("허용하지 않는 파일 형식입니다(%s)", ext)
	case int64(len(data)) > s.Limits.MaxFileBytes:
		return domain.Attachment{}, validation("첨부는 파일당 %dMB, 문서당 %d개까지입니다", s.Limits.MaxFileBytes>>20, s.Limits.MaxFiles)
	case len(data) == 0:
		return domain.Attachment{}, validation("빈 파일은 올릴 수 없습니다")
	case !sniffOK(ext, data):
		return domain.Attachment{}, validation("파일 내용이 확장자와 맞지 않습니다")
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	var id int64
	err = s.write(ctx, p, "doc_attachment.upload", audit.TargetDocAttachment, 0, func(tx contract.Tx) error {
		newID, err := dbutil.InsertID(ctx, tx, `INSERT INTO document_attachments (uploaded_by, filename, size, sha256, content_type, content, created_at) VALUES (?,?,?,?,?,?,?)`,
			p.UserID, name, len(data), hash, ctype, data, s.now().Format(time.RFC3339))
		if err != nil {
			return err
		}
		id = newID
		// 감사에는 파일 이름·크기·해시만 남긴다(내용은 남기지 않는다).
		return s.record(ctx, tx, p, "doc_attachment.upload", audit.TargetDocAttachment, id,
			audit.DiffItem{Key: "filename", New: name}, audit.DiffItem{Key: "size", New: fmt.Sprint(len(data))}, audit.DiffItem{Key: "sha256", New: hash})
	})
	if err != nil {
		return domain.Attachment{}, err
	}
	return domain.Attachment{ID: id, Filename: name, Size: int64(len(data)), SHA256: hash}, nil
}

// Open 은 첨부 내용을 돌려준다. 문서에 붙은 첨부는 문서 열람 조건 함수(ViewScope)를 그대로 타고(범위 밖은 찾을 수 없음), 붙지 않은 첨부는 올린 사람만 연다.
func (s *AttachmentService) Open(ctx context.Context, id int64) (domain.AttachmentFile, error) {
	p, err := contract.Authorize(ctx, contract.PermDraftRead)
	if err != nil {
		return domain.AttachmentFile{}, err
	}
	var f domain.AttachmentFile
	var docID, cardTxn sql.NullInt64
	var uploader int64
	err = s.DB.QueryRowContext(ctx, `SELECT doc_id, card_txn_id, uploaded_by, filename, content_type, content, size, sha256 FROM document_attachments WHERE id = ?`, id).
		Scan(&docID, &cardTxn, &uploader, &f.Filename, &f.ContentType, &f.Data, &f.Size, &f.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return f, contract.ErrNotFound
	}
	if err != nil {
		return f, err
	}
	if p.Channel == contract.ChannelMCP {
		return domain.AttachmentFile{}, contract.ErrNotFound // 첨부 내용은 MCP 로 나가지 않는다
	}
	if docID.Valid && f.Filename == "" {
		// 폐기된 문서의 첨부(폐기 때 내용을 지운 행). 문서를 볼 수 있는 사람에게만 폐기 안내를 한다.
		if _, err := s.Docs.Get(ctx, docID.Int64); err != nil {
			return domain.AttachmentFile{}, err
		}
		return domain.AttachmentFile{}, fmt.Errorf("%w: 폐기된 문서입니다. 보존 기한이 지나 첨부가 삭제되었습니다.", contract.ErrInvalidState)
	}
	if cardTxn.Valid {
		// 카드 소명 증빙: 소지자·재무·관리자·감사(card:read_all)·확인할 조직장만 연다.
		if uploader != p.UserID && !s.cardTxnVisible(ctx, p, cardTxn.Int64) {
			return domain.AttachmentFile{}, contract.ErrNotFound
		}
		f.ID = id
		return f, nil
	}
	if !docID.Valid {
		if uploader != p.UserID {
			return domain.AttachmentFile{}, contract.ErrNotFound
		}
		f.ID = id
		return f, nil
	}
	// 휴가 신청서(LEAVE)의 증빙은 진단서일 수 있어 문서를 볼 수 있는 결재자라도 내려받지 못한다. 이 문서는 문서 열람 판정 대신 휴가 열람 범위가 정한다(B50 11.4).
	if handled, err := s.leaveProofGate(ctx, p, docID.Int64, id); err != nil {
		return domain.AttachmentFile{}, err
	} else if handled {
		f.ID = id
		return f, nil
	}
	// 문서 열람 조건 함수(+특권 열람 감사)를 그대로 쓴다.
	if _, err := s.Docs.Get(ctx, docID.Int64); err != nil {
		return domain.AttachmentFile{}, err
	}
	f.ID = id
	return f, nil
}

// linkAttachments 는 문서의 첨부 필드가 가리키는 첨부를 문서에 붙인다(트랜잭션 안). 본인이 올린 아직 안 붙은(또는 이 문서의) 첨부만 되고,
// 빠진 첨부는 연결을 풀어 24시간 뒤 정리 대상이 된다. 개수·합계 상한도 여기서 검사한다.
func (b Base) linkAttachments(ctx context.Context, tx contract.Tx, limits AttachLimits, docID, userID int64, defs []domain.FieldDef, fields map[string]any) error {
	var ids []int64
	for _, d := range defs {
		if d.Type != domain.FieldAttachment {
			continue
		}
		list, _ := fields[d.Key].([]any)
		for _, v := range list {
			if n, ok := asInt(v); ok {
				ids = append(ids, n)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_attachments SET doc_id = NULL WHERE doc_id = ?`, docID); err != nil {
		return err
	}
	if len(ids) > limits.MaxFiles {
		return validation("첨부는 파일당 %dMB, 문서당 %d개까지입니다", limits.MaxFileBytes>>20, limits.MaxFiles)
	}
	var total int64
	for _, id := range ids {
		var size int64
		var uploader int64
		var doc sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT size, uploaded_by, doc_id FROM document_attachments WHERE id = ?`, id).Scan(&size, &uploader, &doc)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (uploader != userID || doc.Valid) {
			return validation("첨부를 찾을 수 없습니다. 다시 올려 주세요")
		}
		if err != nil {
			return err
		}
		total += size
		if _, err := tx.ExecContext(ctx, `UPDATE document_attachments SET doc_id = ? WHERE id = ?`, docID, id); err != nil {
			return err
		}
	}
	if total > limits.MaxTotalBytes {
		return validation("첨부 합계는 %dMB 까지입니다", limits.MaxTotalBytes>>20)
	}
	return nil
}

// Attachments 는 문서에 붙은 첨부의 이름·크기(내용 제외)다.
func (b Base) docAttachments(ctx context.Context, docID int64) []domain.Attachment {
	rows, err := b.DB.QueryContext(ctx, `SELECT id, filename, size, sha256 FROM document_attachments WHERE doc_id = ? AND filename <> '' ORDER BY id`, docID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []domain.Attachment
	for rows.Next() {
		var a domain.Attachment
		if err := rows.Scan(&a.ID, &a.Filename, &a.Size, &a.SHA256); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// PurgeOrphans 는 문서에 붙지 않은 채 24시간이 지난 첨부를 지운다(시스템 작업). 지운 건수만 감사에 남긴다. 건수가 0 이면 기록하지 않는다.
func (s *AttachmentService) PurgeOrphans(ctx context.Context) (int, error) {
	cutoff := s.now().Add(-24 * time.Hour).Format(time.RFC3339)
	var n int
	err := s.Txs.WithTx(ctx, func(tx contract.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM document_attachments WHERE doc_id IS NULL AND card_txn_id IS NULL AND created_at < ?`, cutoff)
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		if n == 0 {
			return nil
		}
		return s.Audit.Record(ctx, tx, audit.Entry{Actor: contract.Principal{Role: "system", Channel: contract.ChannelWeb}, Action: "doc_attachment.purge", TargetType: audit.TargetDocAttachment, TargetID: "orphans",
			Result: audit.ResultSuccess, Diff: []audit.DiffItem{{Key: "count", New: fmt.Sprint(n)}}})
	})
	return n, err
}

// Run 은 서버가 켜져 있는 동안 한 시간마다 고아 첨부를 정리한다(기동 때 한 번도 한다).
func (s *AttachmentService) Run(ctx context.Context) {
	tick := func() {
		if _, err := s.PurgeOrphans(ctx); err != nil && ctx.Err() == nil {
			log.Printf("attachments: 고아 첨부 정리 실패: %v", err)
		}
	}
	tick()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// linkCardEvidence 는 카드 소명 증빙 첨부를 카드 내역에 연결한다(트랜잭션 안). 본인이 올린, 아직 어디에도 붙지 않은(또는 이 내역의) 첨부만 된다.
func (b Base) linkCardEvidence(ctx context.Context, tx contract.Tx, txnID, userID int64, ids []int64) error {
	for _, id := range ids {
		var uploader int64
		var doc, card sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT uploaded_by, doc_id, card_txn_id FROM document_attachments WHERE id = ?`, id).Scan(&uploader, &doc, &card)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (uploader != userID || doc.Valid || (card.Valid && card.Int64 != txnID))) {
			return validation("첨부를 찾을 수 없거나 다른 곳에 이미 쓰인 파일입니다. 파일을 다시 올려 주세요.")
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE document_attachments SET card_txn_id = ? WHERE id = ?`, txnID, id); err != nil {
			return err
		}
	}
	return nil
}

// cardEvidence 는 카드 내역에 연결된 증빙 첨부 목록이다.
func (b Base) cardEvidence(ctx context.Context, q contract.Tx, txnID int64) []domain.Attachment {
	rows, err := q.QueryContext(ctx, `SELECT id, filename, size, sha256 FROM document_attachments WHERE card_txn_id = ? ORDER BY id`, txnID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []domain.Attachment
	for rows.Next() {
		var a domain.Attachment
		if err := rows.Scan(&a.ID, &a.Filename, &a.Size, &a.SHA256); err == nil {
			out = append(out, a)
		}
	}
	return out
}
