// 역할 관리(/admin/roles, 권한 role:manage): 역할 표·권한 선택·부여(사람·조직·직위·조직장)·영향 미리보기·위험 권한 2인 확인·권한 출처.
// 기본 템플릿은 읽기 전용이고 복제해서 쓴다. 권한 판정은 서버가 하고 이 화면은 보이고 보내기만 한다.
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { parseAuthError } from "../auth/authApi";
import { Badge, ConfirmDialog, Notice, TableWrap } from "../layout/ui";
import { PERMISSION_LABELS } from "./auditLabels";

interface RoleView {
  id: number;
  key: string;
  name: string;
  description: string;
  kind: "template" | "custom";
  version: number;
  permissions: string[];
  grantCount: number;
  userCount: number;
}
interface PermInfo {
  code: string;
  group: string;
  risk: boolean;
}
interface GrantView {
  id: number;
  roleId: number;
  targetType: string;
  label: string;
  includeDescendants: boolean;
  source: string;
  reason: string;
  users: number;
  scope?: string;
  scopeLabel?: string;
}
interface Targets {
  users: { id: number; name: string; email: string }[];
  units: { id: number; name: string; kind: string }[];
  ranks: { id: number; name: string }[];
  entities?: { id: number; code: string; name: string }[];
}
interface Preview {
  affectedUsers: number;
  newlyGained: number;
  sample: string[];
  riskPermissions: string[];
  needsApproval: boolean;
}
interface RoleRequest {
  id: number;
  summary: string;
  reason: string;
  riskPermissions: string[];
  requester: string;
  requestedAt: string;
  expiresAt: string;
  status: string;
}
interface UserAccess {
  userId: number;
  name: string;
  roles: string[];
  permissions: string[];
  sources: { permission: string; roleName: string; via: string }[];
}

const GROUP_LABELS: Record<string, string> = {
  purchase: "구매", approval: "결재", inventory: "재고", document: "증빙 문서", audit: "감사", eval: "평가", policy: "규정", usage: "AI 사용량",
  llm: "LLM", token: "개인 토큰", user: "사용자", mask: "마스킹", org: "조직도", notification: "알림", form: "결재 양식", draft: "양식 문서",
  inbox: "문서함", receipt: "수신", authority: "전결", budget: "예산", vendor: "거래처", report: "보고서", retention: "보존·폐기", ledger: "전표·원장",
  account: "계정과목", auth: "로그인 설정", payment: "지급", fund: "자금", card: "법인카드", vat: "부가세", role: "역할 관리", company: "회사 설정", import: "기초 데이터 가져오기",
};
const KIND_LABELS: Record<string, string> = { company: "회사", division: "본부", department: "부서", team: "팀" };
const TARGET_TYPE_LABELS: Record<string, string> = { user: "사람", unit: "조직", rank: "직위", unit_head: "조직장" };

function label(code: string): string {
  return PERMISSION_LABELS[code] ?? code;
}
function fmt(s: string): string {
  return new Date(s).toLocaleString("ko-KR");
}

