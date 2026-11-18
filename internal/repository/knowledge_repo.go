package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// 사내 지식 검색 저장소(B47, 명세 4·5·6절). 색인 표(knowledge_docs·knowledge_chunks)와 대기열·세대 번호, 결재 문서 본문 읽기, 열람 가능 문서 판정.
// 열람 판정은 ViewScope.Cond 한 곳이다(OpenableDocIDs). 색인 표에는 권한 정보가 없다.

type SQLKnowledge struct{}

// KnowledgeKinds 는 지식 문서 종류다.
const (
	KSrcApproval   = "approval_doc"
	KSrcAttachment = "attachment"
	KSrcUpload     = "upload"
)

// ---- 대기열 ----

// KQueueRow 는 처리할 대기열 줄이다.
type KQueueRow struct {
	ID       int64
	Op       string
	Kind     string
	SourceID int64
	EntityID int64
	Attempts int
}

// Enqueue 는 색인 대기열에 줄을 더한다(업무 트랜잭션 안에서 부른다: 롤백되면 줄도 없다).
func (SQLKnowledge) Enqueue(ctx context.Context, q contract.Tx, op, kind string, sourceID, entityID int64, reason string, now time.Time) error {
	if entityID <= 0 {
		entityID = contract.WriteEntityID(ctx)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO knowledge_queue (op, source_kind, source_id, entity_id, reason, created_at) VALUES (?,?,?,?,?,?)`,
		op, kind, sourceID, entityID, reason, fmtTime(now))
	return mapErr(err)
}

// staleClaim 은 처리 중으로 표시된 뒤 이 시간이 지나도 끝나지 않은 줄을 다시 잡는 기준이다(워커가 죽은 경우).
const staleClaim = 5 * time.Minute

// MaxQueueAttempts 는 한 줄을 시도하는 최대 횟수다. 넘으면 사유를 남기고 처리 끝으로 둔다(업무는 막지 않는다).
const MaxQueueAttempts = 5

// ClaimOpen 은 처리할 줄을 최대 n 개 잡는다(먼저 들어온 순). 잡은 줄은 처리 중으로 표시하고 시도 횟수를 올린다.
func (SQLKnowledge) ClaimOpen(ctx context.Context, q contract.Tx, n int, now time.Time) ([]KQueueRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, op, source_kind, source_id, entity_id, attempts FROM knowledge_queue
		WHERE done_at IS NULL AND (claimed_at IS NULL OR claimed_at < ?) ORDER BY id LIMIT ?`, fmtTime(now.Add(-staleClaim)), n)
	if err != nil {
		return nil, err
	}
	var out []KQueueRow
	for rows.Next() {
		var r KQueueRow
		if err := rows.Scan(&r.ID, &r.Op, &r.Kind, &r.SourceID, &r.EntityID, &r.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		if _, err := q.ExecContext(ctx, `UPDATE knowledge_queue SET claimed_at = ?, attempts = attempts + 1 WHERE id = ?`, fmtTime(now), out[i].ID); err != nil {
			return nil, mapErr(err)
		}
		out[i].Attempts++
	}
	return out, nil
}

// QueueDone 은 줄을 처리 끝으로 표시한다.
func (SQLKnowledge) QueueDone(ctx context.Context, q contract.Tx, id int64, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE knowledge_queue SET done_at = ?, last_error = '' WHERE id = ?`, fmtTime(now), id)
	return mapErr(err)
}

// QueueFail 은 실패를 기록한다. 시도 횟수가 한도에 닿았으면 처리 끝으로 둔다(사유는 남는다).
func (SQLKnowledge) QueueFail(ctx context.Context, q contract.Tx, id int64, attempts int, msg string, now time.Time) error {
	if len([]rune(msg)) > 200 {
		msg = string([]rune(msg)[:200])
	}
	if attempts >= MaxQueueAttempts {
		_, err := q.ExecContext(ctx, `UPDATE knowledge_queue SET done_at = ?, last_error = ? WHERE id = ?`, fmtTime(now), msg, id)
		return mapErr(err)
	}
	_, err := q.ExecContext(ctx, `UPDATE knowledge_queue SET claimed_at = NULL, last_error = ? WHERE id = ?`, msg, id)
	return mapErr(err)
}

// ---- 세대 번호 ----

// Generation 은 색인 세대 번호와 바뀐 시각이다.
func (SQLKnowledge) Generation(ctx context.Context, q contract.Tx) (int64, string, error) {
	var g int64
	var at string
	err := q.QueryRowContext(ctx, `SELECT generation, changed_at FROM knowledge_state WHERE id = 1`).Scan(&g, &at)
	return g, at, err
}

// BumpGeneration 은 색인이 바뀌었음을 알린다(다른 프로세스의 메모리 색인이 따라온다).
func (SQLKnowledge) BumpGeneration(ctx context.Context, q contract.Tx, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE knowledge_state SET generation = generation + 1, changed_at = ? WHERE id = 1`, fmtTime(now))
	return mapErr(err)
}

