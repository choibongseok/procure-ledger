//(업로드)·platform(메뉴·권한). 관리 > 지식 색인의 "업로드 매뉴얼·규정" 절(B47 명세 11.2): 목록, 올리기, 새 버전, 공개 범위, 삭제.
// 공개 범위는 규정 머리말과 같은 의미(역할 AND 부서)다. 영문 상태 코드·내부 경로는 보이지 않는다.
import { useRef, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { apiFetch, api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ROLE_LABELS, type Role } from "../auth/permissions";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap, useToast } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

export interface UploadRow {
  id: number;
  entityId: number;
  entityName: string;
  kind: string;
  kindLabel: string;
  title: string;
  version: number;
  roles: string;
  departments: string;
  filename: string;
  size: number;
  uploadedByName: string;
  uploadedAt: string;
  state: string;
  note: string;
  notice?: string;
  warnings?: string[];
}

export const UPLOAD_STATE_LABEL: Record<string, string> = {
  ready: "검색됨",
  pending: "색인 대기",
  no_text: "글자 없음",
  unreadable: "읽을 수 없음",
  unsupported: "지원하지 않는 형식",
  too_long: "앞부분만 검색됨",
  failed: "색인 실패",
};

const ROLE_CHOICES: Role[] = ["admin", "finance", "manager", "staff", "auditor"];

export function rolesText(v: string): string {
  if (v === "all") return "모든 직원";
  return v
    .split(",")
    .map((r) => ROLE_LABELS[r as Role] ?? r)
    .join("·");
}
export function deptsText(v: string): string {
  return v === "all" ? "모든 부서" : v.split(",").join("·");
}

