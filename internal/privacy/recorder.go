package privacy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

// atLayout 은 고정 폭 UTC 시각이다(소수 9자리 고정: 문자열 순서가 곧 시간 순서라 기간 비교가 정확하다).
const atLayout = "2006-01-02T15:04:05.000000000Z"

const lockKey = "privacy-log" // PG 에서 조회 기록 체인 이어 붙이기를 직렬화하는 잠금(감사 체인 잠금과 별개)

var (
	categories = map[string]bool{CatContact: true, CatAccount: true, CatCard: true, CatEmployeeNo: true, CatAttendance: true}
	subjTypes  = map[string]bool{SubjectUser: true, SubjectVendor: true, SubjectCompany: true, SubjectPayment: true, SubjectCounterparty: true}
	fieldNames = map[string]bool{FieldEmail: true, FieldPhone: true, FieldAccountNo: true, FieldBusinessNo: true, FieldCardTxn: true, FieldEmployeeNo: true, FieldLeaveReason: true, FieldAbsenceReason: true, FieldProofFile: true}
	channels   = map[string]bool{"web": true, "mcp": true, "llmgw": true, "cli": true, "scim": true, "api": true}
)

// Options 는 Recorder 구성이다.
type Options struct {
	DB      *sql.DB
	Txs     contract.TxManager
	Audit   audit.Recorder
	Deliver func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error // 메일·웹훅 발송 건(B36). 비면 앱 안 알림만
	Now     func() time.Time                                                                                         // 시험이 시계를 주입한다. 비면 time.Now
}

// Recorder 는 조회 기록 저장·검증·보관·조회를 모두 하는 서비스다(Noter 구현).
type Recorder struct {
	db      *sql.DB
	txs     contract.TxManager
	audit   audit.Recorder
	now     func() time.Time
	deliver func(ctx context.Context, tx contract.Tx, notificationID, userID int64, kind string, at time.Time) error
}

var _ Noter = (*Recorder)(nil)

