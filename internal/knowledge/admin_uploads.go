package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/policysearch"
	"github.com/choibongseok/procure-ledger/internal/rbac"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 업로드 매뉴얼·규정 관리(B47 2단계, 명세 4.3·11.2). 권한 knowledge:manage. 저장 전에 추출을 시험해 읽지 못하면 한국어 사유로 거부한다.
// 삭제·교체는 같은 트랜잭션에서 조각을 지우고 세대를 올린다(검색에서 바로 빠진다). 새 문서의 색인은 대기열을 거친다.

// MaxUploadBytes 는 업로드 파일 하나의 상한이다(D15: 첨부와 같은 10MB).
const MaxUploadBytes = 10 << 20

// MaxUploadsPerEntity 는 법인당 열려 있는 업로드 문서 상한이다(D15). 시험이 낮춰 쓸 수 있게 변수다.
var MaxUploadsPerEntity = 500

func invalid(msg string) error { return fmt.Errorf("%w: %s", contract.ErrValidation, msg) }

func normSlug(title string) string { return strings.Join(strings.Fields(strings.ToLower(title)), " ") }

// scopeOf 는 입력의 열람 범위를 검증해 정규화한다(비면 all). 부서 이름은 존재해야 한다.
func (a *Admin) scopeOf(ctx context.Context, roles, depts string) (string, string, error) {
	roles, depts = strings.TrimSpace(roles), strings.TrimSpace(depts)
	if roles == "" {
		roles = "all"
	}
	if depts == "" {
		depts = "all"
	}
	d, err := policysearch.ParseScope(roles, depts)
	if err != nil {
		return "", "", invalid("공개 범위가 올바르지 않습니다. 역할은 기본 역할 이름이나 권한 이름, 부서는 등록된 부서 이름이어야 합니다")
	}
	if !d.AllDepts {
		names, err := a.repo.DepartmentNames(ctx, a.DB)
		if err != nil {
			return "", "", err
		}
		for _, n := range d.Depts {
			if !names[n] {
				return "", "", invalid("공개 범위의 부서를 찾을 수 없습니다: " + n)
			}
		}
	}
	return rawScope(d.AllRoles, d.Roles, roles), rawScope(d.AllDepts, d.Depts, depts), nil
}

func rawScope(all bool, list []string, fallback string) string {
	if all {
		return "all"
	}
	if len(list) == 0 {
		return fallback
	}
	return strings.Join(list, ",")
}

// targetEntity 는 올릴 법인을 정한다: 비면 현재 법인, "*" 는 모든 법인 공통(그룹 범위만), 코드는 이 권한이 그 법인에서 효력일 때만.
func (a *Admin) targetEntity(ctx context.Context, p contract.Principal, code string) (int64, error) {
	set := p.EntitiesFor(contract.PermKnowledgeManage)
	switch code = strings.ToUpper(strings.TrimSpace(code)); code {
	case "":
		return contract.WriteEntityID(ctx), nil
	case "*":
		if !set.All {
			return 0, fmt.Errorf("%w: 모든 법인 공통 문서는 그룹 범위 관리자만 올릴 수 있습니다", contract.ErrForbidden)
		}
		return 0, nil
	}
	var id int64
	if err := a.DB.QueryRowContext(ctx, `SELECT id FROM entities WHERE code = ?`, code).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return 0, invalid("모르는 법인 코드입니다")
	} else if err != nil {
		return 0, err
	}
	if !set.Has(id) {
		return 0, fmt.Errorf("%w: 이 법인에는 올릴 권한이 없습니다", contract.ErrForbidden)
	}
	return id, nil
}

// editable 은 기존 업로드를 고칠 수 있는지다: 열려 있고, 법인이 이 권한 범위 안(모든 법인 공통은 그룹 범위만)이어야 한다. 아니면 없는 것과 같은 ErrNotFound.
func (a *Admin) editable(ctx context.Context, p contract.Principal, id int64) (repository.UploadRow, error) {
	u, err := a.repo.UploadByID(ctx, a.DB, id, false)
	if err != nil {
		return u, err
	}
	set := p.EntitiesFor(contract.PermKnowledgeManage)
	if u.RemovedAt != "" || (u.EntityID == 0 && !set.All) || (u.EntityID != 0 && !set.Has(u.EntityID)) {
		return u, contract.ErrNotFound
	}
	if ent, scoped := entityScope(ctx); scoped && u.EntityID != 0 && u.EntityID != ent {
		return u, contract.ErrNotFound
	}
	return u, nil
}

