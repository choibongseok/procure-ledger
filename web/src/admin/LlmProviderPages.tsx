// LLM 공급자·라우팅 화면(B45 명세 10절). 공급자 설정은 llm:provider_manage(위험 권한·세션 전용), 라우팅은 llm:policy_manage.
// 저장된 키·주소는 화면에 나오지 않는다("저장됨"과 호스트만).
import { useState, type FormEvent } from "react";
import { api, ApiError } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, LoadingRows, Notice, PageHead, TableWrap } from "../layout/ui";
import { serverJson, serverMessage as errorText } from "../api/serverError";
import { useLoad } from "../purchase/useLoad";

export interface ProviderRow {
  id: number;
  code: string;
  kind: string;
  name: string;
  defaultModel: string;
  hint: string;
  mode: string;
  external: boolean;
  active: boolean;
  hasSecret: boolean;
  health?: string;
  calls24h?: number;
  fallbacks24h?: number;
  avgLatencyMs?: number;
  priceInMicroPerMtok: number;
  priceOutMicroPerMtok: number;
  timeoutMs: number;
  version: number;
}

interface ProviderList {
  providers: ProviderRow[];
  kinds: string[];
  sealerOk: boolean;
  rehearsal: boolean;
  mockModes: string[];
}

const KIND_LABEL: Record<string, string> = {
  mock: "모의(시험용)",
  openai_compat: "OpenAI 호환(사내 모델·vLLM 등)",
  anthropic: "Anthropic",
  bedrock: "AWS Bedrock",
  vertex: "Google Vertex AI",
};

const HEALTH: Record<string, [string, "ok" | "warn" | "bad" | "muted"]> = {
  ok: ["정상", "ok"],
  circuit_open: ["회로 차단 중", "bad"],
  check_settings: ["설정 확인 필요", "warn"],
  off: ["꺼짐", "muted"],
};

const TEST_LABEL: Record<string, string> = {
  ok: "연결됐습니다.",
  timeout: "시간 초과로 연결하지 못했습니다.",
  rate_limited: "공급자가 호출 한도를 알렸습니다.",
  http_5xx: "공급자 서버 오류입니다.",
  network: "네트워크로 연결하지 못했습니다.",
  auth: "인증에 실패했습니다. 키를 확인하세요.",
  bad_request: "요청이 거부되었습니다. 모델 ID 를 확인하세요.",
  setup: "설정을 읽을 수 없습니다. 키·주소를 다시 저장하세요.",
  unknown: "알 수 없는 오류입니다.",
};

interface FormState {
  id: number | null;
  version: number;
  code: string;
  kind: string;
  name: string;
  defaultModel: string;
  baseUrl: string;
  apiKey: string;
  region: string;
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken: string;
  project: string;
  location: string;
  serviceAccountJson: string;
  mode: string;
  timeoutMs: number;
  priceIn: number;
  priceOut: number;
}

const emptyForm: FormState = { id: null, version: 0, code: "", kind: "openai_compat", name: "", defaultModel: "", baseUrl: "", apiKey: "", region: "", accessKeyId: "", secretAccessKey: "", sessionToken: "", project: "", location: "", serviceAccountJson: "", mode: "", timeoutMs: 30000, priceIn: 0, priceOut: 0 };