// New 는 Recorder 를 만든다.
func New(o Options) (*Recorder, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil {
		return nil, errors.New("privacy: DB·Txs·Audit 가 필요합니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Recorder{db: o.DB, txs: o.Txs, audit: o.Audit, now: o.Now, deliver: o.Deliver}, nil
}

func sum(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(h[:])
}

// subjectsHash 는 정렬한 정보주체 목록의 해시다(정보주체 행 변조도 행 해시에 묶인다).
func subjectsHash(subs []Subject) string {
	keys := make([]string, 0, len(subs))
	for _, s := range subs {
		keys = append(keys, fmt.Sprintf("%s:%d", s.Type, s.ID))
	}
	sort.Strings(keys)
	return sum(keys...)
}

// rowHash 는 한 행의 해시다. 정보주체 목록(subjectsHash)과 직전 행 해시를 포함한다.
func rowHash(prev, at string, actor int64, role, channel string, token int64, category, screen, dataset string, export int, fields string, count int, subjHash, exposure string) string {
	return sum(prev, at, fmt.Sprint(actor), role, channel, fmt.Sprint(token), category, screen, dataset, fmt.Sprint(export), fields, fmt.Sprint(count), subjHash, exposure)
}

func normalize(p contract.Principal, a Access) (Access, string, error) {
	if !categories[a.Category] {
		return a, "", fmt.Errorf("privacy: 알 수 없는 범주 %q", a.Category)
	}
	if !ValidScreen(a.Screen) {
		return a, "", fmt.Errorf("privacy: 허용되지 않은 화면 이름 %q", a.Screen)
	}
	seen := map[string]bool{}
	var fields []string
	for _, f := range a.Fields {
		if !fieldNames[f] {
			return a, "", fmt.Errorf("privacy: 알 수 없는 필드 %q", f)
		}
		if !seen[f] {
			seen[f] = true
			fields = append(fields, f)
		}
	}
	sort.Strings(fields)
	if fields == nil {
		fields = []string{}
	}
	fj, _ := json.Marshal(fields)
	dedup := map[Subject]bool{}
	subs := make([]Subject, 0, len(a.Subjects))
	for _, s := range a.Subjects {
		if !subjTypes[s.Type] || s.ID <= 0 {
			return a, "", fmt.Errorf("privacy: 잘못된 정보주체 %v", s)
		}
		if !dedup[s] {
			dedup[s] = true
			subs = append(subs, s)
		}
	}
	a.Subjects = subs
	if a.UnknownSubjects < 0 {
		a.UnknownSubjects = 0
	}
	return a, string(fj), nil
}

// Note 는 읽기 경로용 기록이다(자체 짧은 트랜잭션).
func (r *Recorder) Note(ctx context.Context, p contract.Principal, a Access) error {
	a, _, err := normalize(p, a)
	if err != nil {
		return err
	}
	if len(a.Subjects)+a.UnknownSubjects == 0 && !a.Anomaly { // MCP 이상 상태는 정보주체를 몰라도 행을 남긴다
		return nil
	}
	return r.txs.WithTx(ctx, func(tx contract.Tx) error { return r.NoteTx(ctx, tx, p, a) })
}

// lastHash 는 체인의 끝 해시다. 행이 하나도 없으면 가장 최근에 파기한 달의 머리 해시, 그것도 없으면 빈 문자열이다.
func lastHash(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (string, error) {
	var h string
	err := q.QueryRowContext(ctx, `SELECT hash FROM privacy_access_log ORDER BY id DESC LIMIT 1`).Scan(&h)
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = q.QueryRowContext(ctx, `SELECT head_hash FROM privacy_log_segments ORDER BY last_id DESC LIMIT 1`).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return h, err
}

const subjectBatch = 500

// NoteTx 는 열린 트랜잭션 안에서 기록한다(체인 이어 붙이기 + 정보주체 배치 INSERT).
func (r *Recorder) NoteTx(ctx context.Context, tx contract.Tx, p contract.Principal, a Access) error {
	a, fieldsJSON, err := normalize(p, a)
	if err != nil {
		return err
	}
	if len(a.Subjects)+a.UnknownSubjects == 0 && !a.Anomaly {
		return nil
	}
	if err := dbutil.AcquireKeyLock(ctx, tx, lockKey); err != nil {
		return err
	}
	prev, err := lastHash(ctx, tx)
	if err != nil {
		return err
	}
	at := r.now().UTC().Format(atLayout)
	channel := string(p.Channel)
	if !channels[channel] {
		channel = "web"
	}
	exposure := "raw"
	if a.Anomaly {
		exposure = "mcp_anomaly"
	}
	export := 0
	if a.Export {
		export = 1
	}
	count := len(a.Subjects) + a.UnknownSubjects
	role := string(p.Role)
	sh := subjectsHash(a.Subjects)
	h := rowHash(prev, at, p.UserID, role, channel, p.TokenID, a.Category, a.Screen, a.Dataset, export, fieldsJSON, count, sh, exposure)
	var token any
	if p.TokenID != 0 {
		token = p.TokenID
	}
	id, err := dbutil.InsertID(ctx, tx, `INSERT INTO privacy_access_log (at, actor_id, actor_role, channel, token_id, category, screen, dataset, is_export, fields, subject_count, subjects_hash, exposure, prev_hash, hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, at, p.UserID, role, channel, token, a.Category, a.Screen, a.Dataset, export, fieldsJSON, count, sh, exposure, prev, h)
	if err != nil {
		return err
	}
	for i := 0; i < len(a.Subjects); i += subjectBatch {
		end := i + subjectBatch
		if end > len(a.Subjects) {
			end = len(a.Subjects)
		}
		var sb strings.Builder
		args := make([]any, 0, (end-i)*3)
		sb.WriteString(`INSERT INTO privacy_access_subjects (log_id, subject_type, subject_id) VALUES `)
		for j := i; j < end; j++ {
			if j > i {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?)")
			args = append(args, id, a.Subjects[j].Type, a.Subjects[j].ID)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}
