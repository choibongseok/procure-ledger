package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 문서 통합 검색(B29 ②). search_text 는 문서 저장 경로가 같은 트랜잭션에서 RefreshSearchText 로 다시 만든다.
// 권한 조건(ViewScope.Cond)은 쿼리 WHERE 의 맨 앞에 들어가고 그 위에서 검색어를 건다(권한을 나중에 거르지 않는다).

const searchTextMax = 20000

// NormalizeSearch 는 검색어·검색 텍스트의 정규화(소문자, 연속 공백을 하나로)다.
func NormalizeSearch(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// collectFieldText 는 필드 값에서 검색할 글자를 모은다. 민감 필드(와 민감 열)는 넣지 않는다(검색으로 가려진 값을 알아내는 우회 방지). 첨부 필드는 파일 이름이 값이라 넣지 않는다.
func collectFieldText(defs []domain.FieldDef, values map[string]any) []string {
	var out []string
	for _, d := range defs {
		if d.Sensitive || d.Type == domain.FieldAttachment {
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
				out = append(out, collectFieldText(d.Columns, cells)...)
			}
		case domain.FieldUnit:
			// 조직 번호는 검색 대상이 아니다.
		default:
			switch x := v.(type) {
			case string:
				out = append(out, x)
			case float64:
				out = append(out, fmt.Sprintf("%g", x))
			case bool:
			}
		}
	}
	return out
}