function Scope({ roles, depts, setRoles, setDepts, allDepts }: { roles: string[] | "all"; depts: string[] | "all"; setRoles: (v: string[] | "all") => void; setDepts: (v: string[] | "all") => void; allDepts: string[] }) {
  const toggle = (cur: string[] | "all", v: string, set: (x: string[] | "all") => void) => {
    const list = cur === "all" ? [] : cur;
    set(list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  };
  return (
    <>
      <fieldset className="field">
        <legend>공개 역할</legend>
        <label>
          <input type="checkbox" checked={roles === "all"} onChange={(e) => setRoles(e.target.checked ? "all" : [])} /> 모든 직원
        </label>
        {ROLE_CHOICES.map((r) => (
          <label key={r}>
            <input type="checkbox" disabled={roles === "all"} checked={roles !== "all" && roles.includes(r)} onChange={() => toggle(roles, r, setRoles)} /> {ROLE_LABELS[r]}
          </label>
        ))}
      </fieldset>
      <fieldset className="field">
        <legend>공개 부서</legend>
        <label>
          <input type="checkbox" checked={depts === "all"} onChange={(e) => setDepts(e.target.checked ? "all" : [])} /> 모든 부서
        </label>
        {allDepts.map((d) => (
          <label key={d}>
            <input type="checkbox" disabled={depts === "all"} checked={depts !== "all" && depts.includes(d)} onChange={() => toggle(depts, d, setDepts)} /> {d}
          </label>
        ))}
      </fieldset>
    </>
  );
}

const scopeValue = (v: string[] | "all") => (v === "all" || v.length === 0 ? "all" : v.join(","));

export function KnowledgeUploads({ onChanged }: { onChanged?: () => void }) {
  const { entities, groupScope, currentEntity } = useAuth();
  const toast = useToast();
  const list = useLoad(() => api.get<{ uploads: UploadRow[] }>("/api/admin/knowledge/uploads"), []);
  const depts = useLoad(() => api.get<{ departments: string[] }>("/api/admin/knowledge/departments"), []);
  const [title, setTitle] = useState("");
  const [kind, setKind] = useState("regulation");
  const [entity, setEntity] = useState("");
  const [roles, setRoles] = useState<string[] | "all">("all");
  const [dept, setDept] = useState<string[] | "all">("all");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [removing, setRemoving] = useState<UploadRow | null>(null);
  const [editing, setEditing] = useState<UploadRow | null>(null);
  const [eRoles, setERoles] = useState<string[] | "all">("all");
  const [eDept, setEDept] = useState<string[] | "all">("all");
  const replaceRef = useRef<HTMLInputElement>(null);
  const [replaceFor, setReplaceFor] = useState<UploadRow | null>(null);
  const multi = (entities?.length ?? 0) > 1;
  const rows = list.data?.uploads ?? [];
  const allDepts = depts.data?.departments ?? [];

  /** 업로드 응답의 경고(개인정보 패턴 등)는 닫을 때까지 화면에 남긴다. 토스트는 사라지기 때문이다. */
  const [warnings, setWarnings] = useState<string[]>([]);

  async function done(msg: string) {
    toast(msg);
    await list.reload();
    onChanged?.();
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (!file) return setError("파일을 선택하세요.");
    const fd = new FormData();
    fd.append("file", file);
    fd.append("title", title);
    fd.append("kind", kind);
    fd.append("entity", entity);
    fd.append("roles", scopeValue(roles));
    fd.append("departments", scopeValue(dept));
    setBusy(true);
    try {
      const res = await apiFetch<UploadRow>("/api/admin/knowledge/uploads", { method: "POST", body: fd });
      setWarnings(res?.warnings ?? []);
      setTitle("");
      setFile(null);
      (document.getElementById("ku-file") as HTMLInputElement | null)?.value && ((document.getElementById("ku-file") as HTMLInputElement).value = "");
      await done("올렸습니다. 색인이 끝나는 몇 초 뒤부터 검색에서 찾을 수 있습니다.");
    } catch (err2) {
      setError(errorText(err2));
    } finally {
      setBusy(false);
    }
  }

  async function replace(f: File | undefined) {
    if (!f || !replaceFor) return;
    const fd = new FormData();
    fd.append("file", f);
    setBusy(true);
    setError("");
    try {
      const res = await apiFetch<UploadRow>(`/api/admin/knowledge/uploads/${replaceFor.id}/replace`, { method: "POST", body: fd });
      setWarnings(res?.warnings ?? []);
      await done(`「${replaceFor.title}」 새 버전을 올렸습니다. 이전 버전은 검색에서 빠졌고, 새 버전은 색인이 끝나는 몇 초 뒤부터 검색됩니다.`);
    } catch (err2) {
      setError(errorText(err2));
    } finally {
      setBusy(false);
      setReplaceFor(null);
      if (replaceRef.current) replaceRef.current.value = "";
    }
  }

  async function saveScope() {
    if (!editing) return;
    setBusy(true);
    setError("");
    try {
      await api.put(`/api/admin/knowledge/uploads/${editing.id}/scope`, { roles: scopeValue(eRoles), departments: scopeValue(eDept) });
      setEditing(null);
      await done("공개 범위를 바꿨습니다. 검색에 바로 반영됩니다.");
    } catch (err2) {
      setError(errorText(err2));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!removing) return;
    setBusy(true);
    setError("");
    try {
      await api.del(`/api/admin/knowledge/uploads/${removing.id}`);
      setRemoving(null);
      await done("삭제했습니다. 검색에서 바로 빠졌습니다.");
    } catch (err2) {
      setError(errorText(err2));
      setRemoving(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h2>업로드 매뉴얼·규정</h2>
      <p className="hint">PDF·DOCX·MD 파일(10MB까지)을 올리면 내용 검색에서 쪽·조항 출처와 함께 찾을 수 있습니다. 스캔한 그림 PDF 는 글자를 읽을 수 없어 올릴 수 없습니다.</p>
      {error && <Notice tone="error">{error}</Notice>}
      {warnings.length > 0 && (
        <Notice tone="warn">
          {warnings.map((w) => (
            <div key={w}>{w}</div>
          ))}
          <button type="button" className="btn-secondary btn-sm" onClick={() => setWarnings([])}>
            확인했습니다
          </button>
        </Notice>
      )}
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.data && rows.length === 0 && <EmptyState>올린 매뉴얼·규정이 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="업로드 매뉴얼·규정">
            <thead>
              <tr>
                <th>제목</th>
                <th className="nowrap">종류</th>
                <th className="nowrap">버전</th>
                {multi && <th className="nowrap">법인</th>}
                <th>공개 범위</th>
                <th className="nowrap">검색 상태</th>
                <th className="nowrap">올린 사람</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((u) => (
                <tr key={u.id}>
                  <td data-label="제목">
                    <Link to={`/knowledge/${u.id}`}>{u.title}</Link>
                    <div className="hint">{u.filename}</div>
                  </td>
                  <td data-label="종류" className="nowrap">{u.kindLabel}</td>
                  <td data-label="버전" className="nowrap">v{u.version}</td>
                  {multi && <td data-label="법인" className="nowrap">{u.entityName}</td>}
                  <td data-label="공개 범위">
                    {rolesText(u.roles)} · {deptsText(u.departments)}
                  </td>
                  <td data-label="검색 상태" className="nowrap">
                    <Badge tone={u.state === "ready" ? "ok" : u.state === "pending" || u.state === "too_long" ? "info" : "warn"}>{UPLOAD_STATE_LABEL[u.state] ?? "확인 필요"}</Badge>
                    {u.note && <div className="hint">{u.note}</div>}
                    {u.notice && <div className="hint">{u.notice}</div>}
                  </td>
                  <td data-label="올린 사람" className="nowrap">
                    {u.uploadedByName}
                    <div className="hint">{new Date(u.uploadedAt).toLocaleDateString("ko-KR")}</div>
                  </td>
                  <td className="nowrap">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={busy}
                      onClick={() => {
                        setReplaceFor(u);
                        replaceRef.current?.click();
                      }}
                    >
                      새 버전 올리기
                    </button>{" "}
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => {
                        setEditing(u);
                        setERoles(u.roles === "all" ? "all" : u.roles.split(","));
                        setEDept(u.departments === "all" ? "all" : u.departments.split(","));
                      }}
                    >
                      공개 범위
                    </button>{" "}
                    <button type="button" className="btn-secondary btn-sm" onClick={() => setRemoving(u)}>
                      삭제
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <input ref={replaceRef} type="file" accept=".pdf,.docx,.md" hidden aria-label="새 버전 파일" onChange={(e) => void replace(e.target.files?.[0])} />

      {editing && (
        <div className="card" role="group" aria-label="공개 범위 변경">
          <h3>「{editing.title}」 공개 범위</h3>
          <Scope roles={eRoles} depts={eDept} setRoles={setERoles} setDepts={setEDept} allDepts={allDepts} />
          <div className="form-row">
            <button type="button" disabled={busy} onClick={() => void saveScope()}>
              저장
            </button>
            <button type="button" className="btn-secondary" onClick={() => setEditing(null)}>
              취소
            </button>
          </div>
        </div>
      )}

      <h3>새 문서 올리기</h3>
      <form onSubmit={(e) => void submit(e)} aria-label="업로드 양식">
        <Notice tone="info">문서에 개인정보(연락처·계좌·주민등록번호)가 들어 있지 않은지 확인하세요. 검색 발췌는 가려서 보이지만 원문은 열람 가능한 사람에게 그대로 보입니다.</Notice>
        <div className="form-row">
          <div className="field">
            <label htmlFor="ku-file">파일</label>
            <input id="ku-file" type="file" accept=".pdf,.docx,.md" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
          </div>
          <div className="field">
            <label htmlFor="ku-title">제목</label>
            <input id="ku-title" value={title} maxLength={100} onChange={(e) => setTitle(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="ku-kind">종류</label>
            <select id="ku-kind" value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="regulation">규정</option>
              <option value="manual">매뉴얼</option>
            </select>
          </div>
          {multi && (
            <div className="field">
              <label htmlFor="ku-entity">법인</label>
              <select id="ku-entity" value={entity} onChange={(e) => setEntity(e.target.value)}>
                <option value="">현재 법인{currentEntity && !currentEntity.all ? `(${currentEntity.name ?? ""})` : ""}</option>
                {entities?.map((x) => (
                  <option key={x.code} value={x.code}>
                    {x.name}
                  </option>
                ))}
                {groupScope && <option value="*">모든 법인 공통</option>}
              </select>
            </div>
          )}
        </div>
        <Scope roles={roles} depts={dept} setRoles={setRoles} setDepts={setDept} allDepts={allDepts} />
        <button type="submit" disabled={busy}>
          올리기
        </button>
      </form>

      {removing && (
        <ConfirmDialog title="문서를 삭제할까요?" confirmLabel="삭제" busy={busy} onCancel={() => setRemoving(null)} onConfirm={() => void remove()}>
          「{removing.title}」를 삭제하면 검색에서 바로 사라지고 원본 파일도 지워집니다. 되돌릴 수 없습니다.
        </ConfirmDialog>
      )}
    </>
  );
}