// ---- 색인 표 ----

// KDoc 은 색인할 지식 문서 한 건이다.
type KDoc struct {
	EntityID    int64
	Kind        string
	SourceID    int64
	ParentDocID int64
	Title       string
	DocNo       string
	FormCode    string
	Version     int
	ContentHash string
	State       string
	Note        string
}

// KChunk 는 조각이다.
type KChunk struct {
	LocatorKind string
	Locator     string
	Heading     string
	Text        string
}

// UpsertDoc 은 지식 문서와 조각을 통째로 바꾼다. 내용 해시와 상태가 같으면 건너뛴다(changed=false). 색인이 바뀌면 호출부가 세대를 올린다.
func (SQLKnowledge) UpsertDoc(ctx context.Context, q contract.Tx, d KDoc, chunks []KChunk, now time.Time) (changed bool, err error) {
	var id int64
	var hash, state string
	err = q.QueryRowContext(ctx, `SELECT id, content_hash, extract_state FROM knowledge_docs WHERE source_kind = ? AND source_id = ?`, d.Kind, d.SourceID).Scan(&id, &hash, &state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id = 0
	case err != nil:
		return false, err
	case hash == d.ContentHash && state == d.State:
		return false, nil
	}
	if id == 0 {
		id, err = dbutil.InsertID(ctx, q, `INSERT INTO knowledge_docs (entity_id, source_kind, source_id, parent_doc_id, title, doc_no, form_code, version, content_hash, extract_state, extract_note, chunk_count, indexed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.EntityID, d.Kind, d.SourceID, d.ParentDocID, d.Title, d.DocNo, d.FormCode, maxInt(d.Version, 1), d.ContentHash, d.State, d.Note, len(chunks), fmtTime(now))
		if err != nil {
			return false, mapErr(err)
		}
	} else {
		if _, err := q.ExecContext(ctx, `DELETE FROM knowledge_chunks WHERE doc_id = ?`, id); err != nil {
			return false, mapErr(err)
		}
		if _, err := q.ExecContext(ctx, `UPDATE knowledge_docs SET entity_id = ?, parent_doc_id = ?, title = ?, doc_no = ?, form_code = ?, version = ?, content_hash = ?, extract_state = ?, extract_note = ?, chunk_count = ?, indexed_at = ? WHERE id = ?`,
			d.EntityID, d.ParentDocID, d.Title, d.DocNo, d.FormCode, maxInt(d.Version, 1), d.ContentHash, d.State, d.Note, len(chunks), fmtTime(now), id); err != nil {
			return false, mapErr(err)
		}
	}
	for i, c := range chunks {
		if _, err := q.ExecContext(ctx, `INSERT INTO knowledge_chunks (doc_id, entity_id, seq, locator_kind, locator, heading, text) VALUES (?,?,?,?,?,?,?)`,
			id, d.EntityID, i+1, c.LocatorKind, c.Locator, c.Heading, c.Text); err != nil {
			return false, mapErr(err)
		}
	}
	return true, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// DeleteDoc 은 지식 문서와 조각을 지운다. 지운 문서가 있었으면 true.
func (SQLKnowledge) DeleteDoc(ctx context.Context, q contract.Tx, kind string, sourceID int64) (bool, error) {
	return deleteKnowledge(ctx, q, `source_kind = ? AND source_id = ?`, kind, sourceID)
}

// DeleteByParent 는 결재 문서에 딸린 지식 문서(첨부)를 모두 지운다.
func (SQLKnowledge) DeleteByParent(ctx context.Context, q contract.Tx, parentDocID int64) (bool, error) {
	return deleteKnowledge(ctx, q, `parent_doc_id = ?`, parentDocID)
}

func deleteKnowledge(ctx context.Context, q contract.Tx, cond string, args ...any) (bool, error) {
	if _, err := q.ExecContext(ctx, `DELETE FROM knowledge_chunks WHERE doc_id IN (SELECT id FROM knowledge_docs WHERE `+cond+`)`, args...); err != nil {
		return false, mapErr(err)
	}
	res, err := q.ExecContext(ctx, `DELETE FROM knowledge_docs WHERE `+cond, args...)
	if err != nil {
		return false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PurgeApprovalDoc 은 폐기 트랜잭션이 부른다: 결재 문서와 그 첨부의 조각을 지우고 세대를 올린다(명세 5.2: 동기 삭제, D11).
func PurgeApprovalDoc(ctx context.Context, q contract.Tx, docID int64, now time.Time) error {
	k := SQLKnowledge{}
	a, err := k.DeleteDoc(ctx, q, KSrcApproval, docID)
	if err != nil {
		return err
	}
	b, err := k.DeleteByParent(ctx, q, docID)
	if err != nil {
		return err
	}
	if a || b {
		return k.BumpGeneration(ctx, q, now)
	}
	return nil
}

// ChunkRow 는 메모리 색인을 만들 때 읽는 조각과 부모 정보다.
type ChunkRow struct {
	ChunkID     int64
	DocID       int64
	EntityID    int64
	Kind        string
	SourceID    int64
	ParentDocID int64
	Title       string
	DocNo       string
	LocatorKind string
	Locator     string
	Heading     string
	Text        string
	Version     int
	// 업로드 문서의 종류(manual·regulation)와 열람 범위 원문(역할·부서). 결재 문서·첨부는 비어 있다.
	UploadKind string
	Roles      string
	Depts      string
}

// LoadChunks 는 읽을 수 있는 상태(ready)의 모든 조각이다(색인을 만들 때만 부른다).
func (SQLKnowledge) LoadChunks(ctx context.Context, q contract.Tx) ([]ChunkRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id, c.doc_id, c.entity_id, d.source_kind, d.source_id, d.parent_doc_id, d.title, d.doc_no, c.locator_kind, c.locator, c.heading, c.text, d.version,
		COALESCE(u.kind,''), COALESCE(u.roles,''), COALESCE(u.departments,'')
		FROM knowledge_chunks c JOIN knowledge_docs d ON d.id = c.doc_id LEFT JOIN knowledge_uploads u ON d.source_kind = 'upload' AND u.id = d.source_id WHERE d.extract_state IN ('ready','too_long') ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChunkRow
	for rows.Next() {
		var r ChunkRow
		if err := rows.Scan(&r.ChunkID, &r.DocID, &r.EntityID, &r.Kind, &r.SourceID, &r.ParentDocID, &r.Title, &r.DocNo, &r.LocatorKind, &r.Locator, &r.Heading, &r.Text, &r.Version, &r.UploadKind, &r.Roles, &r.Depts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Status 는 관리 화면의 색인 현황이다(제목 없음).
func (SQLKnowledge) Status(ctx context.Context, q contract.Tx) (domain.KnowledgeStatus, error) {
	st := domain.KnowledgeStatus{Docs: map[string]int{}, States: map[string]int{}}
	rows, err := q.QueryContext(ctx, `SELECT source_kind, extract_state, COUNT(*) FROM knowledge_docs GROUP BY source_kind, extract_state`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var k, s string
		var n int
		if err := rows.Scan(&k, &s, &n); err != nil {
			rows.Close()
			return st, err
		}
		st.Docs[k] += n
		st.States[s] += n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return st, err
	}
	rows.Close()
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_chunks`).Scan(&st.Chunks); err != nil {
		return st, err
	}
	var oldest sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*), MIN(created_at) FROM knowledge_queue WHERE done_at IS NULL`).Scan(&st.QueueOpen, &oldest); err != nil {
		return st, err
	}
	st.OldestQueue = oldest.String
	st.Generation, st.ChangedAt, err = SQLKnowledge{}.Generation(ctx, q)
	return st, err
}

// ---- 결재 문서 ----

// ApprovalKDoc 은 색인할 결재 문서의 본문이다.
type ApprovalKDoc struct {
	ID         int64
	EntityID   int64
	No         string
	Title      string
	FormCode   string
	FormName   string
	Done       bool // 완료(approved·ordered)
	Disposed   bool
	Searchable bool
	Lines      []KLine
}

// KLine 은 소제목 하나와 그 글이다(필드 라벨: 값).
type KLine struct{ Heading, Text string }

// collectLabeled 는 양식 필드의 "라벨: 값" 줄을 모은다. 민감 필드·첨부 필드·조직 번호는 넣지 않는다(collectFieldText 와 같은 규칙).
func collectLabeled(defs []domain.FieldDef, values map[string]any) []KLine {
	var out []KLine
	for _, d := range defs {
		if d.Sensitive || d.Type == domain.FieldAttachment || d.Type == domain.FieldUnit {
			continue
		}
		v, ok := values[d.Key]
		if !ok || v == nil {
			continue
		}
		switch d.Type {
		case domain.FieldTable:
			rows, _ := v.([]any)
			for _, r := range rows {
				cells, _ := r.(map[string]any)
				var parts []string
				for _, c := range collectLabeled(d.Columns, cells) {
					parts = append(parts, c.Heading+": "+c.Text)
				}
				if len(parts) > 0 {
					out = append(out, KLine{Heading: d.Label, Text: strings.Join(parts, " · ")})
				}
			}
		default:
			switch x := v.(type) {
			case string:
				if strings.TrimSpace(x) != "" {
					out = append(out, KLine{Heading: d.Label, Text: x})
				}
			case float64:
				out = append(out, KLine{Heading: d.Label, Text: fmt.Sprintf("%g", x)})
			}
		}
	}
	return out
}

// ApprovalDocForIndex 는 결재 문서 한 건의 색인 재료를 읽는다. 문서가 없으면 found=false. 법인 조건 없이 읽는다(색인 워커는 법인 context 가 없다).
func (SQLKnowledge) ApprovalDocForIndex(ctx context.Context, q contract.Tx, id int64) (d ApprovalKDoc, found bool, err error) {
	var title, reason, fields, vendor, defJSON, status, disposed, formCode, formName string
	var searchable int
	err = q.QueryRowContext(ctx, `SELECT p.entity_id, p.no, p.title, p.reason, p.fields, COALESCE(v.name,''), COALESCE(fv.definition,''), p.status, COALESCE(p.disposed_at,''),
		COALESCE(fm.code,'PUR'), COALESCE(fm.name,'구매 품의서'), COALESCE(fm.searchable,1)
		FROM purchase_requests p LEFT JOIN vendors v ON v.id = p.vendor_id LEFT JOIN approval_forms fm ON fm.id = p.form_id
		LEFT JOIN approval_form_versions fv ON fv.id = p.form_version_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.id = ?`, id).Scan(&d.EntityID, &d.No, &title, &reason, &fields, &vendor, &defJSON, &status, &disposed, &formCode, &formName, &searchable)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.ID, d.Title, d.FormCode, d.FormName = id, title, formCode, formName
	d.Done = status == "approved" || status == "ordered"
	d.Disposed = disposed != ""
	d.Searchable = searchable == 1 && !NeverSearchableForm(formCode)
	if !d.Done || d.Disposed || !d.Searchable {
		return d, true, nil
	}
	if strings.TrimSpace(title) != "" {
		d.Lines = append(d.Lines, KLine{Heading: "제목", Text: title})
	}
	if strings.TrimSpace(reason) != "" {
		d.Lines = append(d.Lines, KLine{Heading: "사유", Text: reason})
	}
	if vendor != "" {
		d.Lines = append(d.Lines, KLine{Heading: "거래처", Text: vendor})
	}
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(i.name,''), l.quantity, l.unit_price FROM purchase_request_lines l LEFT JOIN items i ON i.id = l.item_id WHERE l.pr_id = ? ORDER BY l.id`, id)
	if err != nil {
		return d, true, err
	}
	for rows.Next() {
		var name string
		var qty, price int64
		if err := rows.Scan(&name, &qty, &price); err != nil {
			rows.Close()
			return d, true, err
		}
		if name != "" {
			d.Lines = append(d.Lines, KLine{Heading: "품목", Text: fmt.Sprintf("%s · 수량 %d · 단가 %d", name, qty, price)})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return d, true, err
	}
	rows.Close()
	if defJSON != "" && fields != "" {
		var def domain.FormDefinition
		var values map[string]any
		if json.Unmarshal([]byte(defJSON), &def) == nil && json.Unmarshal([]byte(fields), &values) == nil {
			d.Lines = append(d.Lines, collectLabeled(def.Fields, values)...)
			if strings.TrimSpace(def.Body) != "" {
				d.Lines = append(d.Lines, KLine{Heading: "본문", Text: def.Body})
			}
		}
	}
	return d, true, nil
}