// MsgIndexing 은 업로드 직후 색인 대기 중에 보이는 안내다(워커가 몇 초마다 대기열을 처리한다).
const MsgIndexing = "색인 중입니다. 보통 몇 초 안에 검색에 나오며, 그 전에는 검색에서 찾지 못할 수 있습니다."

// MsgPIIWarning 은 업로드한 파일에 개인정보 패턴이 있을 때 관리자에게 보이는 경고다.
const MsgPIIWarning = "이 파일에 이메일·전화번호·계좌번호로 보이는 내용이 있습니다. 공개 범위 안의 사람에게는 원문 그대로 보이고, 그 열람은 개인정보 열람 기록에 남습니다. 꼭 필요한 내용인지, 공개 범위가 알맞은지 확인하세요."

func checkFile(filename string, content []byte) (warnings []string, err error) {
	if Ext(filename) == "" {
		return nil, invalid(MsgFormat)
	}
	if len(content) > MaxUploadBytes {
		return nil, invalid("파일이 너무 큽니다(최대 10MB).")
	}
	if len(content) == 0 {
		return nil, invalid(MsgEmpty)
	}
	secs, err := ExtractUpload(filename, content)
	if err != nil {
		var xe *ExtractError
		if errors.As(err, &xe) {
			return nil, invalid(xe.Msg)
		}
		return nil, err
	}
	for _, s := range secs {
		if rbac.DetectPII(s.Heading + "\n" + s.Text).Any() {
			return []string{MsgPIIWarning}, nil
		}
	}
	return nil, nil
}

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (a *Admin) toUpload(ctx context.Context, u repository.UploadRow) (domain.KnowledgeUpload, error) {
	names, err := a.repo.EntityNamesByID(ctx, a.DB)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	states, err := a.repo.UploadStates(ctx, a.DB)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	return uploadView(u, names, states), nil
}

func uploadView(u repository.UploadRow, names map[int64]string, states map[int64][2]string) domain.KnowledgeUpload {
	v := domain.KnowledgeUpload{ID: u.ID, EntityID: u.EntityID, EntityName: names[u.EntityID], Kind: u.Kind, Title: u.Title, Version: u.Version, Roles: u.Roles, Departments: u.Departments,
		Filename: u.Filename, Size: u.Size, UploadedByName: u.UploadedByName, UploadedAt: u.UploadedAt, State: "pending", Notice: MsgIndexing}
	if u.EntityID == 0 {
		v.EntityName = "모든 법인"
	}
	if st, ok := states[u.ID]; ok {
		v.State, v.Note = st[0], st[1]
		if v.State != "pending" {
			v.Notice = ""
		}
	}
	return v
}

