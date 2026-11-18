// 전자세금계산서 대행사 설정 화면(B43 명세 7·9절 5항): 대행사 이름·엔드포인트·API 키·인증서 입력, 시험 연결, 사용 켜기/끄기. taxasp:manage(관리자, 위험 권한·세션 전용).
// 키·인증서·비밀번호는 한 번 입력하면 봉인되어 다시 볼 수 없다(교체만). 화면에는 호스트와 인증서 만료일·주체 끝만 보인다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, Notice } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface AspView {
  configured: boolean;
  provider: string;
  providers: string[];
  active: boolean;
  host: string;
  hasKey: boolean;
  hasCert: boolean;
  certNotAfter: string;
  certSubject: string;
  certState: "none" | "ok" | "expiring" | "expired";
  daysLeft?: number;
  mockWarning: boolean;
  rehearsal: boolean;
  updatedAt: string;
}

const providerName = (p: string) => (p === "mock" ? "모의 대행사(시험용)" : p);

interface FormState {
  provider: string;
  endpoint: string;
  apiKey: string;
  certPem: string;
  keyPem: string;
  password: string;
}

const emptyForm: FormState = { provider: "", endpoint: "", apiKey: "", certPem: "", keyPem: "", password: "" };

export function TaxAspPage() {
  const { can } = useAuth();
  const manage = can("taxasp:manage");
  const view = useLoad(() => (manage ? api.get<AspView>("/api/admin/tax-asp") : Promise.resolve(null)), [manage]);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [note, setNote] = useState<{ tone: "ok" | "error" | "warn"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const v = view.data;

  async function run(fn: () => Promise<string | { tone: "ok" | "warn"; text: string }>) {
    setBusy(true);
    setNote(null);
    try {
      const r = await fn();
      setNote(typeof r === "string" ? { tone: "ok", text: r } : r);
      await view.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await api.put("/api/admin/tax-asp", { provider: form.provider || v?.provider || "", endpoint: form.endpoint, apiKey: form.apiKey, certPem: form.certPem, keyPem: form.keyPem, password: form.password });
      setForm(emptyForm); // 입력한 비밀은 화면에 남기지 않는다
      return "대행사 설정을 저장했습니다. 키와 인증서는 봉인되어 다시 볼 수 없습니다.";
    });
  }

  if (!manage) return <Notice tone="error">전자세금계산서 대행사 설정을 볼 권한이 없습니다.</Notice>;
  const providers = v?.providers ?? [];
  const chosen = form.provider || v?.provider || providers[0] || "";

  return (
    <section>
      <h1>전자세금계산서 대행사</h1>
      <p>세금계산서를 국세청에 보내고 받는 연계 대행사의 접속 정보를 관리합니다. 키와 인증서는 저장하면 봉인되어 다시 볼 수 없고, 바꾸려면 새 값을 입력합니다.</p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {view.error && <Notice tone="error">{view.error}</Notice>}
      {v?.rehearsal && <Notice tone="warn">복구 리허설 중에는 대행사 설정을 바꾸거나 시험 연결을 할 수 없습니다(복구본이 실제 대행사에 발행하는 사고를 막습니다).</Notice>}
      {v?.mockWarning && <Notice tone="warn">모의 대행사가 선택되어 있습니다. 모의 대행사는 시험용이라 실제 국세청에 접수되지 않습니다. 운영에서는 실제 대행사를 설정하세요.</Notice>}
      {v?.certState === "expired" && <Notice tone="error">대행사 인증서가 만료되어 세금계산서를 발행할 수 없습니다. 인증서를 교체하세요.</Notice>}
      {v?.certState === "expiring" && <Notice tone="warn">대행사 인증서가 {v.daysLeft}일 뒤 만료됩니다. 만료되면 세금계산서를 발행할 수 없으니 미리 교체하세요.</Notice>}

      <section className="card" aria-labelledby="asp-state">
        <h2 id="asp-state">현재 설정</h2>
        {!v?.configured ? (
          <p>대행사가 설정되어 있지 않습니다. 설정하기 전에는 세금계산서를 작성만 할 수 있고 발행(전송)은 되지 않습니다.</p>
        ) : (
          <dl>
            <dt>대행사</dt>
            <dd>{providerName(v.provider)}</dd>
            <dt>사용 상태</dt>
            <dd>
              <Badge tone={v.active ? "ok" : "muted"}>{v.active ? "사용 중" : "꺼짐"}</Badge>
            </dd>
            <dt>접속 호스트</dt>
            <dd>{v.host || "-"}</dd>
            <dt>API 키</dt>
            <dd>{v.hasKey ? "등록됨(보이지 않음)" : "없음"}</dd>
            <dt>인증서</dt>
            <dd>
              {v.hasCert ? `${v.certSubject || "등록됨"} · 만료일 ${v.certNotAfter}` : "없음"}
              {v.hasCert && v.certState === "expiring" && <> · <Badge tone="warn">만료 임박</Badge></>}
              {v.hasCert && v.certState === "expired" && <> · <Badge tone="bad">만료</Badge></>}
            </dd>
          </dl>
        )}
        {v?.configured && (
          <p>
            <button
              type="button"
              className="btn-secondary"
              disabled={busy || v.rehearsal}
              onClick={() =>
                void run(async () => {
                  const r = await api.post<{ ok: boolean; message: string }>("/api/admin/tax-asp/test");
                  return { tone: r.ok ? "ok" : "warn", text: r.message || (r.ok ? "시험 연결에 성공했습니다." : "시험 연결에 실패했습니다.") };
                })
              }
            >
              시험 연결
            </button>{" "}
            {v.active ? (
              <button type="button" className="btn-secondary" disabled={busy || v.rehearsal} onClick={() => void run(async () => (await api.post("/api/admin/tax-asp/deactivate"), "대행사 사용을 껐습니다. 발행(전송)이 멈춥니다."))}>
                사용 끄기
              </button>
            ) : (
              <button type="button" disabled={busy || v.rehearsal} onClick={() => void run(async () => (await api.post("/api/admin/tax-asp/activate"), "대행사 사용을 켰습니다."))}>
                사용 켜기
              </button>
            )}
          </p>
        )}
      </section>

      <section className="card" aria-labelledby="asp-form">
        <h2 id="asp-form">{v?.configured ? "설정 바꾸기" : "대행사 설정"}</h2>
        <form onSubmit={save} aria-label="대행사 설정 양식" autoComplete="off">
          <div className="field">
            <label htmlFor="asp-provider">대행사</label>
            <select id="asp-provider" value={chosen} onChange={(e) => setForm({ ...form, provider: e.target.value })}>
              {providers.map((p) => (
                <option key={p} value={p}>
                  {providerName(p)}
                </option>
              ))}
            </select>
          </div>
          {chosen !== "mock" && (
            <>
              <div className="field">
                <label htmlFor="asp-endpoint">접속 주소(https)</label>
                <input id="asp-endpoint" value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} placeholder="https://" autoComplete="off" />
                <span className="hint">호스트 하나로 고정됩니다. 저장하면 봉인되어 호스트만 표시됩니다.</span>
              </div>
              <div className="field">
                <label htmlFor="asp-key">API 키</label>
                <input id="asp-key" type="password" value={form.apiKey} onChange={(e) => setForm({ ...form, apiKey: e.target.value })} autoComplete="new-password" />
                <span className="hint">비워 두면 저장된 키를 그대로 씁니다.</span>
              </div>
              <div className="field">
                <label htmlFor="asp-cert">인증서(PEM, 선택)</label>
                <textarea id="asp-cert" rows={4} value={form.certPem} onChange={(e) => setForm({ ...form, certPem: e.target.value })} spellCheck={false} style={{ width: "100%" }} />
              </div>
              <div className="field">
                <label htmlFor="asp-keypem">인증서 개인키(PEM, 선택)</label>
                <textarea id="asp-keypem" rows={4} value={form.keyPem} onChange={(e) => setForm({ ...form, keyPem: e.target.value })} spellCheck={false} style={{ width: "100%" }} />
              </div>
              <div className="field">
                <label htmlFor="asp-pass">인증서 비밀번호(선택)</label>
                <input id="asp-pass" type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} autoComplete="new-password" />
              </div>
            </>
          )}
          {chosen === "mock" && <p className="hint">모의 대행사는 접속 정보가 필요 없습니다. 시험·시연용이며 실제 국세청에 접수되지 않습니다.</p>}
          <p>
            <button type="submit" disabled={busy || !chosen || v?.rehearsal}>
              저장
            </button>
          </p>
        </form>
      </section>
    </section>
  );
}