export function RolesPage() {
  const [roles, setRoles] = useState<RoleView[]>([]);
  const [perms, setPerms] = useState<PermInfo[]>([]);
  const [targets, setTargets] = useState<Targets>({ users: [], units: [], ranks: [] });
  const [requests, setRequests] = useState<RoleRequest[]>([]);
  const [selected, setSelected] = useState<number | "new" | null>(null);
  const [error, setError] = useState("");
  const [info, setInfo] = useState("");
  const [deleting, setDeleting] = useState<RoleView | null>(null);

  const load = useCallback(async () => {
    try {
      const [r, p, t, q] = await Promise.all([
        api.get<{ roles: RoleView[] }>("/api/admin/roles"),
        api.get<{ permissions: PermInfo[] }>("/api/admin/permissions"),
        api.get<Targets>("/api/admin/role-targets"),
        api.get<{ requests: RoleRequest[] }>("/api/admin/role-requests"),
      ]);
      setRoles(r.roles ?? []);
      setPerms(p.permissions ?? []);
      setTargets({ users: t.users ?? [], units: t.units ?? [], ranks: t.ranks ?? [], entities: t.entities ?? [] });
      setRequests(q.requests ?? []);
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const current = typeof selected === "number" ? roles.find((r) => r.id === selected) : undefined;
  const pending = requests.filter((q) => q.status === "pending");

  async function decide(q: RoleRequest, approve: boolean) {
    setError("");
    setInfo("");
    try {
      if (approve) {
        await api.post(`/api/admin/role-requests/${q.id}/approve`);
        setInfo("요청을 승인해 적용했습니다.");
      } else {
        const reason = window.prompt("반려 사유를 입력하세요");
        if (!reason) return;
        await api.post(`/api/admin/role-requests/${q.id}/reject`, { reason });
        setInfo("요청을 반려했습니다.");
      }
      await load();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  async function clone(r: RoleView) {
    const name = window.prompt(`'${r.name}' 을(를) 복제합니다. 새 역할 이름을 입력하세요`, `${r.name} 복사`);
    if (!name) return;
    setError("");
    try {
      await api.post(`/api/admin/roles/${r.id}/clone`, { name });
      setInfo("복제했습니다. 권한을 고쳐 쓰세요.");
      await load();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  async function remove() {
    if (!deleting) return;
    const r = deleting;
    setDeleting(null);
    setError("");
    try {
      await api.del(`/api/admin/roles/${r.id}`);
      setInfo(`'${r.name}' 을(를) 지웠습니다.`);
      if (selected === r.id) setSelected(null);
      await load();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  return (
    <section>
      <h1>역할 관리</h1>
      <p>
        역할은 권한 묶음입니다. 기본 템플릿 5개(관리자·재무·결재자·직원·감사)는 고칠 수 없고, 복제해서 회사에 맞는 역할을 만드세요. 사람·조직·직위·조직장에게 부여하면 그 사람의 권한은 부여된 역할의 합집합이 됩니다.{" "}
        <Link to="/admin/roles/separation">권한 분리 점검</Link>
      </p>
      {error && <Notice tone="error">{error}</Notice>}
      {info && <Notice tone="ok">{info}</Notice>}

      {pending.length > 0 && (
        <section className="card" aria-labelledby="pending-title">
          <h2 id="pending-title">승인 대기 요청(위험 권한)</h2>
          <p className="hint">위험 권한이 든 부여·변경은 요청한 사람이 아닌 다른 역할 관리자가 24시간 안에 승인해야 적용됩니다.</p>
          <ul>
            {pending.map((q) => (
              <li key={q.id}>
                <strong>{q.summary}</strong> — 요청: {q.requester}, {fmt(q.requestedAt)}, 만료 {fmt(q.expiresAt)}
                <br />
                위험 권한: {q.riskPermissions.map(label).join(", ")} · 사유: {q.reason}{" "}
                <button type="button" className="btn-sm" onClick={() => void decide(q, true)}>
                  승인
                </button>{" "}
                <button type="button" className="btn-secondary btn-sm" onClick={() => void decide(q, false)}>
                  반려
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}

      <p>
        <button type="button" onClick={() => setSelected("new")}>
          새 역할 만들기
        </button>
      </p>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th>역할</th>
              <th>종류</th>
              <th>권한</th>
              <th>부여</th>
              <th>사용 인원</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {roles.map((r) => (
              <tr key={r.id} aria-selected={selected === r.id}>
                <td>{r.name}</td>
                <td>{r.kind === "template" ? <Badge tone="info">기본 템플릿</Badge> : <Badge tone="muted">사용자 정의</Badge>}</td>
                <td className="num">{r.permissions.length}개</td>
                <td className="num">{r.grantCount}건</td>
                <td className="num">{r.userCount}명</td>
                <td className="nowrap">
                  <button type="button" className="btn-secondary btn-sm" onClick={() => setSelected(r.id)} aria-label={`${r.name} 보기·편집`}>
                    {r.kind === "template" ? "보기" : "편집"}
                  </button>{" "}
                  <button type="button" className="btn-secondary btn-sm" onClick={() => void clone(r)} aria-label={`${r.name} 복제`}>
                    복제
                  </button>{" "}
                  {r.kind === "custom" && (
                    <button type="button" className="btn-danger btn-sm" onClick={() => setDeleting(r)} aria-label={`${r.name} 삭제`}>
                      삭제
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>

      {selected !== null && (
        <RoleEditor
          key={String(selected) + (current?.version ?? "")}
          role={selected === "new" ? undefined : current}
          perms={perms}
          setError={setError}
          setInfo={setInfo}
          onSaved={async (id) => {
            await load();
            if (id) setSelected(id);
          }}
        />
      )}
      {current && <GrantsPanel role={current} targets={targets} setError={setError} setInfo={setInfo} onChanged={load} />}
      <UserAccessPanel users={targets.users} setError={setError} />
      {deleting && (
        <ConfirmDialog title="역할 삭제" confirmLabel="삭제" onConfirm={() => void remove()} onCancel={() => setDeleting(null)}>
          '{deleting.name}' 역할을 지웁니다. 부여가 남아 있으면 지워지지 않습니다.
        </ConfirmDialog>
      )}
    </section>
  );
}

function RoleEditor({
  role,
  perms,
  setError,
  setInfo,
  onSaved,
}: {
  role?: RoleView;
  perms: PermInfo[];
  setError: (m: string) => void;
  setInfo: (m: string) => void;
  onSaved: (id?: number) => Promise<void>;
}) {
  const readOnly = role?.kind === "template";
  const [name, setName] = useState(role?.name ?? "");
  const [desc, setDesc] = useState(role?.description ?? "");
  const [sel, setSel] = useState<Set<string>>(new Set(role?.permissions ?? []));
  const [reason, setReason] = useState("");
  const groups = useMemo(() => {
    const g = new Map<string, PermInfo[]>();
    for (const p of perms) g.set(p.group, [...(g.get(p.group) ?? []), p]);
    return [...g.entries()];
  }, [perms]);
  const addedRisk = perms.filter((p) => p.risk && sel.has(p.code) && !(role?.permissions ?? []).includes(p.code));

  function toggle(code: string) {
    const n = new Set(sel);
    if (n.has(code)) n.delete(code);
    else n.add(code);
    setSel(n);
  }

  async function save() {
    setError("");
    setInfo("");
    const body = { name, description: desc, permissions: [...sel], version: role?.version ?? 0, reason };
    try {
      if (!role) {
        const r = await api.post<RoleView>("/api/admin/roles", body);
        setInfo("역할을 만들었습니다. 이제 사람·조직 등에게 부여하세요.");
        await onSaved(r.id);
      } else {
        const r = await api.put<{ role?: RoleView; request?: { id: number } }>(`/api/admin/roles/${role.id}`, body);
        setInfo(r?.request ? "위험 권한이 더해져 다른 관리자의 승인을 기다립니다. 승인 전에는 적용되지 않습니다." : "역할을 저장했습니다.");
        await onSaved(role.id);
      }
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  return (
    <section className="card" aria-labelledby="editor-title">
      <h2 id="editor-title">{role ? (readOnly ? `${role.name} (기본 템플릿, 읽기 전용)` : `${role.name} 편집`) : "새 역할"}</h2>
      <div className="field">
        <label htmlFor="role-name">이름</label>
        <input id="role-name" value={name} disabled={readOnly} maxLength={100} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="field">
        <label htmlFor="role-desc">설명</label>
        <input id="role-desc" value={desc} disabled={readOnly} maxLength={500} onChange={(e) => setDesc(e.target.value)} />
      </div>
      <fieldset disabled={readOnly}>
        <legend>권한({sel.size}개 선택)</legend>
        {groups.map(([g, list]) => (
          <div key={g} className="field">
            <strong>{GROUP_LABELS[g] ?? g}</strong>
            <div>
              {list.map((p) => (
                <label key={p.code} className="nowrap" style={{ marginRight: "var(--sp-4)" }}>
                  <input type="checkbox" checked={sel.has(p.code)} onChange={() => toggle(p.code)} /> {label(p.code)}
                  {p.risk && <Badge tone="warn">위험</Badge>}
                </label>
              ))}
            </div>
          </div>
        ))}
      </fieldset>
      {!readOnly && addedRisk.length > 0 && role && role.grantCount > 0 && (
        <div className="field">
          <Notice tone="warn">이미 부여된 역할에 위험 권한({addedRisk.map((p) => label(p.code)).join(", ")})을 더하면 다른 관리자의 승인이 필요합니다. 사유를 입력하세요.</Notice>
          <label htmlFor="role-reason">사유</label>
          <input id="role-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
      )}
      {!readOnly && (
        <button type="button" onClick={() => void save()}>
          {role ? "저장" : "만들기"}
        </button>
      )}
    </section>
  );
}

function GrantsPanel({
  role,
  targets,
  setError,
  setInfo,
  onChanged,
}: {
  role: RoleView;
  targets: Targets;
  setError: (m: string) => void;
  setInfo: (m: string) => void;
  onChanged: () => Promise<void>;
}) {
  const [grants, setGrants] = useState<GrantView[]>([]);
  const [type, setType] = useState("user");
  const [targetId, setTargetId] = useState(0);
  const [kind, setKind] = useState("team");
  const [desc, setDesc] = useState(false);
  const [reason, setReason] = useState("");
  const [scope, setScope] = useState("own"); // 적용 범위(B44): own 소속 법인 전체 | entity 특정 법인 | group 그룹 전체
  const [scopeEntity, setScopeEntity] = useState(0);
  const [preview, setPreview] = useState<Preview | null>(null);
  const multiEntity = (targets.entities?.length ?? 0) > 1; // 법인이 하나뿐이면 범위 선택을 그리지 않는다(이전과 같은 화면)

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ grants: GrantView[] }>(`/api/admin/roles/${role.id}/grants`);
      setGrants(r.grants ?? []);
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }, [role.id, setError]);
  useEffect(() => {
    void load();
  }, [load]);

  const body = () => ({
    targetType: type,
    targetId: type === "unit_head" ? 0 : targetId,
    unitKind: type === "unit_head" ? kind : "",
    includeDescendants: type === "unit" && desc,
    reason,
    ...(multiEntity ? { scope, scopeEntityId: scope === "entity" ? scopeEntity : 0 } : {}),
  });

  async function doPreview() {
    setError("");
    setInfo("");
    try {
      setPreview(await api.post<Preview>(`/api/admin/roles/${role.id}/grants/preview`, body()));
    } catch (e) {
      setPreview(null);
      setError(parseAuthError(e).message);
    }
  }
  async function add() {
    setError("");
    setInfo("");
    try {
      const r = await api.post<{ affectedUsers: number; request?: { id: number }; solo?: boolean }>(`/api/admin/roles/${role.id}/grants`, body());
      setInfo(r?.request ? "위험 권한이 든 역할이라 다른 관리자의 승인을 기다립니다." : `부여했습니다(영향 ${r?.affectedUsers ?? 0}명).${r?.solo ? " 관리자가 한 명뿐이라 2인 확인 없이 적용했고 감사 로그에 표시됩니다." : ""}`);
      setPreview(null);
      setReason("");
      await load();
      await onChanged();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }
  async function revoke(g: GrantView) {
    setError("");
    setInfo("");
    try {
      await api.del(`/api/admin/role-grants/${g.id}`);
      setInfo(`'${g.label}' 부여를 회수했습니다.`);
      await load();
      await onChanged();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  const options =
    type === "user"
      ? targets.users.map((u) => ({ id: u.id, text: `${u.name} (${u.email})` }))
      : type === "unit"
        ? targets.units.map((u) => ({ id: u.id, text: `${u.name} (${KIND_LABELS[u.kind] ?? u.kind})` }))
        : type === "rank"
          ? targets.ranks.map((r) => ({ id: r.id, text: r.name }))
          : [];

  return (
    <section className="card" aria-labelledby="grants-title">
      <h2 id="grants-title">'{role.name}' 부여</h2>
      {grants.length === 0 ? (
        <p>부여된 곳이 없습니다.</p>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>대상</th>
                <th>유형</th>
                <th>영향 인원</th>
                <th>사유</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {grants.map((g) => (
                <tr key={g.id}>
                  <td>
                    {g.label}
                    {g.includeDescendants && " (하위 포함)"}
                    {multiEntity && g.scopeLabel && <span className="hint"> · {g.scopeLabel}</span>}
                  </td>
                  <td>
                    {TARGET_TYPE_LABELS[g.targetType] ?? g.targetType} {g.source === "compat" && <Badge tone="muted">기본 역할</Badge>}
                  </td>
                  <td className="num">{g.users}명</td>
                  <td>{g.reason}</td>
                  <td>
                    <button type="button" className="btn-danger btn-sm" onClick={() => void revoke(g)} aria-label={`${g.label} 부여 회수`}>
                      회수
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <h3>새로 부여</h3>
      <div className="form-row">
        <label>
          대상 유형
          <select
            value={type}
            onChange={(e) => {
              setType(e.target.value);
              setTargetId(0);
              setPreview(null);
            }}
          >
            {Object.entries(TARGET_TYPE_LABELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        {type === "unit_head" ? (
          <label>
            조직 종류
            <select value={kind} onChange={(e) => setKind(e.target.value)}>
              {Object.entries(KIND_LABELS).map(([k, v]) => (
                <option key={k} value={k}>
                  {v}
                </option>
              ))}
            </select>
          </label>
        ) : (
          <label>
            대상
            <select value={targetId} onChange={(e) => setTargetId(Number(e.target.value))}>
              <option value={0}>선택…</option>
              {options.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.text}
                </option>
              ))}
            </select>
          </label>
        )}
        {type === "unit" && (
          <label className="nowrap">
            <input type="checkbox" checked={desc} onChange={(e) => setDesc(e.target.checked)} /> 하위 조직 포함
          </label>
        )}
        {multiEntity && (
          <label>
            적용 범위
            <select
              value={scope}
              onChange={(e) => {
                setScope(e.target.value);
                setPreview(null);
              }}
            >
              <option value="own">소속 법인 전체</option>
              <option value="entity">특정 법인</option>
              <option value="group">그룹 전체(모든 법인)</option>
            </select>
          </label>
        )}
        {multiEntity && scope === "entity" && (
          <label>
            법인
            <select value={scopeEntity} onChange={(e) => setScopeEntity(Number(e.target.value))}>
              <option value={0}>선택…</option>
              {(targets.entities ?? []).map((en) => (
                <option key={en.id} value={en.id}>
                  {en.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <label>
          사유
          <input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={300} />
        </label>
        <button type="button" className="btn-secondary" onClick={() => void doPreview()}>
          영향 미리보기
        </button>
      </div>
      {preview && (
        <div role="status" className="notice notice-info" data-testid="grant-preview">
          <p>
            영향 {preview.affectedUsers}명, 새로 권한이 생기는 사람 {preview.newlyGained}명
            {preview.sample.length > 0 && ` (${preview.sample.join(", ")}${preview.newlyGained > preview.sample.length ? " 외" : ""})`}
          </p>
          {scope === "group" && multiEntity && <p>그룹 전체 범위는 모든 법인에서 효력이 있어 다른 관리자의 승인이 필요합니다.</p>}
          {preview.riskPermissions.length > 0 && (
            <p>
              위험 권한: {preview.riskPermissions.map(label).join(", ")} —{" "}
              {preview.needsApproval ? "다른 관리자의 승인이 필요합니다(사유 필수)." : "관리자가 한 명뿐이라 2인 확인 없이 사유만 받고 적용됩니다."}
            </p>
          )}
          <button type="button" onClick={() => void add()}>
            {preview.needsApproval ? "승인 요청 보내기" : "부여"}
          </button>
        </div>
      )}
    </section>
  );
}

function UserAccessPanel({ users, setError }: { users: { id: number; name: string; email: string }[]; setError: (m: string) => void }) {
  const [uid, setUid] = useState(0);
  const [acc, setAcc] = useState<UserAccess | null>(null);
  async function show(id: number) {
    setUid(id);
    setAcc(null);
    if (!id) return;
    try {
      setAcc(await api.get<UserAccess>(`/api/admin/users/${id}/access`));
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }
  const byPerm = new Map<string, string[]>();
  for (const s of acc?.sources ?? []) byPerm.set(s.permission, [...(byPerm.get(s.permission) ?? []), `${s.roleName} ← ${s.via}`]);
  return (
    <section className="card" aria-labelledby="access-title">
      <h2 id="access-title">이 사람의 권한이 어디서 왔는가</h2>
      <label>
        사용자
        <select value={uid} onChange={(e) => void show(Number(e.target.value))}>
          <option value={0}>선택…</option>
          {users.map((u) => (
            <option key={u.id} value={u.id}>
              {u.name} ({u.email})
            </option>
          ))}
        </select>
      </label>
      {acc && (
        <>
          <p>
            역할: {acc.roles.length ? acc.roles.join(", ") : "없음"} · 권한 {acc.permissions.length}개
          </p>
          <TableWrap>
            <table>
              <thead>
                <tr>
                  <th>권한</th>
                  <th>출처(역할 ← 부여)</th>
                </tr>
              </thead>
              <tbody>
                {[...byPerm.entries()].map(([p, from]) => (
                  <tr key={p}>
                    <td>{label(p)}</td>
                    <td>{[...new Set(from)].join(" · ")}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </>
      )}
    </section>
  );
}