// Uploads 는 열려 있는 업로드 목록이다.
func (a *Admin) Uploads(ctx context.Context) ([]domain.KnowledgeUpload, error) {
	p, err := a.authorize(ctx, "knowledge.upload_list")
	if err != nil {
		return nil, err
	}
	var entityID int64
	if ent, scoped := entityScope(ctx); scoped {
		entityID = ent
	} else if !p.EntitiesFor(contract.PermKnowledgeManage).All {
		entityID = contract.WriteEntityID(ctx)
	}
	rows, err := a.repo.ListUploads(ctx, a.DB, entityID)
	if err != nil {
		return nil, err
	}
	names, err := a.repo.EntityNamesByID(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	states, err := a.repo.UploadStates(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	out := make([]domain.KnowledgeUpload, 0, len(rows))
	for _, r := range rows {
		out = append(out, uploadView(r, names, states))
	}
	return out, nil
}

// Upload 는 새 매뉴얼·규정을 올린다.
func (a *Admin) Upload(ctx context.Context, in domain.KnowledgeUploadInput) (domain.KnowledgeUpload, error) {
	p, err := a.authorize(ctx, "knowledge.upload")
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	if in.Kind != "manual" && in.Kind != "regulation" {
		return domain.KnowledgeUpload{}, invalid("종류는 매뉴얼 또는 규정 중에서 고르세요")
	}
	title := strings.TrimSpace(in.Title)
	if n := utf8.RuneCountInString(title); n < 1 || n > 100 {
		return domain.KnowledgeUpload{}, invalid("제목은 1~100자로 입력하세요")
	}
	warnings, err := checkFile(in.Filename, in.Content)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	roles, depts, err := a.scopeOf(ctx, in.Roles, in.Departments)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	entityID, err := a.targetEntity(ctx, p, in.Entity)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	var id int64
	err = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if entityID > 0 {
			if err := repository.EnsureEntityWritable(ctx, tx, entityID); err != nil {
				if errors.Is(err, contract.ErrNotFound) {
					return invalid("모르는 법인입니다")
				}
				return err
			}
		}
		if n, err := a.repo.CountActiveUploads(ctx, tx, entityID); err != nil {
			return err
		} else if n >= MaxUploadsPerEntity {
			return invalid(fmt.Sprintf("업로드 문서가 최대 개수(%d개)에 도달했습니다", MaxUploadsPerEntity))
		}
		sha := shaOf(in.Content)
		if dup, found, err := a.repo.ActiveUploadBySHA(ctx, tx, entityID, sha); err != nil {
			return err
		} else if found {
			return invalid("같은 파일이 이미 올라와 있습니다(제목: " + dup.Title + ")")
		}
		slug := normSlug(title)
		if _, found, err := a.repo.ActiveUploadBySlug(ctx, tx, entityID, slug); err != nil {
			return err
		} else if found {
			return invalid("같은 제목의 문서가 이미 있습니다. 「새 버전 올리기」로 올려 주세요")
		}
		ext := Ext(in.Filename)
		id, err = a.repo.InsertUpload(ctx, tx, repository.UploadRow{EntityID: entityID, Kind: in.Kind, Title: title, Slug: slug, Version: 1, Roles: roles, Departments: depts,
			Filename: strings.TrimSpace(in.Filename), ContentType: ContentTypeOf(ext), Size: int64(len(in.Content)), SHA256: sha, Content: in.Content, UploadedBy: p.UserID}, a.Worker.now())
		if err != nil {
			return err
		}
		if err := a.repo.Enqueue(ctx, tx, "upsert", repository.KSrcUpload, id, entityID, "uploaded", a.Worker.now()); err != nil {
			return err
		}
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "knowledge.upload", TargetType: audit.TargetKnowledge, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "kind", New: in.Kind}, {Key: "title", New: title}, {Key: "filename", New: in.Filename}, {Key: "size", New: strconv.Itoa(len(in.Content))},
				{Key: "roles", New: roles}, {Key: "departments", New: depts}, {Key: "entity", New: strconv.FormatInt(entityID, 10)}}})
	})
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	u, err := a.repo.UploadByID(ctx, a.DB, id, false)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	return a.withWarnings(ctx, u, warnings)
}

