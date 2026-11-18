// 연동 관리 화면(B42 명세 15절): 연동 API 켜기·끄기·속도 설정, API 토큰 발급·폐기. api:manage(관리자).
// 토큰 원문은 발급 직후 한 번만 보여 준다. 위험 안내: 토큰은 발급하는 사람의 권한이 아니라 고른 범위의 읽기 권한만 가지며 발급자 권한이 줄어도 자동으로 줄지 않는다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { useAuth } from "../auth/AuthProvider";
import { errorText, useLoad } from "../purchase/useLoad";

interface Settings {
  enabled: boolean;
  ratePerMin: number;
  writePerMin: number;
  defaultRate: number;
  maxActiveTokens: number;
  maxConcurrent: number;
  globalCidr: boolean;
  rehearsal: boolean;
  changesKeepDays: number;
  openapiPath: string;
}

interface ScopeInfo {
  name: string;
  title: string;
  held: boolean;
  write: boolean;
  note: string;
  warning: string;
}

interface Token {
  id: number;
  name: string;
  prefix: string;
  scopes: string[];
  createdBy: number;
  createdByName: string;
  createdAt: string;
  expiresAt: string;
  revokedAt?: string | null;
  lastUsedAt?: string | null;
  allowCidr: string;
  entityId?: number; // 0 = 그룹 전체(읽기만)
  entityCode?: string;
  status: "active" | "expired" | "revoked";
  calls24h: number;
  denied24h: number;
  warnings: string[];
}

const STATUS: Record<Token["status"], { text: string; tone: Tone }> = {
  active: { text: "사용 중", tone: "ok" },
  expired: { text: "만료", tone: "muted" },
  revoked: { text: "폐기", tone: "bad" },
};

const WARNING_TEXT: Record<string, string> = {
  expiring: "만료 임박",
  issuer_left: "발급자 퇴사",
  issuer_reduced: "발급자 권한이 줄었습니다",
};

const fmt = (s?: string | null) => (s ? new Date(s).toLocaleString("ko-KR") : "-");

// 읽기 범위의 안내. 쓰기 범위(가져오기·전표 초안)의 안내와 발급자별 경고는 서버가 준다(note·warning).
const SCOPE_NOTE: Record<string, string> = {
  "purchase_orders:read": "다른 부서의 발주까지 전사 범위로 읽습니다.",
  "receipts:read": "다른 부서의 입고까지 전사 범위로 읽습니다.",
  "journals:read": "금액이 포함된 전표를 읽습니다.",
  "payables:read": "금액이 포함된 채무를 읽습니다.",
};

