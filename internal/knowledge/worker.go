package knowledge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// Worker 는 색인 대기열을 처리한다(명세 4.2·5.2). 업무 트랜잭션은 대기열 줄만 넣고, 읽기·조각 내기·색인은 여기서 줄 단위 트랜잭션으로 한다.
// 같은 내용이면(내용 해시) 건너뛰므로 몇 번을 돌려도 같은 결과다. 서버만 돌린다(erp-mcp 는 세대 번호를 보고 따라오기만 한다).
type Worker struct {
	Txs   contract.TxManager
	Now   func() time.Time
	Batch int
	repo  repository.SQLKnowledge
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

// RunOnce 는 처리할 줄을 한 묶음 처리한다. 처리한 줄 수를 돌려준다. 줄 하나의 실패는 그 줄에 기록하고 나머지는 계속한다.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	n := w.Batch
	if n <= 0 {
		n = 50
	}
	var rows []repository.KQueueRow
	if err := w.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var err error
		rows, err = w.repo.ClaimOpen(ctx, tx, n, w.now())
		return err
	}); err != nil {
		return 0, err
	}
	for _, r := range rows {
		perr := w.Txs.WithTx(ctx, func(tx contract.Tx) error { return w.process(ctx, tx, r) })
		err := w.Txs.WithTx(ctx, func(tx contract.Tx) error {
			if perr != nil {
				log.Printf("knowledge: 색인 실패(대기열 %d, 시도 %d): %v", r.ID, r.Attempts, perr)
				return w.repo.QueueFail(ctx, tx, r.ID, r.Attempts, "색인하지 못했습니다("+shortErr(perr)+")", w.now())
			}
			return w.repo.QueueDone(ctx, tx, r.ID, w.now())
		})
		if err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func shortErr(err error) string {
	s := err.Error()
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:80])
	}
	return s
}

// Drain 은 대기열이 빌 때까지(또는 한도까지) 돌린다. 시험·다시 색인 명령이 쓴다.
func (w *Worker) Drain(ctx context.Context, maxRounds int) (int, error) {
	total := 0
	for i := 0; i < maxRounds; i++ {
		n, err := w.RunOnce(ctx)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
	return total, nil
}

// Run 은 주기마다 RunOnce 를 부른다. ctx 가 끝나면 멈춘다.
func (w *Worker) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := w.Drain(ctx, 20); err != nil && ctx.Err() == nil {
				log.Printf("knowledge: 대기열 처리 오류: %v", err)
			}
		}
	}
}

