// 계정 동기화(SCIM) 관리 화면(B40 명세 12절): 연결·토큰, 동기화 기록. scim:read 로 보고 scim:manage 로 바꾼다.
// 토큰 원문은 발급 직후 한 번만 보여 준다. 동기화 기록의 이메일은 서버가 가려서 준다.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

export interface ScimSettings {
  enabled: boolean;
  groupMode: string;
  defaultUnit: string;
  defaultRank: string;
  defaultRole: string;
  adoptExisting: boolean;
}

export interface ScimToken {
  id: number;
  name: string;
  source: string;
  scope: string;
  prefix: string;
  createdAt: string;
  expiresAt: string;
  revokedAt?: string | null;
  lastUsedAt?: string | null;
  issuerActive: boolean;
}

interface Overview {
  endpoint: string;
  settings: ScimSettings;
  tokens: ScimToken[];
  maxActiveTokens: number;
  sources: string[];
  scopes: string[];
}

export interface SyncLogEntry {
  id: number;
  at: string;
  source: string;
  tokenName: string;
  method: string;
  op: string;
  result: string;
  httpStatus: number;
  scimType: string;
  summary: string;
  warnings: string[];
}

export const SOURCE_LABEL: Record<string, string> = { entra: "Microsoft Entra ID", okta: "Okta", google: "구글 워크스페이스", other: "기타" };
const SCOPE_LABEL: Record<string, string> = { users: "사용자만", users_groups: "사용자와 그룹" };
export const RESULT_LABEL: Record<string, string> = { success: "성공", denied: "거부", conflict: "충돌", error: "오류" };
const RESULT_TONE: Record<string, Tone> = { success: "ok", denied: "warn", conflict: "warn", error: "bad" };
const OP_LABEL: Record<string, string> = {
  group_set: "그룹 멤버 변경", group_remove: "그룹 삭제", create: "입사", reactivate: "재입사", deactivate: "퇴사", transfer: "전보", rank_change: "직위 변경", link: "기존 사용자 연결", update: "정보 갱신", rejected: "요청 거부",
};

function fmt(s?: string | null): string {
  return s ? new Date(s).toLocaleString("ko-KR") : "-";
}

function statusOf(t: ScimToken, now = Date.now()): string {
  if (t.revokedAt) return "폐기됨";
  if (new Date(t.expiresAt).getTime() <= now) return "만료됨";
  return "사용 중";
}
const STATUS_TONE: Record<string, Tone> = { "사용 중": "ok", 폐기됨: "muted", 만료됨: "muted" };

function expiresSoon(t: ScimToken, now = Date.now()): boolean {
  const left = new Date(t.expiresAt).getTime() - now;
  return !t.revokedAt && left > 0 && left < 14 * 24 * 3600 * 1000;
}

export function ScimPage() {
  const { can } = useAuth();
  const [tab, setTab] = useState<"connect" | "groups" | "log">("connect");
  return (
    <section>
      <h1>계정 동기화(SCIM)</h1>
      <p>
        Entra ID·Okta 같은 IdP 에서 입사·퇴사를 자동으로 받아 인사 발령으로 반영합니다. 위험 권한이 있는 계정은 동기화로 바꿀 수 없고, 역할은 줄 수 없습니다.
      </p>
      <div role="tablist" aria-label="계정 동기화" style={{ display: "flex", gap: 8, margin: "12px 0" }}>
        <button type="button" role="tab" className={tab === "connect" ? "" : "btn-secondary"} aria-selected={tab === "connect"} onClick={() => setTab("connect")}>
          연결·토큰
        </button>
        <button type="button" role="tab" className={tab === "groups" ? "" : "btn-secondary"} aria-selected={tab === "groups"} onClick={() => setTab("groups")}>
          그룹 매핑
        </button>
        <button type="button" role="tab" className={tab === "log" ? "" : "btn-secondary"} aria-selected={tab === "log"} onClick={() => setTab("log")}>
          동기화 기록
        </button>
      </div>
      {tab === "connect" && <ConnectTab canManage={can("scim:manage")} />}
      {tab === "groups" && <GroupsTab canManage={can("scim:manage")} />}
      {tab === "log" && <LogTab />}
    </section>
  );
}