// CompletedDocIDs 는 완료·비폐기 결재 문서의 번호와 법인이다(다시 색인에서 대기열을 채울 때). entityID 가 0 이면 전체.
func (SQLKnowledge) CompletedDocIDs(ctx context.Context, q contract.Tx, entityID int64) (ids, entities []int64, err error) {
	query := `SELECT id, entity_id FROM purchase_requests WHERE ` + contract.SQLEntity(ctx, "purchase_requests") + ` AND status IN ('approved','ordered') AND COALESCE(disposed_at,'') = ''`
	var args []any
	if entityID > 0 {
		query += ` AND entity_id = ?`
		args = append(args, entityID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, e int64
		if err := rows.Scan(&id, &e); err != nil {
			return nil, nil, err
		}
		ids, entities = append(ids, id), append(entities, e)
	}
	return ids, entities, rows.Err()
}

// DocLink 는 검색 결과 링크용 정보다: 구매 품의서(form 1)는 /purchase-requests/{id}, 그 밖은 /docs/{id}. 양식 이름과 번호도 돌려준다.
func (SQLKnowledge) DocLink(ctx context.Context, q contract.Tx, id int64) (link, formName string, err error) {
	var formID sql.NullInt64
	err = q.QueryRowContext(ctx, `SELECT p.form_id, COALESCE(fm.name,'') FROM purchase_requests p LEFT JOIN approval_forms fm ON fm.id = p.form_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.id = ?`, id).Scan(&formID, &formName)
	if err != nil {
		return "", "", err
	}
	if !formID.Valid || formID.Int64 == 1 {
		return fmt.Sprintf("/purchase-requests/%d", id), formName, nil
	}
	return fmt.Sprintf("/docs/%d", id), formName, nil
}

// ---- 열람 판정(한 곳: ViewScope.Cond) ----

// OpenableDocIDs 는 후보 문서 중 호출자가 지금 열 수 있는 것이다. 조건은 ViewScope.Cond 를 맨 앞에 두고, 완료·비폐기·현재 법인·양식의 검색 포함 여부를 더한다.
// 후보가 많아도 한 번의 SQL 이다(최대 500 개씩 나눈다).
func (SQLKnowledge) OpenableDocIDs(ctx context.Context, q contract.Tx, ids []int64, scope ViewScope) (map[int64]bool, error) {
	out := map[int64]bool{}
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		cond, args := scope.Cond()
		ph := make([]string, len(chunk))
		for i, id := range chunk {
			ph[i] = "?"
			args = append(args, id)
		}
		rows, err := q.QueryContext(ctx, `SELECT p.id FROM purchase_requests p LEFT JOIN approval_forms fm ON fm.id = p.form_id
			WHERE `+cond+` AND `+contract.SQLEntity(ctx, "p")+` AND p.status IN ('approved','ordered') AND COALESCE(p.disposed_at,'') = ''
			AND COALESCE(fm.searchable,1) = 1 AND COALESCE(fm.code,'') <> 'LEAVE' AND p.id IN (`+strings.Join(ph, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// SetFormSearchable 은 양식의 "내용 검색에 포함" 값을 바꾼다. 이전 값을 돌려준다.
func (SQLKnowledge) SetFormSearchable(ctx context.Context, q contract.Tx, code string, searchable bool) (old bool, formID int64, err error) {
	var cur int
	err = q.QueryRowContext(ctx, `SELECT id, searchable FROM approval_forms WHERE code = ?`, code).Scan(&formID, &cur)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, contract.ErrNotFound
	}
	if err != nil {
		return false, 0, err
	}
	_, err = q.ExecContext(ctx, `UPDATE approval_forms SET searchable = ? WHERE id = ?`, boolInt(searchable), formID)
	return cur == 1, formID, mapErr(err)
}

// IndexedSourceIDs 는 색인에 올라 있는 지식 문서의 출처 번호다(다시 색인이 더는 대상이 아닌 문서를 정리할 때). entityID 가 0 이면 전체.
func (SQLKnowledge) IndexedSourceIDs(ctx context.Context, q contract.Tx, kind string, entityID int64) ([]int64, error) {
	query := `SELECT source_id FROM knowledge_docs WHERE source_kind = ?`
	args := []any{kind}
	if entityID > 0 {
		query += ` AND entity_id = ?`
		args = append(args, entityID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY source_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- 업로드 문서(매뉴얼·규정, 2단계) ----

// UploadRow 는 업로드 문서 한 건이다. Content 는 내용이 필요한 조회(색인·내려받기)에서만 채운다.
type UploadRow struct {
	ID             int64
	EntityID       int64
	Kind           string
	Title          string
	Slug           string
	Version        int
	ReplacesID     int64
	Roles          string
	Departments    string
	Filename       string
	ContentType    string
	Size           int64
	SHA256         string
	UploadedBy     int64
	UploadedByName string
	UploadedAt     string
	RemovedAt      string
	RemovedReason  string
	Content        []byte
}

const uploadCols = `u.id, u.entity_id, u.kind, u.title, u.slug, u.version, u.replaces_id, u.roles, u.departments, u.filename, u.content_type, u.size, u.sha256, u.uploaded_by, COALESCE(us.name,''), u.uploaded_at, COALESCE(u.removed_at,''), u.removed_reason`

func scanUpload(s rowScanner, content bool) (UploadRow, error) {
	var r UploadRow
	dest := []any{&r.ID, &r.EntityID, &r.Kind, &r.Title, &r.Slug, &r.Version, &r.ReplacesID, &r.Roles, &r.Departments, &r.Filename, &r.ContentType, &r.Size, &r.SHA256, &r.UploadedBy, &r.UploadedByName, &r.UploadedAt, &r.RemovedAt, &r.RemovedReason}
	if content {
		dest = append(dest, &r.Content)
	}
	return r, s.Scan(dest...)
}

// InsertUpload 는 업로드 행을 넣는다(유일 색인: 같은 법인·같은 slug 의 열린 행은 하나).
func (SQLKnowledge) InsertUpload(ctx context.Context, q contract.Tx, r UploadRow, now time.Time) (int64, error) {
	id, err := dbutil.InsertID(ctx, q, `INSERT INTO knowledge_uploads (entity_id, kind, title, slug, version, replaces_id, roles, departments, filename, content_type, size, sha256, content, uploaded_by, uploaded_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.EntityID, r.Kind, r.Title, r.Slug, maxInt(r.Version, 1), r.ReplacesID, r.Roles, r.Departments, r.Filename, r.ContentType, r.Size, r.SHA256, r.Content, r.UploadedBy, fmtTime(now))
	return id, mapErr(err)
}

// UploadByID 는 업로드 한 건이다. 없으면 ErrNotFound.
func (SQLKnowledge) UploadByID(ctx context.Context, q contract.Tx, id int64, content bool) (UploadRow, error) {
	cols := uploadCols
	if content {
		cols += ", u.content"
	}
	r, err := scanUpload(q.QueryRowContext(ctx, `SELECT `+cols+` FROM knowledge_uploads u LEFT JOIN users us ON us.id = u.uploaded_by WHERE u.id = ?`, id), content)
	if errors.Is(err, sql.ErrNoRows) {
		return r, contract.ErrNotFound
	}
	return r, err
}

// ActiveUploadBySlug 는 같은 법인에서 같은 제목(slug)으로 열려 있는 업로드다.
func (SQLKnowledge) ActiveUploadBySlug(ctx context.Context, q contract.Tx, entityID int64, slug string) (UploadRow, bool, error) {
	r, err := scanUpload(q.QueryRowContext(ctx, `SELECT `+uploadCols+` FROM knowledge_uploads u LEFT JOIN users us ON us.id = u.uploaded_by WHERE u.entity_id = ? AND u.slug = ? AND u.removed_at IS NULL`, entityID, slug), false)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// ActiveUploadBySHA 는 같은 법인에 같은 내용(해시)으로 열려 있는 업로드다.
func (SQLKnowledge) ActiveUploadBySHA(ctx context.Context, q contract.Tx, entityID int64, sha string) (UploadRow, bool, error) {
	r, err := scanUpload(q.QueryRowContext(ctx, `SELECT `+uploadCols+` FROM knowledge_uploads u LEFT JOIN users us ON us.id = u.uploaded_by WHERE u.entity_id = ? AND u.sha256 = ? AND u.removed_at IS NULL ORDER BY u.id LIMIT 1`, entityID, sha), false)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// CountActiveUploads 는 법인의 열린 업로드 수다.
func (SQLKnowledge) CountActiveUploads(ctx context.Context, q contract.Tx, entityID int64) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_uploads WHERE entity_id = ? AND removed_at IS NULL`, entityID).Scan(&n)
	return n, err
}

// ListUploads 는 업로드 목록이다(열린 것, 최근 순). entityID 가 0 이 아니면 그 법인과 전 법인 공통(0)만, 0 이면 전부.
func (SQLKnowledge) ListUploads(ctx context.Context, q contract.Tx, entityID int64) ([]UploadRow, error) {
	query := `SELECT ` + uploadCols + ` FROM knowledge_uploads u LEFT JOIN users us ON us.id = u.uploaded_by WHERE u.removed_at IS NULL`
	var args []any
	if entityID > 0 {
		query += ` AND (u.entity_id = ? OR u.entity_id = 0)`
		args = append(args, entityID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY u.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UploadRow
	for rows.Next() {
		r, err := scanUpload(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CloseUpload 는 업로드를 닫는다(삭제 또는 새 버전으로 교체). 삭제(reason removed)는 내용을 비우고, 교체(replaced)는 이력으로 내용을 남긴다. 이미 닫혀 있으면 ErrNotFound.
func (SQLKnowledge) CloseUpload(ctx context.Context, q contract.Tx, id, by int64, reason string, now time.Time) error {
	query := `UPDATE knowledge_uploads SET removed_at = ?, removed_by = ?, removed_reason = ? WHERE id = ? AND removed_at IS NULL`
	args := []any{fmtTime(now), by, reason, id}
	if reason == "removed" {
		query = `UPDATE knowledge_uploads SET removed_at = ?, removed_by = ?, removed_reason = ?, content = ?, filename = '', content_type = '', size = 0, sha256 = '' WHERE id = ? AND removed_at IS NULL`
		args = []any{fmtTime(now), by, reason, []byte{}, id}
	}
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return contract.ErrNotFound
	}
	return nil
}

// SetUploadScope 는 열람 범위(역할·부서 원문)를 바꾼다.
func (SQLKnowledge) SetUploadScope(ctx context.Context, q contract.Tx, id int64, roles, depts string) error {
	res, err := q.ExecContext(ctx, `UPDATE knowledge_uploads SET roles = ?, departments = ? WHERE id = ? AND removed_at IS NULL`, roles, depts, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return contract.ErrNotFound
	}
	return nil
}

// DepartmentNames 는 부서 이름 집합이다(업로드 공개 범위의 부서 이름 확인).
func (SQLKnowledge) DepartmentNames(ctx context.Context, q contract.Tx) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT name FROM departments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// DepartmentName 은 부서 번호의 이름이다(없으면 "").
func (SQLKnowledge) DepartmentName(ctx context.Context, q contract.Tx, id int64) (string, error) {
	var n string
	err := q.QueryRowContext(ctx, `SELECT name FROM departments WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return n, err
}

// UploadDocChunks 는 업로드 문서에 딸린 조각 본문을 순서대로 돌려준다(원문 보기 화면).
func (SQLKnowledge) UploadDocChunks(ctx context.Context, q contract.Tx, uploadID int64) ([]KChunk, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.locator_kind, c.locator, c.heading, c.text FROM knowledge_chunks c JOIN knowledge_docs d ON d.id = c.doc_id
		WHERE d.source_kind = 'upload' AND d.source_id = ? ORDER BY c.seq`, uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KChunk
	for rows.Next() {
		var c KChunk
		if err := rows.Scan(&c.LocatorKind, &c.Locator, &c.Heading, &c.Text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UploadStates 는 업로드 문서(출처 번호)별 색인 상태와 사유다.
func (SQLKnowledge) UploadStates(ctx context.Context, q contract.Tx) (map[int64][2]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT source_id, extract_state, extract_note FROM knowledge_docs WHERE source_kind = 'upload'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][2]string{}
	for rows.Next() {
		var id int64
		var st, note string
		if err := rows.Scan(&id, &st, &note); err != nil {
			return nil, err
		}
		out[id] = [2]string{st, note}
	}
	return out, rows.Err()
}

// EntityNamesByID 는 법인 번호 → 이름이다.
func (SQLKnowledge) EntityNamesByID(ctx context.Context, q contract.Tx) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM entities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ---- 첨부(3단계) ----

// AttachKDoc 은 색인할 첨부 한 건과 부모 결재 문서 정보다.
type AttachKDoc struct {
	ID         int64
	DocID      int64
	Filename   string
	Content    []byte
	EntityID   int64
	DocNo      string
	FormCode   string
	Done       bool
	Disposed   bool
	Searchable bool
}

// AttachmentForIndex 는 첨부 한 건과 부모 문서의 상태를 읽는다. 문서에 붙지 않은 첨부(doc_id 없음)는 없는 것과 같다. 법인 조건은 부모 문서의 것이다.
func (SQLKnowledge) AttachmentForIndex(ctx context.Context, q contract.Tx, id int64) (a AttachKDoc, found bool, err error) {
	var status, disposed string
	var searchable int
	err = q.QueryRowContext(ctx, `SELECT a.id, a.doc_id, a.filename, a.content, p.entity_id, p.no, COALESCE(fm.code,'PUR'), p.status, COALESCE(p.disposed_at,''), COALESCE(fm.searchable,1)
		FROM document_attachments a JOIN purchase_requests p ON p.id = a.doc_id LEFT JOIN approval_forms fm ON fm.id = p.form_id WHERE `+contract.SQLEntity(ctx, "p")+` AND a.id = ?`, id).
		Scan(&a.ID, &a.DocID, &a.Filename, &a.Content, &a.EntityID, &a.DocNo, &a.FormCode, &status, &disposed, &searchable)
	if errors.Is(err, sql.ErrNoRows) {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	a.Done = status == "approved" || status == "ordered"
	a.Disposed = disposed != ""
	a.Searchable = searchable == 1 && !NeverSearchableForm(a.FormCode)
	return a, true, nil
}

// AttachmentIDsOfDoc 은 결재 문서에 붙은 첨부 번호다(폐기로 비워진 첨부는 뺀다).
func (SQLKnowledge) AttachmentIDsOfDoc(ctx context.Context, q contract.Tx, docID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM document_attachments WHERE doc_id = ? AND filename <> '' ORDER BY id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CompletedAttachmentIDs 는 완료·비폐기 결재 문서에 붙은 첨부 번호다(다시 색인). entityID 가 0 이면 전체.
func (SQLKnowledge) CompletedAttachmentIDs(ctx context.Context, q contract.Tx, entityID int64) ([]int64, error) {
	query := `SELECT a.id FROM document_attachments a JOIN purchase_requests p ON p.id = a.doc_id WHERE ` + contract.SQLEntity(ctx, "p") + ` AND p.status IN ('approved','ordered') AND COALESCE(p.disposed_at,'') = '' AND a.filename <> ''`
	var args []any
	if entityID > 0 {
		query += ` AND p.entity_id = ?`
		args = append(args, entityID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY a.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// NeverSearchableForm 은 설정으로 켤 수 없는 양식(내용 검색에서 항상 빠지는 양식)이다. 휴가 신청서(LEAVE)는 사유가 민감해 어떤 경로로도 색인하지 않는다(B50 11절).
func NeverSearchableForm(code string) bool { return code == "LEAVE" }