export function LlmProvidersPage() {
  const { can } = useAuth();
  const manage = can("llm:provider_manage");
  const data = useLoad(() => (manage ? api.get<ProviderList>("/api/admin/llm/providers") : Promise.resolve(null)), [manage]);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const list = data.data?.providers ?? [];

  async function run(fn: () => Promise<string | void>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      const msg = await fn();
      setNote({ tone: "ok", text: msg || ok });
      await data.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  function edit(p: ProviderRow) {
    setForm({ id: p.id, version: p.version, code: p.code, kind: p.kind, name: p.name, defaultModel: p.defaultModel, baseUrl: "", apiKey: "", region: "", accessKeyId: "", secretAccessKey: "", sessionToken: "", project: "", location: "", serviceAccountJson: "", mode: p.mode, timeoutMs: p.timeoutMs, priceIn: p.priceInMicroPerMtok, priceOut: p.priceOutMicroPerMtok });
    setNote(null);
  }

  async function save(ev: FormEvent) {
    ev.preventDefault();
    const body = {
      code: form.code,
      kind: form.kind,
      name: form.name,
      defaultModel: form.defaultModel,
      baseUrl: form.baseUrl,
      apiKey: form.apiKey,
      region: form.region,
      accessKeyId: form.accessKeyId,
      secretAccessKey: form.secretAccessKey,
      sessionToken: form.sessionToken,
      project: form.project,
      location: form.location,
      serviceAccountJson: form.serviceAccountJson,
      mode: form.mode,
      timeoutMs: form.timeoutMs,
      priceInMicroPerMtok: form.priceIn,
      priceOutMicroPerMtok: form.priceOut,
    };
    await run(async () => {
      if (form.id === null) await api.post("/api/admin/llm/providers", body);
      else await api.put(`/api/admin/llm/providers/${form.id}`, { ...body, version: form.version });
      setForm(emptyForm);
    }, form.id === null ? "공급자를 등록했습니다." : "공급자 설정을 저장했습니다.");
  }

  if (!manage) return <Notice tone="error">LLM 공급자 설정을 볼 권한이 없습니다.</Notice>;

  return (
    <section>
      <PageHead title="LLM 공급자" />
      <p>AI 호출이 나갈 공급자를 등록합니다. 키·주소는 암호화해 저장하고 다시 보여 주지 않습니다. 개인정보 마스킹은 어느 공급자로 보내든 똑같이 먼저 적용됩니다. 어느 용도가 어느 공급자로 가는지는 &quot;LLM 라우팅&quot;에서 정합니다.</p>
      {data.data && !data.data.sealerOk && <Notice tone="warn">서버에 비밀 저장 키(PL_ACCOUNT_SECRET_KEY)가 없어 시험용 공급자만 등록할 수 있습니다.</Notice>}
      {data.data?.rehearsal && <Notice tone="warn">복구 리허설 중이라 외부 공급자는 쓸 수 없습니다.</Notice>}
      {data.error && <Notice tone="error">{data.error}</Notice>}
      {note && <Notice tone={note.tone}>{note.text}</Notice>}

      <section className="card" aria-labelledby="llm-prov-list">
        <h2 id="llm-prov-list">공급자 목록</h2>
        <TableWrap busy={data.loading}>
          <table className="stack-table" aria-label="공급자 목록">
            <thead>
              <tr>
                <th>이름</th>
                <th>종류</th>
                <th>호스트</th>
                <th>기본 모델</th>
                <th>상태</th>
                <th>최근 24시간</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.loading && !data.data ? <LoadingRows cols={7} /> : null}
              {list.length === 0 && !data.loading && (
                <tr>
                  <td colSpan={7}>등록된 공급자가 없습니다. 지금은 서버 설정의 기본 공급자만 쓰고 있습니다.</td>
                </tr>
              )}
              {list.map((p) => (
                <tr key={p.id}>
                  <td data-label="이름">
                    {p.name} <span className="hint">({p.code})</span>
                  </td>
                  <td data-label="종류">{KIND_LABEL[p.kind] ?? p.kind}</td>
                  <td data-label="호스트">{p.hint || "-"}</td>
                  <td data-label="기본 모델">{p.defaultModel || "-"}</td>
                  <td data-label="상태">
                    <Badge tone={(HEALTH[p.health ?? ""] ?? [p.active ? "정상" : "꺼짐", p.active ? "ok" : "muted"])[1]}>{(HEALTH[p.health ?? ""] ?? [p.active ? "사용 중" : "꺼짐"])[0]}</Badge> {p.hasSecret && <Badge tone="info">키 저장됨</Badge>}
                  </td>
                  <td data-label="최근 24시간">
                    {p.calls24h ?? 0}건
                    {(p.fallbacks24h ?? 0) > 0 && <span className="hint"> · 대체로 넘어간 {p.fallbacks24h}건</span>}
                    {(p.avgLatencyMs ?? 0) > 0 && <span className="hint"> · 평균 {p.avgLatencyMs}ms</span>}
                  </td>
                  <td>
                    <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => edit(p)} aria-label={`${p.name} 수정`}>
                      수정
                    </button>{" "}
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={busy}
                      aria-label={`${p.name} ${p.active ? "끄기" : "켜기"}`}
                      onClick={() => void run(async () => void (await api.post(`/api/admin/llm/providers/${p.id}/active`, { active: !p.active })), p.active ? `${p.name} 을(를) 껐습니다.` : `${p.name} 을(를) 켰습니다.`)}
                    >
                      {p.active ? "끄기" : "켜기"}
                    </button>{" "}
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={busy}
                      aria-label={`${p.name} 시험 연결`}
                      onClick={() =>
                        void run(async () => {
                          const r = await api.post<{ ok: boolean; code: string }>(`/api/admin/llm/providers/${p.id}/test`, {});
                          return `${p.name}: ${TEST_LABEL[r.code] ?? TEST_LABEL.unknown}`;
                        }, "")
                      }
                    >
                      시험 연결
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
        <p className="hint">시험 연결은 공급자에 짧은 요청 한 번을 보냅니다(외부 공급자는 비용이 조금 들 수 있습니다).</p>
      </section>

      <section className="card" aria-labelledby="llm-prov-form">
        <h2 id="llm-prov-form">{form.id === null ? "공급자 등록" : "공급자 수정"}</h2>
        <form onSubmit={save} aria-label="공급자 양식">
          <div className="form-row">
            <div className="field">
              <label htmlFor="llm-kind">종류</label>
              <select id="llm-kind" value={form.kind} disabled={form.id !== null} onChange={(e) => setForm({ ...form, kind: e.target.value })}>
                {(data.data?.kinds ?? ["mock", "openai_compat", "anthropic"]).map((k) => (
                  <option key={k} value={k}>
                    {KIND_LABEL[k] ?? k}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="llm-code">공급자 코드</label>
              <input id="llm-code" value={form.code} maxLength={30} disabled={form.id !== null} required onChange={(e) => setForm({ ...form, code: e.target.value.toLowerCase() })} />
              <span className="hint">영문 소문자·숫자·-. 한 번 정하면 바꿀 수 없습니다.</span>
            </div>
            <div className="field">
              <label htmlFor="llm-name">이름</label>
              <input id="llm-name" value={form.name} maxLength={40} required onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="llm-model">기본 모델 ID</label>
              <input id="llm-model" value={form.defaultModel} maxLength={120} onChange={(e) => setForm({ ...form, defaultModel: e.target.value })} />
            </div>
          </div>
          {form.kind === "openai_compat" && (
            <div className="form-row">
              <div className="field">
                <label htmlFor="llm-url">주소(https)</label>
                <input id="llm-url" value={form.baseUrl} placeholder={form.id === null ? "https://llm.example.com/v1" : "바꿀 때만 입력"} onChange={(e) => setForm({ ...form, baseUrl: e.target.value })} />
                <span className="hint">공개 https 주소만 쓸 수 있습니다. 사내 사설 주소는 아직 지원하지 않습니다.</span>
              </div>
            </div>
          )}
          {form.kind === "bedrock" && (
            <>
              <div className="form-row">
                <div className="field">
                  <label htmlFor="llm-region">리전</label>
                  <input id="llm-region" value={form.region} placeholder={form.id === null ? "ap-northeast-2" : "바꿀 때만 입력"} onChange={(e) => setForm({ ...form, region: e.target.value })} />
                  <span className="hint">호출 주소는 리전으로 정해지며 다른 주소는 쓸 수 없습니다.</span>
                </div>
                <div className="field">
                  <label htmlFor="llm-akid">액세스 키 ID</label>
                  <input id="llm-akid" type="password" autoComplete="off" value={form.accessKeyId} placeholder={form.id === null ? "" : "바꿀 때만 입력"} onChange={(e) => setForm({ ...form, accessKeyId: e.target.value })} />
                </div>
                <div className="field">
                  <label htmlFor="llm-sak">비밀 액세스 키</label>
                  <input id="llm-sak" type="password" autoComplete="off" value={form.secretAccessKey} placeholder={form.id === null ? "" : "바꿀 때만 입력"} onChange={(e) => setForm({ ...form, secretAccessKey: e.target.value })} />
                </div>
                <div className="field">
                  <label htmlFor="llm-stok">세션 토큰(선택)</label>
                  <input id="llm-stok" type="password" autoComplete="off" value={form.sessionToken} onChange={(e) => setForm({ ...form, sessionToken: e.target.value })} />
                </div>
              </div>
              <p className="hint">모델 호출 권한만 가진 전용 자격 증명을 쓰세요. 요청 형식은 공개 문서 기준이라 운영 전에 실제 계정으로 확인해야 합니다.</p>
            </>
          )}
          {form.kind === "vertex" && (
            <>
              <div className="form-row">
                <div className="field">
                  <label htmlFor="llm-project">프로젝트 ID</label>
                  <input id="llm-project" value={form.project} placeholder={form.id === null ? "" : "바꿀 때만 입력"} onChange={(e) => setForm({ ...form, project: e.target.value })} />
                </div>
                <div className="field">
                  <label htmlFor="llm-location">로케이션</label>
                  <input id="llm-location" value={form.location} placeholder={form.id === null ? "us-central1" : "바꿀 때만 입력"} onChange={(e) => setForm({ ...form, location: e.target.value })} />
                </div>
              </div>
              <div className="form-row">
                <div className="field">
                  <label htmlFor="llm-sa">서비스 계정 JSON</label>
                  <textarea id="llm-sa" rows={4} autoComplete="off" value={form.serviceAccountJson} placeholder={form.id === null ? "" : "바꿀 때만 붙여넣기"} onChange={(e) => setForm({ ...form, serviceAccountJson: e.target.value })} />
                  <span className="hint">모델 호출 권한만 가진 서비스 계정을 쓰세요. 저장한 뒤에는 다시 볼 수 없습니다. 요청 형식은 공개 문서 기준이라 운영 전에 실제 계정으로 확인해야 합니다.</span>
                </div>
              </div>
            </>
          )}
          {(form.kind === "openai_compat" || form.kind === "anthropic") && (
            <div className="form-row">
              <div className="field">
                <label htmlFor="llm-key">API 키</label>
                <input id="llm-key" type="password" autoComplete="off" value={form.apiKey} placeholder={form.id === null ? "" : "바꿀 때만 입력"} onChange={(e) => setForm({ ...form, apiKey: e.target.value })} />
              </div>
            </div>
          )}
          {form.kind === "mock" && (
            <div className="form-row">
              <div className="field">
                <label htmlFor="llm-mode">동작 모드</label>
                <input id="llm-mode" value={form.mode} placeholder="비우면 정상 응답" list="llm-modes" onChange={(e) => setForm({ ...form, mode: e.target.value })} />
                <datalist id="llm-modes">
                  {(data.data?.mockModes ?? []).map((m) => (
                    <option key={m} value={m} />
                  ))}
                </datalist>
                <span className="hint">시험용입니다. 실패·지연을 흉내 낼 수 있습니다.</span>
              </div>
            </div>
          )}
          <div className="form-row">
            <div className="field">
              <label htmlFor="llm-timeout">시간 제한(ms)</label>
              <input id="llm-timeout" type="number" min={1000} max={120000} value={form.timeoutMs} onChange={(e) => setForm({ ...form, timeoutMs: Number(e.target.value) })} />
            </div>
            <div className="field">
              <label htmlFor="llm-price-in">입력 가격(1백만 토큰당, 1e-6 USD)</label>
              <input id="llm-price-in" type="number" min={0} value={form.priceIn} onChange={(e) => setForm({ ...form, priceIn: Number(e.target.value) })} />
            </div>
            <div className="field">
              <label htmlFor="llm-price-out">출력 가격(1백만 토큰당, 1e-6 USD)</label>
              <input id="llm-price-out" type="number" min={0} value={form.priceOut} onChange={(e) => setForm({ ...form, priceOut: Number(e.target.value) })} />
            </div>
          </div>
          <div className="form-row">
            <button type="submit" disabled={busy}>
              {form.id === null ? "등록" : "저장"}
            </button>
            {form.id !== null && (
              <button type="button" className="btn-secondary" onClick={() => setForm(emptyForm)}>
                취소
              </button>
            )}
          </div>
        </form>
      </section>
    </section>
  );
}

// ---- 라우팅 ----

interface RouteRow {
  id: number;
  sortOrder: number;
  purposePattern: string;
  departmentId: number;
  role: string;
  primaryProviderId: number;
  primaryModel: string;
  fallbackProviderId: number;
  fallbackModel: string;
  primaryCode: string;
  fallbackCode: string;
  active: boolean;
}

interface RouteList {
  routes: RouteRow[];
  providers: ProviderRow[];
  departments?: { id: number; name: string }[];
}

interface SimResult {
  matched: boolean;
  primaryCode: string;
  primaryModel: string;
  fallbackCode: string;
  defaultPath: boolean;
}

const ROLE_CHOICES: [string, string][] = [
  ["", "모든 역할"],
  ["admin", "관리자"],
  ["finance", "재무"],
  ["manager", "팀장"],
  ["staff", "직원"],
  ["auditor", "감사"],
];

/** 라우팅 저장 전 평가(B46) 결과. status: pass·blocked·skipped·confirm_cost. */
interface GateInfo {
  status: string;
  message: string;
  external: boolean;
}

interface GateExtra {
  gateOverride?: boolean;
  gateReason?: string;
  gateConfirmCost?: boolean;
}

interface RouteForm {
  id: number | null;
  purposePattern: string;
  departmentId: number;
  role: string;
  primaryProviderId: number;
  primaryModel: string;
  fallbackProviderId: number;
  fallbackModel: string;
  sortOrder: number;
  active: boolean;
}

const emptyRoute: RouteForm = { id: null, purposePattern: "*", departmentId: 0, role: "", primaryProviderId: 0, primaryModel: "", fallbackProviderId: 0, fallbackModel: "", sortOrder: 0, active: true };

export function LlmRoutesPage() {
  const { can } = useAuth();
  const manage = can("llm:policy_manage");
  const data = useLoad(() => (manage ? api.get<RouteList>("/api/admin/llm/routes") : Promise.resolve(null)), [manage]);
  const [form, setForm] = useState<RouteForm>(emptyRoute);
  const [note, setNote] = useState<{ tone: "ok" | "error" | "warn"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [sim, setSim] = useState({ purpose: "", departmentId: 0, role: "" });
  const [simOut, setSimOut] = useState<SimResult | null>(null);
  const [pending, setPending] = useState<{ gate: GateInfo; extra: GateExtra } | null>(null);
  const [reason, setReason] = useState("");
  const routes = data.data?.routes ?? [];
  const providers = data.data?.providers ?? [];
  const depts = data.data?.departments ?? [];
  const deptName = (id: number) => (id ? (depts.find((d) => d.id === id)?.name ?? `부서 ${id}`) : "모든 부서");
  const deptOptions = (
    <>
      <option value={0}>모든 부서</option>
      {depts.map((d) => (
        <option key={d.id} value={d.id}>
          {d.name}
        </option>
      ))}
    </>
  );

  async function run(fn: () => Promise<void>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      setNote({ tone: "ok", text: ok });
      await data.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  async function submit(extra: GateExtra) {
    const body = {
      purposePattern: form.purposePattern,
      departmentId: form.departmentId,
      role: form.role,
      primaryProviderId: form.primaryProviderId,
      primaryModel: form.primaryModel,
      fallbackProviderId: form.fallbackProviderId,
      fallbackModel: form.fallbackModel,
      sortOrder: form.sortOrder,
      active: form.active,
      ...extra,
    };
    setBusy(true);
    setNote(null);
    try {
      const out = form.id === null ? await api.post<{ gate?: GateInfo }>("/api/admin/llm/routes", body) : await api.put<{ gate?: GateInfo }>(`/api/admin/llm/routes/${form.id}`, body);
      const base = form.id === null ? "규칙을 등록했습니다." : "규칙을 저장했습니다.";
      setNote({ tone: out.gate?.status === "blocked" ? "warn" : "ok", text: out.gate?.message ? `${base} ${out.gate.message}` : base });
      setForm(emptyRoute);
      setPending(null);
      await data.reload();
    } catch (err) {
      // 저장 전 평가가 확인을 요구하면(409 + 게이트 결과) 저장하지 않고 확인 칸을 연다.
      const j = err instanceof ApiError && err.status === 409 ? serverJson<{ gate?: GateInfo }>(err) : null;
      if (j?.body.gate && (j.body.gate.status === "blocked" || j.body.gate.status === "confirm_cost")) {
        setPending({ gate: j.body.gate, extra });
        setReason("");
      } else {
        setNote({ tone: "error", text: errorText(err) });
      }
    } finally {
      setBusy(false);
    }
  }

  async function save(ev: FormEvent) {
    ev.preventDefault();
    setPending(null);
    await submit({});
  }

  async function simulate(ev: FormEvent) {
    ev.preventDefault();
    setNote(null);
    try {
      setSimOut(await api.post<SimResult>("/api/admin/llm/routes/simulate", sim));
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    }
  }

  if (!manage) return <Notice tone="error">LLM 라우팅을 볼 권한이 없습니다.</Notice>;
  const roleLabel = (r: string) => ROLE_CHOICES.find(([k]) => k === r)?.[1] ?? r;

  return (
    <section>
      <PageHead title="LLM 라우팅" />
      <p>용도·부서·역할에 따라 AI 호출을 어느 공급자와 모델로 보낼지 정합니다. 맞는 규칙이 없으면 서버 설정의 기본 공급자로 갑니다. 규칙이 여러 개 맞으면 부서를 지정한 규칙, 용도가 더 구체적인 규칙, 역할을 지정한 규칙, 순서가 작은 규칙 순으로 고릅니다. 주 공급자가 시간 초과·서버 오류·한도 초과면 재시도한 뒤 대체 공급자로 보내고, 계속 실패하면 잠시 주 공급자를 건너뜁니다(회로 차단). 인증 실패·요청 거부는 재시도 없이 바로 대체로 갑니다. 대체가 일어난 호출은 사용량과 감사 로그에 표시됩니다.</p>
      {data.error && <Notice tone="error">{data.error}</Notice>}
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {pending && (
        <section className="card" role="group" aria-label="저장 전 평가 확인">
          <h2>저장 전 평가</h2>
          <Notice tone="warn">{pending.gate.message}</Notice>
          {pending.gate.status === "confirm_cost" ? (
            <div className="form-row">
              <button type="button" disabled={busy} onClick={() => void submit({ ...pending.extra, gateConfirmCost: true })}>
                비용을 확인하고 평가 실행
              </button>
              <button type="button" className="btn-secondary" onClick={() => setPending(null)}>
                취소
              </button>
            </div>
          ) : (
            <>
              <div className="field">
                <label htmlFor="gate-reason">무시 사유(선택)</label>
                <input id="gate-reason" value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />
                <span className="hint">사유 입력 여부만 감사 로그에 남고 내용은 남지 않습니다.</span>
              </div>
              <div className="form-row">
                <button type="button" disabled={busy} onClick={() => void submit({ ...pending.extra, gateOverride: true, gateReason: reason, gateConfirmCost: pending.gate.external })}>
                  그래도 저장
                </button>
                <button type="button" className="btn-secondary" onClick={() => setPending(null)}>
                  취소
                </button>
              </div>
            </>
          )}
        </section>
      )}

      <section className="card" aria-labelledby="llm-route-list">
        <h2 id="llm-route-list">라우팅 규칙</h2>
        <TableWrap busy={data.loading}>
          <table className="stack-table" aria-label="라우팅 규칙">
            <thead>
              <tr>
                <th>용도</th>
                <th>부서</th>
                <th>역할</th>
                <th>주 공급자</th>
                <th>대체</th>
                <th>상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.loading && !data.data ? <LoadingRows cols={7} /> : null}
              {routes.length === 0 && !data.loading && (
                <tr>
                  <td colSpan={7}>규칙이 없습니다. 모든 호출이 기본 공급자로 갑니다.</td>
                </tr>
              )}
              {routes.map((r) => (
                <tr key={r.id}>
                  <td data-label="용도">{r.purposePattern === "*" ? "모든 용도" : r.purposePattern}</td>
                  <td data-label="부서">{deptName(r.departmentId)}</td>
                  <td data-label="역할">{roleLabel(r.role)}</td>
                  <td data-label="주 공급자">
                    {r.primaryCode}
                    {r.primaryModel && <span className="hint"> · {r.primaryModel}</span>}
                  </td>
                  <td data-label="대체">{r.fallbackCode || "-"}</td>
                  <td data-label="상태">
                    <Badge tone={r.active ? "ok" : "muted"}>{r.active ? "사용 중" : "꺼짐"}</Badge>
                  </td>
                  <td>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={busy}
                      aria-label={`${r.purposePattern} 규칙 수정`}
                      onClick={() => setForm({ id: r.id, purposePattern: r.purposePattern, departmentId: r.departmentId, role: r.role, primaryProviderId: r.primaryProviderId, primaryModel: r.primaryModel, fallbackProviderId: r.fallbackProviderId, fallbackModel: r.fallbackModel, sortOrder: r.sortOrder, active: r.active })}
                    >
                      수정
                    </button>{" "}
                    <button type="button" className="btn-secondary btn-sm" disabled={busy} aria-label={`${r.purposePattern} 규칙 삭제`} onClick={() => void run(async () => void (await api.del(`/api/admin/llm/routes/${r.id}`)), "규칙을 삭제했습니다.")}>
                      삭제
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      </section>

      <section className="card" aria-labelledby="llm-route-form">
        <h2 id="llm-route-form">{form.id === null ? "규칙 등록" : "규칙 수정"}</h2>
        <form onSubmit={save} aria-label="규칙 양식">
          <div className="form-row">
            <div className="field">
              <label htmlFor="rt-purpose">용도 패턴</label>
              <input id="rt-purpose" value={form.purposePattern} required onChange={(e) => setForm({ ...form, purposePattern: e.target.value })} />
              <span className="hint">정확한 용도(예: docverify.extract), 접두(예: docverify.*), 또는 *(모든 용도)</span>
            </div>
            <div className="field">
              <label htmlFor="rt-dept">부서</label>
              <select id="rt-dept" value={form.departmentId} onChange={(e) => setForm({ ...form, departmentId: Number(e.target.value) })}>
                {deptOptions}
              </select>
            </div>
            <div className="field">
              <label htmlFor="rt-role">역할</label>
              <select id="rt-role" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
                {ROLE_CHOICES.map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <div className="form-row">
            <div className="field">
              <label htmlFor="rt-primary">주 공급자</label>
              <select id="rt-primary" value={form.primaryProviderId} required onChange={(e) => setForm({ ...form, primaryProviderId: Number(e.target.value) })}>
                <option value={0}>고르세요</option>
                {providers.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="rt-primary-model">주 모델 ID</label>
              <input id="rt-primary-model" value={form.primaryModel} placeholder="비우면 공급자 기본 모델" onChange={(e) => setForm({ ...form, primaryModel: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="rt-fb">대체 공급자</label>
              <select id="rt-fb" value={form.fallbackProviderId} onChange={(e) => setForm({ ...form, fallbackProviderId: Number(e.target.value) })}>
                <option value={0}>없음</option>
                {providers
                  .filter((p) => p.id !== form.primaryProviderId)
                  .map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="rt-fb-model">대체 모델 ID</label>
              <input id="rt-fb-model" value={form.fallbackModel} onChange={(e) => setForm({ ...form, fallbackModel: e.target.value })} />
            </div>
          </div>
          <div className="form-row">
            <div className="field">
              <label htmlFor="rt-order">순서</label>
              <input id="rt-order" type="number" value={form.sortOrder} onChange={(e) => setForm({ ...form, sortOrder: Number(e.target.value) })} />
            </div>
            <div className="field">
              <label htmlFor="rt-active">
                <input id="rt-active" type="checkbox" checked={form.active} onChange={(e) => setForm({ ...form, active: e.target.checked })} /> 사용
              </label>
            </div>
            <button type="submit" disabled={busy}>
              {form.id === null ? "등록" : "저장"}
            </button>
            {form.id !== null && (
              <button type="button" className="btn-secondary" onClick={() => setForm(emptyRoute)}>
                취소
              </button>
            )}
          </div>
          {providers.some((p) => p.external && p.id === form.fallbackProviderId) && <Notice tone="warn">대체 공급자는 데이터가 회사 밖으로 나가는 공급자입니다.</Notice>}
        </form>
      </section>

      <section className="card" aria-labelledby="llm-route-sim">
        <h2 id="llm-route-sim">이 호출은 어디로 가나</h2>
        <form onSubmit={simulate} aria-label="경로 확인 양식">
          <div className="form-row">
            <div className="field">
              <label htmlFor="sim-purpose">용도</label>
              <input id="sim-purpose" value={sim.purpose} placeholder="예: docverify.extract" onChange={(e) => setSim({ ...sim, purpose: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="sim-dept">부서</label>
              <select id="sim-dept" value={sim.departmentId} onChange={(e) => setSim({ ...sim, departmentId: Number(e.target.value) })}>
                {deptOptions}
              </select>
            </div>
            <div className="field">
              <label htmlFor="sim-role">역할</label>
              <select id="sim-role" value={sim.role} onChange={(e) => setSim({ ...sim, role: e.target.value })}>
                {ROLE_CHOICES.map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
            </div>
            <button type="submit">확인</button>
          </div>
        </form>
        {simOut && (
          <p role="status">
            {simOut.defaultPath
              ? "맞는 규칙이 없어 서버 설정의 기본 공급자로 갑니다."
              : `${simOut.primaryCode}${simOut.primaryModel ? ` (${simOut.primaryModel})` : ""} 로 갑니다.${simOut.fallbackCode ? ` 장애 시 대체: ${simOut.fallbackCode}` : ""}`}
          </p>
        )}
      </section>
    </section>
  );
}