// Replace 는 같은 문서의 새 버전을 올린다. 이전 버전은 닫히고(이력으로 남음) 조각이 바로 지워진다.
func (a *Admin) Replace(ctx context.Context, id int64, filename string, content []byte) (domain.KnowledgeUpload, error) {
	p, err := a.authorize(ctx, "knowledge.replace")
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	old, err := a.editable(ctx, p, id)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	warnings, err := checkFile(filename, content)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	var newID int64
	err = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		sha := shaOf(content)
		if dup, found, err := a.repo.ActiveUploadBySHA(ctx, tx, old.EntityID, sha); err != nil {
			return err
		} else if found {
			return invalid("같은 파일이 이미 올라와 있습니다(제목: " + dup.Title + ")")
		}
		now := a.Worker.now()
		if err := a.repo.CloseUpload(ctx, tx, old.ID, p.UserID, "replaced", now); err != nil {
			return err
		}
		ext := Ext(filename)
		newID, err = a.repo.InsertUpload(ctx, tx, repository.UploadRow{EntityID: old.EntityID, Kind: old.Kind, Title: old.Title, Slug: old.Slug, Version: old.Version + 1, ReplacesID: old.ID,
			Roles: old.Roles, Departments: old.Departments, Filename: strings.TrimSpace(filename), ContentType: ContentTypeOf(ext), Size: int64(len(content)), SHA256: sha, Content: content, UploadedBy: p.UserID}, now)
		if err != nil {
			return err
		}
		if _, err := a.repo.DeleteDoc(ctx, tx, repository.KSrcUpload, old.ID); err != nil {
			return err
		}
		if err := a.repo.BumpGeneration(ctx, tx, now); err != nil {
			return err
		}
		if err := a.repo.Enqueue(ctx, tx, "upsert", repository.KSrcUpload, newID, old.EntityID, "replaced", now); err != nil {
			return err
		}
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "knowledge.replace", TargetType: audit.TargetKnowledge, TargetID: strconv.FormatInt(newID, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "title", New: old.Title}, {Key: "version", Old: strconv.Itoa(old.Version), New: strconv.Itoa(old.Version + 1)}, {Key: "filename", New: filename}, {Key: "size", New: strconv.Itoa(len(content))}}})
	})
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	u, err := a.repo.UploadByID(ctx, a.DB, newID, false)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	return a.withWarnings(ctx, u, warnings)
}

// Remove 는 업로드 문서를 지운다(표시와 내용 비움). 조각은 같은 트랜잭션에서 바로 지워 검색에서 빠진다.
func (a *Admin) Remove(ctx context.Context, id int64) error {
	p, err := a.authorize(ctx, "knowledge.remove")
	if err != nil {
		return err
	}
	u, err := a.editable(ctx, p, id)
	if err != nil {
		return err
	}
	return a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		now := a.Worker.now()
		if err := a.repo.CloseUpload(ctx, tx, id, p.UserID, "removed", now); err != nil {
			return err
		}
		if _, err := a.repo.DeleteDoc(ctx, tx, repository.KSrcUpload, id); err != nil {
			return err
		}
		if err := a.repo.BumpGeneration(ctx, tx, now); err != nil {
			return err
		}
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "knowledge.remove", TargetType: audit.TargetKnowledge, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "title", New: u.Title}, {Key: "version", New: strconv.Itoa(u.Version)}}})
	})
}

// SetScope 는 열람 범위를 바꾼다. 색인은 그대로이고(권한은 색인에 없다) 세대만 올려 다른 프로세스의 사전 거름이 따라온다. 검색 재확인은 DB 값을 바로 읽는다.
func (a *Admin) SetScope(ctx context.Context, id int64, roles, departments string) (domain.KnowledgeUpload, error) {
	p, err := a.authorize(ctx, "knowledge.scope")
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	u, err := a.editable(ctx, p, id)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	nr, nd, err := a.scopeOf(ctx, roles, departments)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	err = a.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := a.repo.SetUploadScope(ctx, tx, id, nr, nd); err != nil {
			return err
		}
		if err := a.repo.BumpGeneration(ctx, tx, a.Worker.now()); err != nil {
			return err
		}
		return a.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "knowledge.scope", TargetType: audit.TargetKnowledge, TargetID: strconv.FormatInt(id, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "roles", Old: u.Roles, New: nr}, {Key: "departments", Old: u.Departments, New: nd}}})
	})
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	u, err = a.repo.UploadByID(ctx, a.DB, id, false)
	if err != nil {
		return domain.KnowledgeUpload{}, err
	}
	return a.toUpload(ctx, u)
}

// Departments 는 공개 범위에 쓸 수 있는 부서 이름이다(이름순).
func (a *Admin) Departments(ctx context.Context) ([]string, error) {
	if _, err := a.authorize(ctx, "knowledge.upload_list"); err != nil {
		return nil, err
	}
	names, err := a.repo.DepartmentNames(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// withWarnings 는 업로드·새 버전 응답에 개인정보 경고를 더한다(목록에는 없다).
func (a *Admin) withWarnings(ctx context.Context, u repository.UploadRow, warnings []string) (domain.KnowledgeUpload, error) {
	v, err := a.toUpload(ctx, u)
	if err != nil {
		return v, err
	}
	v.Warnings = warnings
	return v, nil
}