function ConnectTab({ canManage }: { canManage: boolean }) {
  const ov = useLoad(() => api.get<Overview>("/api/admin/scim"), []);
  const [form, setForm] = useState<ScimSettings | null>(null);
  const [name, setName] = useState("");
  const [source, setSource] = useState("entra");
  const [scope, setScope] = useState("users");
  const [days, setDays] = useState(180);
  const [fresh, setFresh] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<ScimToken | null>(null);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const d = ov.data;

  useEffect(() => {
    if (d && !form) setForm(d.settings);
  }, [d, form]);

  async function saveSettings(e: FormEvent) {
    e.preventDefault();
    if (!form) return;
    setBusy(true);
    setNote(null);
    try {
      await api.put("/api/admin/scim/settings", form);
      setNote({ tone: "ok", text: "설정을 저장했습니다." });
      await ov.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  async function issue(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setNote(null);
    try {
      const res = await api.post<{ token: string }>("/api/admin/scim/tokens", { name, source, scope, days });
      setFresh(res.token);
      setName("");
      await ov.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  async function revoke() {
    if (!revoking) return;
    const id = revoking.id;
    setRevoking(null);
    setNote(null);
    try {
      await api.del(`/api/admin/scim/tokens/${id}`);
      await ov.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    }
  }

  if (ov.error) return <Notice tone="error">{ov.error}</Notice>;
  if (!d || !form) return <p>불러오는 중…</p>;
  return (
    <div>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      <section className="card" aria-labelledby="scim-conn">
        <h2 id="scim-conn">연결 주소</h2>
        <p>IdP 의 프로비저닝 설정에 아래 주소(테넌트 URL)와 발급한 토큰을 넣습니다.</p>
        <code data-testid="scim-endpoint">{d.endpoint}</code>{" "}
        <button type="button" className="btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(d.endpoint)}>
          복사
        </button>
        <p className="hint">설정 절차는 docs/auth-providers.md 의 계정 동기화 절을 보세요. https 가 아닌 주소는 로컬 개발에서만 쓰세요.</p>
      </section>

      <section className="card" aria-labelledby="scim-settings">
        <h2 id="scim-settings">동기화 설정</h2>
        <form onSubmit={saveSettings}>
          <div className="field">
            <label>
              <input type="checkbox" checked={form.enabled} disabled={!canManage} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} /> 계정 동기화 사용
            </label>
          </div>
          <div className="field">
            <label htmlFor="scim-unit">기본 조직 코드(부서 정보가 없을 때)</label>
            <input id="scim-unit" value={form.defaultUnit} disabled={!canManage} onChange={(e) => setForm({ ...form, defaultUnit: e.target.value })} />
          </div>
          <div className="field">
            <label htmlFor="scim-rank">기본 직위(직위 정보가 없을 때)</label>
            <input id="scim-rank" value={form.defaultRank} disabled={!canManage} onChange={(e) => setForm({ ...form, defaultRank: e.target.value })} />
          </div>
          <div className="field">
            <label htmlFor="scim-role">기본 역할(역할 키)</label>
            <input id="scim-role" value={form.defaultRole} disabled={!canManage} onChange={(e) => setForm({ ...form, defaultRole: e.target.value })} />
          </div>
          <div className="field">
            <label>
              <input type="checkbox" checked={form.adoptExisting} disabled={!canManage} onChange={(e) => setForm({ ...form, adoptExisting: e.target.checked })} /> 같은 이메일의 기존 사용자를 연결
            </label>
          </div>
          <div className="field">
            <label htmlFor="scim-group">그룹 연동</label>
            <select id="scim-group" value={form.groupMode} disabled={!canManage} onChange={(e) => setForm({ ...form, groupMode: e.target.value })} aria-describedby="scim-group-hint">
              <option value="off">사용 안 함</option>
              <option value="unit">그룹 = 조직(멤버가 그 조직으로 전보)</option>
              <option value="role">그룹 = 역할(멤버에게 역할 부여)</option>
            </select>
            <span id="scim-group-hint" className="hint">
              한 번에 하나만 고를 수 있습니다. 조직 방식에서는 사용자의 부서 속성을 무시합니다. 위험 권한이 든 역할은 연결할 수 없습니다. 방식을 바꾸려면 다른 방식의 그룹 매핑을 먼저 지우세요.
            </span>
          </div>
          {canManage && (
            <button type="submit" disabled={busy}>
              설정 저장
            </button>
          )}
        </form>
        <p className="hint">부서·직위·이메일 속성이 없는 입사는 기본 조직·기본 직위가 없으면 거부됩니다. 기본 역할에는 위험 권한이 든 역할을 쓸 수 없습니다.</p>
      </section>

      <section className="card" aria-labelledby="scim-tokens">
        <h2 id="scim-tokens">연동 토큰</h2>
        {canManage && (
          <form onSubmit={issue} className="form-row">
            <label>
              이름 <input value={name} onChange={(e) => setName(e.target.value)} maxLength={80} required />
            </label>
            <label>
              IdP{" "}
              <select value={source} onChange={(e) => setSource(e.target.value)}>
                {d.sources.map((s) => (
                  <option key={s} value={s}>
                    {SOURCE_LABEL[s] ?? s}
                  </option>
                ))}
              </select>
            </label>
            <label>
              범위{" "}
              <select value={scope} onChange={(e) => setScope(e.target.value)}>
                {d.scopes.map((s) => (
                  <option key={s} value={s}>
                    {SCOPE_LABEL[s] ?? s}
                  </option>
                ))}
              </select>
            </label>
            <label>
              유효 기간(일) <input type="number" min={1} max={365} value={days} onChange={(e) => setDays(Number(e.target.value))} />
            </label>
            <button type="submit" disabled={busy}>
              토큰 발급
            </button>
          </form>
        )}
        {fresh && (
          <div role="status" className="notice notice-warn">
            <p>
              <strong>이 토큰은 지금만 볼 수 있습니다.</strong> 창을 닫으면 다시 확인할 수 없습니다. IdP 의 비밀 토큰 칸에 붙여 넣으세요.
            </p>
            <code data-testid="scim-fresh-token">{fresh}</code>{" "}
            <button type="button" className="btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(fresh)}>
              복사
            </button>{" "}
            <button type="button" className="btn-sm" onClick={() => setFresh(null)}>
              저장했습니다
            </button>
          </div>
        )}
        <p className="hint">같은 IdP 의 유효 토큰은 {d.maxActiveTokens > 0 ? `${d.maxActiveTokens}개까지` : "개수 제한 없이"} 둘 수 있어 교체할 때 새 토큰을 먼저 발급하고 IdP 에 넣은 뒤 옛 토큰을 폐기합니다.</p>
        {d.tokens.length === 0 ? (
          <EmptyState hint="위 양식에서 IdP 를 고르고 발급하세요.">발급한 연동 토큰이 없습니다.</EmptyState>
        ) : (
          <TableWrap>
            <table className="stack-table">
              <thead>
                <tr>
                  <th>이름</th>
                  <th>IdP</th>
                  <th>범위</th>
                  <th>토큰 앞부분</th>
                  <th>만료</th>
                  <th>마지막 사용</th>
                  <th>상태</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {d.tokens.map((t) => (
                  <tr key={t.id}>
                    <td data-label="이름">{t.name}</td>
                    <td data-label="IdP">{SOURCE_LABEL[t.source] ?? t.source}</td>
                    <td data-label="범위">{SCOPE_LABEL[t.scope] ?? t.scope}</td>
                    <td data-label="토큰 앞부분">{t.prefix}…</td>
                    <td data-label="만료" className="nowrap">{fmt(t.expiresAt)}</td>
                    <td data-label="마지막 사용" className="nowrap">{fmt(t.lastUsedAt)}</td>
                    <td data-label="상태">
                      <Badge tone={STATUS_TONE[statusOf(t)]}>{statusOf(t)}</Badge>
                      {expiresSoon(t) && <Badge tone="warn">만료 임박</Badge>}
                      {!t.issuerActive && !t.revokedAt && <Badge tone="warn">발급자 퇴사</Badge>}
                    </td>
                    <td>
                      {canManage && statusOf(t) === "사용 중" && (
                        <button type="button" className="btn-danger btn-sm" onClick={() => setRevoking(t)}>
                          폐기
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
      </section>
      {revoking && (
        <ConfirmDialog title="연동 토큰 폐기" confirmLabel="폐기" onConfirm={() => void revoke()} onCancel={() => setRevoking(null)}>
          <p>
            「{revoking.name}」({revoking.prefix}…) 토큰을 폐기합니다. 이 토큰을 쓰는 IdP 는 바로 동기화하지 못하게 되고, 폐기한 토큰은 되살릴 수 없습니다. 교체하려면 새 토큰을 먼저 발급해 IdP 에 넣으세요.
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}

function LogTab() {
  const [result, setResult] = useState("");
  const [source, setSource] = useState("");
  const [fromDay, setFromDay] = useState("");
  const [toDay, setToDay] = useState("");
  const [items, setItems] = useState<SyncLogEntry[]>([]);
  const [more, setMore] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  const load = useCallback(
    async (before?: number) => {
      setLoading(true);
      setError("");
      try {
        const q = new URLSearchParams({ limit: "50" });
        if (result) q.set("result", result);
        if (source) q.set("source", source);
        // 날짜는 한국 시간 하루의 시작(포함)과 다음 날 시작(미포함)으로 서버에 보낸다.
        if (fromDay) q.set("from", new Date(`${fromDay}T00:00:00+09:00`).toISOString());
        if (toDay) q.set("to", new Date(new Date(`${toDay}T00:00:00+09:00`).getTime() + 24 * 3600 * 1000).toISOString());
        if (before) q.set("before", String(before));
        const res = await api.get<{ items: SyncLogEntry[] }>(`/api/admin/scim/log?${q.toString()}`);
        setItems((cur) => (before ? [...cur, ...res.items] : res.items));
        setMore(res.items.length === 50);
      } catch (err) {
        setError(errorText(err));
      } finally {
        setLoading(false);
      }
    },
    [result, source, fromDay, toDay],
  );

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <form className="form-row" onSubmit={(e) => e.preventDefault()}>
        <label>
          결과{" "}
          <select value={result} onChange={(e) => setResult(e.target.value)}>
            <option value="">전체</option>
            {Object.entries(RESULT_LABEL).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <label>
          IdP{" "}
          <select value={source} onChange={(e) => setSource(e.target.value)}>
            <option value="">전체</option>
            {Object.entries(SOURCE_LABEL).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <label>
          시작일 <input type="date" value={fromDay} max={toDay || undefined} onChange={(e) => setFromDay(e.target.value)} />
        </label>
        <label>
          종료일 <input type="date" value={toDay} min={fromDay || undefined} onChange={(e) => setToDay(e.target.value)} />
        </label>
      </form>
      {error && <Notice tone="error">{error}</Notice>}
      {!loading && !error && items.length === 0 ? (
        <EmptyState hint="IdP 가 사용자를 보내면 여기에 성공·거부·충돌이 쌓입니다.">동기화 기록이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table aria-label="동기화 기록" className="stack-table">
            <thead>
              <tr>
                <th>시각</th>
                <th>IdP</th>
                <th>토큰</th>
                <th>종류</th>
                <th>결과</th>
                <th>내용</th>
              </tr>
            </thead>
            <tbody>
              {items.map((e) => (
                <tr key={e.id}>
                  <td data-label="시각" className="nowrap">{fmt(e.at)}</td>
                  <td data-label="IdP">{SOURCE_LABEL[e.source] ?? e.source}</td>
                  <td data-label="토큰">{e.tokenName || "-"}</td>
                  <td data-label="종류">{OP_LABEL[e.op] ?? e.op}</td>
                  <td data-label="결과">
                    <Badge tone={RESULT_TONE[e.result] ?? "muted"}>{RESULT_LABEL[e.result] ?? e.result}</Badge>
                    <span className="hint"> {e.httpStatus}</span>
                  </td>
                  <td data-label="내용">
                    {e.summary}
                    {e.warnings.length > 0 && (
                      <ul className="hint">
                        {e.warnings.map((w, i) => (
                          <li key={i}>{w}</li>
                        ))}
                      </ul>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {more && (
        <button type="button" className="btn-secondary" disabled={loading} onClick={() => void load(items[items.length - 1]?.id)}>
          더 보기
        </button>
      )}
    </div>
  );
}

export interface GroupMapView {
  id: number;
  source: string;
  groupExternalId: string;
  displayName: string;
  kind: "unit" | "role";
  unitCode?: string;
  unitName?: string;
  roleName?: string;
  members: number;
}

interface GroupList {
  mode: string;
  maps: GroupMapView[];
  unmapped: { source: string; groupExternalId: string; displayName: string; at: string }[];
  units: { id: number; code: string; name: string }[];
  roles: { id: number; key: string; name: string; risky: boolean }[];
}

function GroupsTab({ canManage }: { canManage: boolean }) {
  const list = useLoad(() => api.get<GroupList>("/api/admin/scim/group-maps"), []);
  const [source, setSource] = useState("entra");
  const [ext, setExt] = useState("");
  const [display, setDisplay] = useState("");
  const [target, setTarget] = useState(0);
  const [deleting, setDeleting] = useState<GroupMapView | null>(null);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const d = list.data;

  async function create(e: FormEvent) {
    e.preventDefault();
    if (!d) return;
    setBusy(true);
    setNote(null);
    try {
      await api.post("/api/admin/scim/group-maps", { source, groupExternalId: ext, displayName: display, unitId: d.mode === "unit" ? target : 0, roleId: d.mode === "role" ? target : 0 });
      setExt("");
      setDisplay("");
      setTarget(0);
      setNote({ tone: "ok", text: "그룹 매핑을 저장했습니다." });
      await list.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!deleting) return;
    const id = deleting.id;
    setDeleting(null);
    setNote(null);
    try {
      await api.del(`/api/admin/scim/group-maps/${id}`);
      setNote({ tone: "ok", text: "그룹 매핑을 지웠습니다." });
      await list.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    }
  }

  if (list.error) return <Notice tone="error">{list.error}</Notice>;
  if (!d) return <p>불러오는 중…</p>;
  if (d.mode === "off") {
    return <Notice tone="info">그룹 연동이 꺼져 있습니다. 「연결·토큰」 탭의 동기화 설정에서 그룹 연동 방식(조직 또는 역할)을 고르면 여기서 IdP 그룹을 연결할 수 있습니다.</Notice>;
  }
  const targetLabel = d.mode === "unit" ? "연결할 조직" : "연결할 역할";
  return (
    <div>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      <p>
        {d.mode === "unit" ? "그룹에 들어온 멤버가 연결한 조직으로 전보됩니다(효력일은 받은 날)." : "그룹에 들어온 멤버에게 연결한 역할이 부여됩니다. 위험 권한이 든 역할은 연결할 수 없습니다."} 매핑이 없는 그룹은 거부됩니다.
      </p>
      {d.maps.length === 0 ? (
        <EmptyState hint="아래 양식에서 IdP 그룹의 식별자(Entra 는 그룹 개체 ID, Okta 는 그룹 ID)를 넣어 연결하세요.">연결한 그룹이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table className="stack-table" aria-label="그룹 매핑">
            <thead>
              <tr>
                <th>IdP</th>
                <th>그룹</th>
                <th>연결 대상</th>
                <th>멤버</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {d.maps.map((m) => (
                <tr key={m.id}>
                  <td data-label="IdP">{SOURCE_LABEL[m.source] ?? m.source}</td>
                  <td data-label="그룹">
                    {m.displayName}
                    <span className="hint"> {m.groupExternalId}</span>
                  </td>
                  <td data-label="연결 대상">{m.kind === "unit" ? `${m.unitName ?? ""} (${m.unitCode ?? ""})` : m.roleName}</td>
                  <td data-label="멤버">{m.members}명</td>
                  <td>
                    {canManage && (
                      <button type="button" className="btn-danger btn-sm" onClick={() => setDeleting(m)}>
                        삭제
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {d.unmapped.length > 0 && (
        <section className="card" aria-labelledby="scim-unmapped">
          <h2 id="scim-unmapped">매핑이 없는 그룹</h2>
          <p className="hint">IdP 가 보냈지만 매핑이 없어 거부된 그룹입니다.</p>
          <ul>
            {d.unmapped.map((g) => (
              <li key={g.source + g.groupExternalId}>
                {SOURCE_LABEL[g.source] ?? g.source} · {g.groupExternalId}{" "}
                {canManage && (
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    onClick={() => {
                      setSource(g.source);
                      setExt(g.groupExternalId);
                      setDisplay(g.displayName);
                    }}
                  >
                    매핑 만들기
                  </button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
      {canManage && (
        <section className="card" aria-labelledby="scim-newmap">
          <h2 id="scim-newmap">그룹 연결</h2>
          <form onSubmit={create}>
            <div className="field">
              <label htmlFor="gm-source">IdP</label>
              <select id="gm-source" value={source} onChange={(e) => setSource(e.target.value)}>
                {Object.entries(SOURCE_LABEL).map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="gm-ext">그룹 식별자</label>
              <input id="gm-ext" value={ext} onChange={(e) => setExt(e.target.value)} maxLength={255} required />
            </div>
            <div className="field">
              <label htmlFor="gm-display">표시 이름</label>
              <input id="gm-display" value={display} onChange={(e) => setDisplay(e.target.value)} maxLength={200} />
            </div>
            <div className="field">
              <label htmlFor="gm-target">{targetLabel}</label>
              <select id="gm-target" value={target} onChange={(e) => setTarget(Number(e.target.value))} required>
                <option value={0}>선택하세요</option>
                {d.mode === "unit"
                  ? d.units.map((u) => (
                      <option key={u.id} value={u.id}>
                        {u.name} ({u.code})
                      </option>
                    ))
                  : d.roles.map((r) => (
                      <option key={r.id} value={r.id} disabled={r.risky}>
                        {r.name}
                        {r.risky ? " (위험 권한 포함: 연결할 수 없음)" : ""}
                      </option>
                    ))}
              </select>
            </div>
            <button type="submit" disabled={busy || target === 0}>
              연결
            </button>
          </form>
        </section>
      )}
      {deleting && (
        <ConfirmDialog title="그룹 매핑 삭제" confirmLabel="삭제" onConfirm={() => void remove()} onCancel={() => setDeleting(null)}>
          <p>
            「{deleting.displayName}」 매핑을 지웁니다.{" "}
            {deleting.kind === "role" ? "이 매핑으로 부여된 역할은 모두 회수됩니다(사람이 따로 준 부여는 그대로입니다)." : "이미 전보된 사람의 조직은 바뀌지 않습니다."}
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}