export function IntegrationsPage() {
  const settings = useLoad(() => api.get<Settings>("/api/admin/integrations/settings"), []);
  const scopes = useLoad(() => api.get<ScopeInfo[]>("/api/admin/integrations/scopes"), []);
  const tokens = useLoad(() => api.get<Token[]>("/api/admin/integrations/tokens"), []);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [days, setDays] = useState(180);
  const [cidr, setCidr] = useState("");
  // 토큰이 묶일 법인(B44): ""=내 현재 법인, "*"=그룹 전체(읽기만), 숫자=그 법인. 고를 수 있는 법인이 둘 이상일 때만 보인다.
  const { entities = [], groupScope = false } = useAuth();
  const multi = entities.length >= 2;
  const [entityPick, setEntityPick] = useState("");
  const [fresh, setFresh] = useState<string | null>(null);
  const [freshWarnings, setFreshWarnings] = useState<string[]>([]);
  const [writeConfirm, setWriteConfirm] = useState(false);
  const [revoking, setRevoking] = useState<Token | null>(null);
  const [rate, setRate] = useState<string>("");
  const [writeRate, setWriteRate] = useState<string>("");

  async function run(fn: () => Promise<unknown>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      if (ok) setNote({ tone: "ok", text: ok });
      await Promise.all([settings.reload(), tokens.reload()]);
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  function toggle(name: string) {
    setPicked((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]));
  }

  const pickedWrite = (scopes.data ?? []).filter((sc) => sc.write && picked.includes(sc.name));

  function submitIssue(e: FormEvent) {
    e.preventDefault();
    if (pickedWrite.length > 0) setWriteConfirm(true); // 쓰기 범위는 한 번 더 확인한다
    else void issue();
  }

  async function issue() {
    setWriteConfirm(false);
    setBusy(true);
    setNote(null);
    try {
      const res = await api.post<{ token: string; warnings?: string[] }>("/api/admin/integrations/tokens", { name, scopes: picked, days, allowCidr: cidr, ...(entityPick === "*" ? { group: true } : entityPick ? { entityId: Number(entityPick) } : {}) });
      setFresh(res.token);
      setFreshWarnings(res.warnings ?? []);
      setName("");
      setPicked([]);
      setCidr("");
      setEntityPick("");
      await tokens.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  const s = settings.data;
  const list = tokens.data ?? [];
  return (
    <section>
      <h1>연동 관리</h1>
      <p>
        외부 시스템이 읽기 전용 REST API(<code>/api/v1</code>)로 데이터를 가져가도록 토큰을 발급합니다. 토큰은 발급하는 분의 권한이 아니라 고른 범위의 읽기 권한만 가지며, 사람 정보와 사업자번호·계좌·연락처 원문은 나가지 않습니다.
      </p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {settings.error && <Notice tone="error">{settings.error}</Notice>}
      {s?.rehearsal && <Notice tone="warn">복구 리허설 모드라 연동 API 가 꺼져 있습니다.</Notice>}

      {s && (
        <section className="card" aria-labelledby="api-settings">
          <h2 id="api-settings">연동 API 설정</h2>
          <p>
            상태: <Badge tone={s.enabled ? "ok" : "muted"}>{s.enabled ? "켜짐" : "꺼짐"}</Badge>{" "}
            <button type="button" className={s.enabled ? "btn-secondary btn-sm" : "btn-sm"} disabled={busy || s.rehearsal} onClick={() => void run(() => api.put("/api/admin/integrations/settings", { enabled: !s.enabled }), s.enabled ? "연동 API 를 껐습니다. 모든 호출이 거부됩니다." : "연동 API 를 켰습니다.")}>
              {s.enabled ? "끄기" : "켜기"}
            </button>
          </p>
          <form
            className="form-row"
            onSubmit={(e: FormEvent) => {
              e.preventDefault();
              void run(() => api.put("/api/admin/integrations/settings", { ratePerMin: Number(rate || s.ratePerMin) }), "분당 한도를 바꿨습니다.");
            }}
          >
            <label>
              토큰당 분당 읽기 호출 한도{" "}
              <input type="number" min={10} max={6000} value={rate === "" ? s.ratePerMin : rate} onChange={(e) => setRate(e.target.value)} />
            </label>
            <button type="submit" className="btn-secondary" disabled={busy}>
              한도 저장
            </button>
          </form>
          <form
            className="form-row"
            onSubmit={(e: FormEvent) => {
              e.preventDefault();
              void run(() => api.put("/api/admin/integrations/settings", { writePerMin: Number(writeRate || s.writePerMin) }), "분당 쓰기 한도를 바꿨습니다.");
            }}
          >
            <label>
              토큰당 분당 쓰기 호출 한도{" "}
              <input type="number" min={1} max={600} value={writeRate === "" ? s.writePerMin : writeRate} onChange={(e) => setWriteRate(e.target.value)} />
            </label>
            <button type="submit" className="btn-secondary" disabled={busy}>
              쓰기 한도 저장
            </button>
            <span className="hint"> 가져오기·전표 초안 같은 쓰기 호출만 세며 읽기 한도와 따로입니다.</span>
          </form>
          <p className="hint">
            유효 토큰은 {s.maxActiveTokens}개까지, 서버 전체 동시 요청은 {s.maxConcurrent}건까지입니다. 변경 피드는 {s.changesKeepDays}일 보관합니다.
            {s.globalCidr ? " 서버 전체 허용 출발지가 설정되어 있습니다." : ""} OpenAPI 문서: <a href={s.openapiPath}>{s.openapiPath}</a>
          </p>
        </section>
      )}

      <section className="card" aria-labelledby="api-tokens">
        <h2 id="api-tokens">API 토큰</h2>
        <form onSubmit={submitIssue} aria-label="토큰 발급">
          <div className="field">
            <label htmlFor="tok-name">이름</label>
            <input id="tok-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={80} required />
            <span className="hint">어떤 연동에 쓰는지 알아볼 수 있게 적으세요(예: 회계 시스템 야간 동기화).</span>
          </div>
          <fieldset className="field">
            <legend>범위</legend>
            {(scopes.data ?? []).map((sc) => (
              <div key={sc.name}>
                <label>
                  <input type="checkbox" checked={picked.includes(sc.name)} disabled={!sc.held} onChange={() => toggle(sc.name)} /> {sc.title}
                </label>{" "}
                {sc.write && <Badge tone="warn">쓰기</Badge>}
                {!sc.held && <span className="hint"> 발급하는 분에게 필요한 권한이 없어 고를 수 없습니다.</span>}
                {sc.note && picked.includes(sc.name) && <div className="hint">{sc.note}</div>}
                {sc.warning && picked.includes(sc.name) && <Notice tone="warn">{sc.warning}</Notice>}
                {SCOPE_NOTE[sc.name] && picked.includes(sc.name) && <span className="hint"> {SCOPE_NOTE[sc.name]}</span>}
              </div>
            ))}
          </fieldset>
          <div className="field">
            <label htmlFor="tok-days">유효 기간(일)</label>
            <input id="tok-days" type="number" min={1} max={365} value={days} onChange={(e) => setDays(Number(e.target.value))} />
          </div>
          <div className="field">
            <label htmlFor="tok-cidr">허용 출발지(선택)</label>
            <input id="tok-cidr" value={cidr} onChange={(e) => setCidr(e.target.value)} placeholder="예: 203.0.113.0/24, 198.51.100.7" />
            <span className="hint">비우면 서버 전체 설정만 적용합니다.</span>
          </div>
          {multi && (
            <div className="field">
              <label htmlFor="tok-entity">법인</label>
              <select id="tok-entity" value={entityPick} onChange={(e) => setEntityPick(e.target.value)}>
                <option value="">내 현재 법인</option>
                {entities.map((e) => (
                  <option key={e.id} value={e.id}>
                    {e.name}
                  </option>
                ))}
                {groupScope && <option value="*">그룹 전체(읽기만)</option>}
              </select>
              <span className="hint">토큰은 고른 법인의 데이터만 읽고 씁니다. 그룹 전체는 모든 법인을 읽기만 합니다.</span>
            </div>
          )}
          <button type="submit" disabled={busy || picked.length === 0}>
            토큰 발급
          </button>
        </form>
        {fresh && (
          <div role="status" className="notice notice-warn">
            <p>
              <strong>이 토큰은 지금만 볼 수 있습니다.</strong> 창을 닫으면 다시 확인할 수 없습니다. 연동 시스템의 비밀 설정에 붙여 넣으세요.
            </p>
            {freshWarnings.map((w) => (
              <p key={w}>
                <Badge tone="warn">주의</Badge> {w}
              </p>
            ))}
            <code data-testid="api-fresh-token">{fresh}</code>{" "}
            <button type="button" className="btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(fresh)}>
              복사
            </button>{" "}
            <button
              type="button"
              className="btn-sm"
              onClick={() => {
                setFresh(null);
                setFreshWarnings([]);
              }}
            >
              저장했습니다
            </button>
          </div>
        )}
        {tokens.error && <Notice tone="error">{tokens.error}</Notice>}
        {tokens.data && list.length === 0 && <EmptyState hint="위 양식에서 범위를 고르고 발급하세요.">발급한 API 토큰이 없습니다.</EmptyState>}
        {list.length > 0 && (
          <TableWrap>
            <table className="stack-table" aria-label="API 토큰">
              <thead>
                <tr>
                  <th>이름</th>
                  <th>토큰 앞부분</th>
                  <th>범위</th>
                  {multi && <th>법인</th>}
                  <th>만료</th>
                  <th>마지막 사용</th>
                  <th>최근 24시간</th>
                  <th>상태</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {list.map((t) => (
                  <tr key={t.id}>
                    <td data-label="이름">
                      {t.name}
                      <div className="hint">발급 {t.createdByName || `사용자 ${t.createdBy}`}</div>
                    </td>
                    <td data-label="토큰 앞부분">{t.prefix}…</td>
                    <td data-label="범위">{t.scopes.join(", ")}</td>
                    {multi && <td data-label="법인">{t.entityId === 0 ? "그룹 전체" : (entities.find((x) => x.id === t.entityId)?.name ?? t.entityCode ?? "-")}</td>}
                    <td data-label="만료" className="nowrap">
                      {fmt(t.expiresAt)}
                    </td>
                    <td data-label="마지막 사용" className="nowrap">
                      {fmt(t.lastUsedAt)}
                    </td>
                    <td data-label="최근 24시간">
                      호출 {t.calls24h.toLocaleString("ko-KR")}건 · 거부 {t.denied24h.toLocaleString("ko-KR")}건
                    </td>
                    <td data-label="상태">
                      <Badge tone={STATUS[t.status].tone}>{STATUS[t.status].text}</Badge>
                      {t.warnings.map((w) => (
                        <Badge key={w} tone="warn">
                          {WARNING_TEXT[w] ?? w}
                        </Badge>
                      ))}
                    </td>
                    <td>
                      {t.status === "active" && (
                        <button type="button" className="btn-danger btn-sm" disabled={busy} onClick={() => setRevoking(t)}>
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
      {writeConfirm && (
        <ConfirmDialog title="쓰기 범위 토큰 발급" confirmLabel="발급" busy={busy} onConfirm={() => void issue()} onCancel={() => setWriteConfirm(false)}>
          <p>이 토큰은 데이터를 읽는 것뿐 아니라 만들거나 바꿀 수 있습니다: {pickedWrite.map((sc) => sc.title).join(", ")}.</p>
          {pickedWrite.map((sc) => (
            <p key={sc.name} className="hint">
              {sc.note}
            </p>
          ))}
          {pickedWrite.filter((sc) => sc.warning).map((sc) => (
            <Notice key={sc.name} tone="warn">
              {sc.warning}
            </Notice>
          ))}
          <p>토큰이 유출되면 그 범위의 변경을 누구나 할 수 있으니 허용 출발지를 좁히고 유효 기간을 짧게 두는 것을 권장합니다.</p>
        </ConfirmDialog>
      )}
      {revoking && (
        <ConfirmDialog
          title="API 토큰 폐기"
          confirmLabel="폐기"
          busy={busy}
          onConfirm={() => {
            const t = revoking;
            setRevoking(null);
            void run(() => api.del(`/api/admin/integrations/tokens/${t.id}`), "토큰을 폐기했습니다.");
          }}
          onCancel={() => setRevoking(null)}
        >
          <p>
            「{revoking.name}」({revoking.prefix}…) 토큰을 폐기합니다. 이 토큰을 쓰는 연동은 바로 호출하지 못하게 되고 되살릴 수 없습니다. 교체하려면 새 토큰을 먼저 발급해 연동에 적용한 뒤 폐기하세요.
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}