func (w *Worker) process(ctx context.Context, tx contract.Tx, r repository.KQueueRow) error {
	changed := false
	var err error
	switch {
	case r.Op == "remove":
		changed, err = w.removeAll(ctx, tx, r)
	case r.Kind == repository.KSrcApproval:
		changed, err = w.upsertApproval(ctx, tx, r.SourceID)
	case r.Kind == repository.KSrcUpload:
		changed, err = w.upsertUpload(ctx, tx, r.SourceID)
	case r.Kind == repository.KSrcAttachment:
		changed, err = w.upsertAttachment(ctx, tx, r.SourceID)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	if changed {
		return w.repo.BumpGeneration(ctx, tx, w.now())
	}
	return nil
}

func (w *Worker) removeAll(ctx context.Context, tx contract.Tx, r repository.KQueueRow) (bool, error) {
	a, err := w.repo.DeleteDoc(ctx, tx, r.Kind, r.SourceID)
	if err != nil {
		return false, err
	}
	if r.Kind == repository.KSrcApproval {
		b, err := w.repo.DeleteByParent(ctx, tx, r.SourceID)
		return a || b, err
	}
	return a, nil
}

// upsertApproval 은 결재 문서 한 건을 색인한다. 문서가 없거나 완료가 아니거나 폐기됐거나 양식이 검색에서 빠졌으면 색인에서 뺀다.
func (w *Worker) upsertApproval(ctx context.Context, tx contract.Tx, id int64) (bool, error) {
	d, found, err := w.repo.ApprovalDocForIndex(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if !found || !d.Done || d.Disposed || !d.Searchable {
		return w.removeAll(ctx, tx, repository.KQueueRow{Kind: repository.KSrcApproval, SourceID: id})
	}
	chunks, tooLong := SplitLines(d.Lines, "body", "")
	if len(chunks) == 0 {
		return w.removeAll(ctx, tx, repository.KQueueRow{Kind: repository.KSrcApproval, SourceID: id})
	}
	state, note := StateReady, ""
	if tooLong {
		state, note = StateTooLong, "내용이 길어 앞부분만 색인했습니다"
	}
	doc := repository.KDoc{EntityID: d.EntityID, Kind: repository.KSrcApproval, SourceID: id, Title: d.Title, DocNo: d.No, FormCode: d.FormCode,
		ContentHash: ContentHash(fmt.Sprintf("%s|%s|%s|%d", d.No, d.Title, d.FormCode, d.EntityID), chunks), State: state, Note: note}
	// too_long 도 검색에는 쓴다(앞부분). 읽기 대상 상태는 ready 와 too_long 이다.
	return w.repo.UpsertDoc(ctx, tx, doc, chunks, w.now())
}

// upsertUpload 는 업로드 문서 한 건을 색인한다. 지워졌거나 새 버전에 밀려난 문서는 색인에서 뺀다. 파일을 읽지 못하면 조각 없이 상태와 사유만 남긴다
// (업로드 서비스가 저장 전에 같은 추출을 시험하므로 보통은 일어나지 않는다).
func (w *Worker) upsertUpload(ctx context.Context, tx contract.Tx, id int64) (bool, error) {
	u, err := w.repo.UploadByID(ctx, tx, id, true)
	if errors.Is(err, contract.ErrNotFound) || (err == nil && u.RemovedAt != "") {
		return w.removeAll(ctx, tx, repository.KQueueRow{Kind: repository.KSrcUpload, SourceID: id})
	}
	if err != nil {
		return false, err
	}
	doc := repository.KDoc{EntityID: u.EntityID, Kind: repository.KSrcUpload, SourceID: id, Title: u.Title, Version: u.Version}
	secs, xerr := ExtractUpload(u.Filename, u.Content)
	if xerr != nil {
		doc.State, doc.Note = StateOfExtractError(xerr), xerr.Error()
		doc.ContentHash = ContentHash(fmt.Sprintf("%s|%d|%d|%s", u.Title, u.Version, u.EntityID, doc.State), nil)
		return w.repo.UpsertDoc(ctx, tx, doc, nil, w.now())
	}
	chunks, tooLong := ToChunks(secs)
	doc.State = StateReady
	if tooLong {
		doc.State, doc.Note = StateTooLong, "내용이 길어 앞부분만 색인했습니다"
	}
	doc.ContentHash = ContentHash(fmt.Sprintf("%s|%d|%d|%s", u.Title, u.Version, u.EntityID, u.SHA256), chunks)
	return w.repo.UpsertDoc(ctx, tx, doc, chunks, w.now())
}

// StateOfExtractError 는 추출 실패를 지식 문서의 상태 이름으로 바꾼다.
func StateOfExtractError(err error) string {
	var xe *ExtractError
	if !errors.As(err, &xe) {
		return "failed"
	}
	switch xe.Msg {
	case MsgNoText:
		return "no_text"
	case MsgGarbled:
		return "unreadable"
	case MsgFormat, MsgNotUTF8, MsgAttachUnsupported:
		return "unsupported"
	}
	return "failed"
}

// upsertAttachment 는 결재 문서 첨부 한 건을 색인한다. 부모 문서가 완료·비폐기·검색 포함이 아니거나 첨부가 비워졌으면 색인에서 뺀다.
// 글을 읽지 못하는 첨부(스캔·깨짐·지원하지 않는 형식)는 조각 없이 상태와 사유만 남긴다(관리 화면 색인 현황).
func (w *Worker) upsertAttachment(ctx context.Context, tx contract.Tx, id int64) (bool, error) {
	a, found, err := w.repo.AttachmentForIndex(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if !found || !a.Done || a.Disposed || !a.Searchable || a.Filename == "" {
		return w.removeAll(ctx, tx, repository.KQueueRow{Kind: repository.KSrcAttachment, SourceID: id})
	}
	doc := repository.KDoc{EntityID: a.EntityID, Kind: repository.KSrcAttachment, SourceID: id, ParentDocID: a.DocID, Title: a.Filename, DocNo: a.DocNo, FormCode: a.FormCode}
	head := fmt.Sprintf("%s|%d|%d|%s", a.Filename, a.DocID, a.EntityID, a.DocNo)
	secs, xerr := ExtractAttachment(a.Filename, a.Content)
	if xerr != nil {
		doc.State, doc.Note = StateOfExtractError(xerr), xerr.Error()
		doc.ContentHash = ContentHash(head+"|"+doc.State, nil)
		return w.repo.UpsertDoc(ctx, tx, doc, nil, w.now())
	}
	chunks, tooLong := ToChunks(secs)
	doc.State = StateReady
	if tooLong {
		doc.State, doc.Note = StateTooLong, "내용이 길어 앞부분만 색인했습니다"
	}
	doc.ContentHash = ContentHash(head, chunks)
	return w.repo.UpsertDoc(ctx, tx, doc, chunks, w.now())
}