// RefreshSearchText 는 문서 한 건의 검색 텍스트를 다시 만든다. 폐기된 문서는 번호·양식·기안자만 남긴다(제목·본문은 없다, 명세 4.4).
func (SQLPurchases) RefreshSearchText(ctx context.Context, q contract.Tx, prID int64) error {
	var no, title, reason, fields, requester, vendor, defJSON, formCode, formName string
	var disposed string
	err := q.QueryRowContext(ctx, `SELECT p.no, p.title, p.reason, p.fields, COALESCE(u.name,''), COALESCE(v.name,''), COALESCE(fv.definition,''), COALESCE(p.disposed_at,''), COALESCE(fm.code,'PUR'), COALESCE(fm.name,'구매 품의서')
		FROM purchase_requests p LEFT JOIN users u ON u.id = p.requester_id LEFT JOIN vendors v ON v.id = p.vendor_id
		LEFT JOIN approval_forms fm ON fm.id = p.form_id
		LEFT JOIN approval_form_versions fv ON fv.id = p.form_version_id WHERE `+contract.SQLEntity(ctx, "p")+` AND p.id = ?`, prID).Scan(&no, &title, &reason, &fields, &requester, &vendor, &defJSON, &disposed, &formCode, &formName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	text := ""
	if disposed != "" {
		text = NormalizeSearch(strings.Join([]string{no, formCode, formName, requester}, " "))
	} else {
		parts := []string{title, no, requester, reason, vendor}
		rows, err := q.QueryContext(ctx, `SELECT COALESCE(i.name,'') FROM purchase_request_lines l LEFT JOIN items i ON i.id = l.item_id WHERE l.pr_id = ? ORDER BY l.id`, prID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			parts = append(parts, n)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if defJSON != "" && fields != "" {
			var def domain.FormDefinition
			var values map[string]any
			if json.Unmarshal([]byte(defJSON), &def) == nil && json.Unmarshal([]byte(fields), &values) == nil {
				parts = append(parts, collectFieldText(def.Fields, values)...)
				parts = append(parts, def.Body)
			}
		}
		text = NormalizeSearch(strings.Join(parts, " "))
		if utf8.RuneCountInString(text) > searchTextMax {
			text = string([]rune(text)[:searchTextMax])
		}
	}
	_, err = q.ExecContext(ctx, `UPDATE purchase_requests SET search_text = ? WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND id = ?`, text, prID)
	return mapErr(err)
}

// BackfillSearchText 는 검색 텍스트가 빈 문서를 채운다(기동 때 한 번, 멱등). 만든 건수를 돌려준다.
func BackfillSearchText(ctx context.Context, q contract.Tx) (int, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM purchase_requests WHERE `+contract.SQLEntity(ctx, "purchase_requests")+` AND search_text = ''`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := (SQLPurchases{}).RefreshSearchText(ctx, q, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// SearchQuery 는 검색 조건이다(Term 은 정규화한 검색어, 비면 조건만으로 찾는다).
type SearchQuery struct {
	Term      string
	From, To  string // 기안일(KST) YYYY-MM-DD
	FormCode  string
	Statuses  []string // draft | in_review(상신·결재 중) | done(승인·발주완료) | rejected | disposed
	Requester string
	Limit     int
	Offset    int
	UserID    int64
}

// likeEscape 는 LIKE 패턴의 %·_·\ 를 이스케이프한다(E6: 문자 그대로 찾는다).
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// SearchDocs 는 열람 범위 안의 문서를 검색한다(최근 변경 내림차순). total 은 조건에 맞는 전체 건수다.
func (SQLPurchases) SearchDocs(ctx context.Context, q contract.Tx, f SearchQuery, scope ViewScope) (rows []domain.DocSearchRow, total int, err error) {
	// 권한 조건이 맨 앞이다. 임시저장은 기안자 본인만 본다.
	var where []string
	var args []any
	if !scope.Unrestricted() {
		cond, cargs := scope.Cond()
		where = append(where, cond)
		args = append(args, cargs...)
	}
	where = append(where, "(p.status <> 'draft' OR p.requester_id = ?)")
	args = append(args, f.UserID)
	if f.Term != "" {
		where = append(where, `lower(p.search_text) LIKE lower(?) ESCAPE '\'`)
		args = append(args, "%"+likeEscape(f.Term)+"%")
	}
	if f.From != "" {
		where = append(where, "date(p.created_at, '+9 hours') >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "date(p.created_at, '+9 hours') <= ?")
		args = append(args, f.To)
	}
	if f.FormCode != "" {
		where = append(where, "COALESCE(fm.code,'PUR') = ?")
		args = append(args, f.FormCode)
	}
	if f.Requester != "" {
		where = append(where, `lower(u.name) LIKE lower(?) ESCAPE '\'`)
		args = append(args, "%"+likeEscape(f.Requester)+"%")
	}
	if len(f.Statuses) > 0 {
		var ors []string
		for _, s := range f.Statuses {
			switch s {
			case "draft":
				ors = append(ors, "(p.status = 'draft' AND COALESCE(p.disposed_at,'') = '')")
			case "in_review":
				ors = append(ors, "(p.status IN ('submitted','in_review'))")
			case "done":
				ors = append(ors, "(p.status IN ('approved','ordered') AND COALESCE(p.disposed_at,'') = '')")
			case "rejected":
				ors = append(ors, "(p.status = 'rejected')")
			case "disposed":
				ors = append(ors, "(COALESCE(p.disposed_at,'') <> '')")
			}
		}
		if len(ors) > 0 {
			where = append(where, "("+strings.Join(ors, " OR ")+")")
		}
	}
	from := ` FROM purchase_requests p JOIN users u ON u.id = p.requester_id AND ` + contract.SQLEntity(ctx, "p") + ` LEFT JOIN approval_forms fm ON fm.id = p.form_id LEFT JOIN approval_lines l ON l.pr_id = p.id AND l.round = p.round
		WHERE ` + strings.Join(where, " AND ")
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	r, err := q.QueryContext(ctx, `SELECT p.id, p.no, p.title, COALESCE(fm.code,'PUR'), COALESCE(fm.name,'구매 품의서'), COALESCE(fm.kind,'purchase'), u.name, COALESCE(l.requester_unit_path,''),
		p.status, COALESCE(p.disposed_at,''), p.created_at, p.updated_at`+from+` ORDER BY p.updated_at DESC, p.id DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()
	for r.Next() {
		var x domain.DocSearchRow
		var status, disposed, created, updated string
		if err := r.Scan(&x.ID, &x.No, &x.Title, &x.FormCode, &x.FormName, &x.FormKind, &x.RequesterName, &x.UnitPath, &status, &disposed, &created, &updated); err != nil {
			return nil, 0, err
		}
		x.Status, x.Disposed, x.CreatedAt, x.UpdatedAt = domain.PRStatus(status), disposed != "", parseTime(created), parseTime(updated)
		rows = append(rows, x)
	}
	return rows, total, r.Err()
}

var _ = time.Second
