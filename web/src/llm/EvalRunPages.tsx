//(B46 2단계 화면). 평가 세트 실행·결과·비교·기준선. 실행은 eval:run, 기준선 지정은 eval:write, 조회는 eval:read.
// 응답 전문은 저장하지 않으므로 화면에도 없다(실패 문항은 사유 한 줄만).
import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, Notice, PageHead, TableWrap } from "../layout/ui";
import { serverMessage as errorText } from "../api/serverError";
import { useLoad } from "../purchase/useLoad";

export interface RunItem {
  seq: number;
  question: string;
  expected: string;
  pass: boolean;
  score: number;
  critical: boolean;
  latencyMs: number;
  detail: string;
}

export interface RunRow {
  id: number;
  at: string;
  setCode: string;
  setName: string;
  setVersion: number;
  promptRef: string;
  provider: string;
  model: string;
  mode: string;
  status: string;
  failReason: string;
  total: number;
  doneItems: number;
  passed: number;
  passRatePct: number;
  thresholdPct: number;
  criticalFails: number;
  gatePassed: boolean;
  avgScore: number;
  latencyMs: number;
  avgLatencyMs: number;
  p95LatencyMs: number;
  inputTokens: number;
  outputTokens: number;
  costMicroUsd: number;
  baseline: boolean;
  baselinePct: number;
  warning: string;
  items: RunItem[];
}

interface CompareRow {
  seq: number;
  question: string;
  aPass: boolean;
  bPass: boolean;
  change: string;
}

interface Comparison {
  a: RunRow;
  b: RunRow;
  sameVersion: boolean;
  note: string;
  regressions: number;
  improvements: number;
  rows: CompareRow[];
}

interface ProviderChoice {
  code: string;
  name: string;
  defaultModel: string;
  external: boolean;
}

const STATUS_LABEL: Record<string, string> = { running: "진행 중", done: "완료", failed: "실패" };
const MODE_LABEL: Record<string, string> = { manual: "화면 실행", gate: "저장 전 평가", cli: "명령줄" };

const usd = (micro: number) => `$${(micro / 1_000_000).toFixed(4)}`;
const runLabel = (r: RunRow) => `#${r.id} v${r.setVersion} · ${r.provider}${r.model ? ` (${r.model})` : ""}`;

function statusBadge(r: RunRow) {
  const tone = r.status === "done" ? (r.gatePassed ? "ok" : "bad") : r.status === "running" ? "info" : "bad";
  const text = r.status === "done" ? (r.gatePassed ? "합격" : "기준 미달") : (STATUS_LABEL[r.status] ?? r.status);
  return <Badge tone={tone}>{text}</Badge>;
}

/** 진행 중인 실행이 있는 동안 주기적으로 다시 불러온다. */
function usePolling(active: boolean, reload: () => Promise<void>) {
  useEffect(() => {
    if (!active) return undefined;
    const t = setInterval(() => void reload(), 1500);
    return () => clearInterval(t);
  }, [active, reload]);
}

export function EvalRunPanel({ code, published }: { code: string; published: boolean }) {
  const { can } = useAuth();
  const canRun = can("eval:run");
  const runs = useLoad(() => api.get<{ runs: RunRow[] }>(`/api/admin/eval/runs?setCode=${encodeURIComponent(code)}`), [code]);
  const options = useLoad(() => (canRun ? api.get<{ providers: ProviderChoice[] }>("/api/admin/eval/run-options") : Promise.resolve({ providers: [] as ProviderChoice[] })), [canRun]);
  const [provider, setProvider] = useState("");
  const [model, setModel] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const rows = runs.data?.runs ?? [];
  usePolling(
    rows.some((r) => r.status === "running"),
    runs.reload,
  );
  const chosen = options.data?.providers.find((p) => p.code === provider);

  async function start() {
    setBusy(true);
    setNote(null);
    try {
      await api.post(`/api/admin/eval/sets/${code}/runs`, { provider, model, confirmCost: confirm });
      setNote({ tone: "ok", text: "평가를 시작했습니다. 문항이 하나씩 채점됩니다." });
      await runs.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-labelledby="eval-runs">
      <h2 id="eval-runs">실행과 결과</h2>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {runs.error && <Notice tone="error">{runs.error}</Notice>}
      {canRun && published && (
        <form
          aria-label="세트 실행 양식"
          onSubmit={(e) => {
            e.preventDefault();
            void start();
          }}
        >
          <div className="form-row">
            <div className="field">
              <label htmlFor="er-provider">공급자</label>
              <select id="er-provider" value={provider} onChange={(e) => (setProvider(e.target.value), setModel(""))}>
                <option value="">현재 경로(라우팅 규칙을 그대로)</option>
                {(options.data?.providers ?? []).map((p) => (
                  <option key={p.code} value={p.code}>
                    {p.name} ({p.code}){p.external ? " · 외부" : ""}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="er-model">모델(선택)</label>
              <input id="er-model" value={model} placeholder={chosen?.defaultModel || "공급자 기본 모델"} onChange={(e) => setModel(e.target.value)} />
            </div>
          </div>
          <Notice tone="warn">{chosen && !chosen.external ? "사내 공급자라 외부 비용은 없지만 일일 한도를 씁니다." : "외부 공급자는 평가 실행에 비용이 들고 일일 한도를 씁니다."}</Notice>
          <div className="form-row">
            <label htmlFor="er-confirm">
              <input id="er-confirm" type="checkbox" checked={confirm} onChange={(e) => setConfirm(e.target.checked)} /> 비용과 한도 사용을 확인했습니다
            </label>
            <button type="submit" disabled={busy || !confirm}>
              세트 실행
            </button>
          </div>
        </form>
      )}
      {!published && <p className="hint">게시한 버전이 있어야 실행할 수 있습니다.</p>}
      <TableWrap busy={runs.loading}>
        <table className="stack-table" aria-label="실행 이력">
          <thead>
            <tr>
              <th>번호</th>
              <th>버전</th>
              <th>공급자</th>
              <th>합격률</th>
              <th>결과</th>
              <th>방식</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && !runs.loading && (
              <tr>
                <td colSpan={6}>아직 실행 기록이 없습니다.</td>
              </tr>
            )}
            {rows.map((r) => (
              <tr key={r.id}>
                <td data-label="번호">
                  <Link to={`/admin/eval/runs/${r.id}`}>#{r.id}</Link> {r.baseline && <Badge tone="info">기준선</Badge>}
                </td>
                <td data-label="버전">v{r.setVersion}</td>
                <td data-label="공급자">
                  {r.provider}
                  {r.model ? ` (${r.model})` : ""}
                </td>
                <td data-label="합격률" className="num">
                  {r.status === "running" ? `${r.doneItems}/${r.total} 문항` : r.status === "done" ? `${r.passRatePct}% (${r.passed}/${r.total})` : "-"}
                </td>
                <td data-label="결과">{statusBadge(r)}</td>
                <td data-label="방식">{MODE_LABEL[r.mode] ?? r.mode}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
    </section>
  );
}

export function EvalRunPage() {
  const { id = "" } = useParams();
  const { can } = useAuth();
  const write = can("eval:write");
  const data = useLoad(() => api.get<RunRow>(`/api/admin/eval/runs/${id}`), [id]);
  const r = data.data;
  const siblings = useLoad(() => (r ? api.get<{ runs: RunRow[] }>(`/api/admin/eval/runs?setCode=${encodeURIComponent(r.setCode)}`) : Promise.resolve({ runs: [] as RunRow[] })), [r?.setCode]);
  const [other, setOther] = useState("");
  const [cmp, setCmp] = useState<Comparison | null>(null);
  const [note, setNote] = useState<{ tone: "ok" | "error" | "warn"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  usePolling(r?.status === "running", data.reload);

  async function baseline(runId: number) {
    setBusy(true);
    setNote(null);
    try {
      const out = await api.put<{ warning: string }>(`/api/admin/eval/sets/${r?.setCode}/baseline`, { runId });
      setNote(out.warning ? { tone: "warn", text: `기준선을 지정했습니다. ${out.warning}` } : { tone: "ok", text: runId ? "기준선으로 지정했습니다." : "기준선을 해제했습니다." });
      await data.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  async function compare() {
    if (!other) return;
    setNote(null);
    try {
      setCmp(await api.get<Comparison>(`/api/admin/eval/run-compare?a=${other}&b=${id}`));
    } catch (err) {
      setCmp(null);
      setNote({ tone: "error", text: errorText(err) });
    }
  }

  if (data.error && !r) return <Notice tone="error">{data.error}</Notice>;
  if (!r) return <p>불러오는 중…</p>;
  const candidates = (siblings.data?.runs ?? []).filter((x) => x.id !== r.id && x.status === "done");
  const failed = r.items.filter((i) => !i.pass);

  return (
    <section>
      <PageHead title={`평가 실행 #${r.id}`} />
      <p>
        <Link to={`/admin/eval/sets/${r.setCode}`}>← {r.setName}</Link> · v{r.setVersion} · {r.provider}
        {r.model ? ` (${r.model})` : ""} · {MODE_LABEL[r.mode] ?? r.mode}
      </p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {r.status === "failed" && <Notice tone="error">실행이 끝나지 못했습니다: {r.failReason}. 지금까지의 결과는 부분 실행으로 인정하지 않습니다.</Notice>}
      {r.warning && <Notice tone="warn">{r.warning}</Notice>}
      <section className="card" aria-labelledby="run-summary">
        <h2 id="run-summary">결과 {statusBadge(r)}</h2>
        {r.status === "running" ? (
          <p>
            진행 중입니다. {r.doneItems}/{r.total} 문항
          </p>
        ) : (
          <TableWrap>
            <table className="stack-table" aria-label="실행 요약">
              <tbody>
                <tr>
                  <th scope="row">합격률</th>
                  <td>
                    {r.passRatePct}% ({r.passed}/{r.total}) · 합격선 {r.thresholdPct}%{r.baselinePct >= 0 && ` · 기준선 ${r.baselinePct}%`}
                  </td>
                </tr>
                <tr>
                  <th scope="row">치명 실패</th>
                  <td>{r.criticalFails}건</td>
                </tr>
                <tr>
                  <th scope="row">평균 점수</th>
                  <td>{r.avgScore}점</td>
                </tr>
                <tr>
                  <th scope="row">지연</th>
                  <td>
                    합 {r.latencyMs}ms · 평균 {r.avgLatencyMs}ms · 95번째 백분위 {r.p95LatencyMs}ms
                  </td>
                </tr>
                <tr>
                  <th scope="row">토큰·비용</th>
                  <td>
                    입력 {r.inputTokens} · 출력 {r.outputTokens} · 비용 {usd(r.costMicroUsd)}
                  </td>
                </tr>
                <tr>
                  <th scope="row">프롬프트 참조</th>
                  <td>{r.promptRef}</td>
                </tr>
              </tbody>
            </table>
          </TableWrap>
        )}
        {write && r.status === "done" && (
          <div className="form-row">
            {r.baseline ? (
              <button type="button" className="btn-secondary" disabled={busy} onClick={() => void baseline(0)}>
                기준선 해제
              </button>
            ) : (
              <button type="button" disabled={busy} onClick={() => void baseline(r.id)}>
                기준선으로 지정
              </button>
            )}
          </div>
        )}
      </section>

      {r.status === "done" && (
        <section className="card" aria-labelledby="run-failed">
          <h2 id="run-failed">실패 문항 {failed.length}건</h2>
          <TableWrap>
            <table className="stack-table" aria-label="실패 문항">
              <thead>
                <tr>
                  <th>번호</th>
                  <th>질문</th>
                  <th>기대 답</th>
                  <th>사유</th>
                </tr>
              </thead>
              <tbody>
                {failed.length === 0 && (
                  <tr>
                    <td colSpan={4}>실패한 문항이 없습니다.</td>
                  </tr>
                )}
                {failed.map((i) => (
                  <tr key={i.seq}>
                    <td data-label="번호" className="num">
                      {i.seq} {i.critical && <Badge tone="bad">치명</Badge>}
                    </td>
                    <td data-label="질문">{i.question}</td>
                    <td data-label="기대 답">{i.expected}</td>
                    <td data-label="사유">{i.detail}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </section>
      )}

      {r.status === "done" && (
        <section className="card" aria-labelledby="run-compare">
          <h2 id="run-compare">다른 실행과 비교</h2>
          <div className="form-row">
            <div className="field">
              <label htmlFor="rc-other">기준 실행</label>
              <select id="rc-other" value={other} onChange={(e) => setOther(e.target.value)}>
                <option value="">고르세요</option>
                {candidates.map((c) => (
                  <option key={c.id} value={c.id}>
                    {runLabel(c)}
                  </option>
                ))}
              </select>
            </div>
            <button type="button" disabled={!other} onClick={() => void compare()}>
              비교
            </button>
          </div>
          {cmp && (
            <>
              <p>
                기준 {runLabel(cmp.a)} → 이번 {runLabel(cmp.b)}. 합격률 {cmp.a.passRatePct}% → {cmp.b.passRatePct}%, 치명 실패 {cmp.a.criticalFails} → {cmp.b.criticalFails}건, 지연 합 {cmp.a.latencyMs} → {cmp.b.latencyMs}ms, 비용 {usd(cmp.a.costMicroUsd)} → {usd(cmp.b.costMicroUsd)}
              </p>
              {cmp.note && <Notice tone="warn">{cmp.note}</Notice>}
              {cmp.sameVersion && (
                <>
                  <p>
                    회귀 {cmp.regressions}건 · 개선 {cmp.improvements}건
                  </p>
                  <TableWrap>
                    <table className="stack-table" aria-label="문항별 비교">
                      <thead>
                        <tr>
                          <th>번호</th>
                          <th>질문</th>
                          <th>기준</th>
                          <th>이번</th>
                          <th />
                        </tr>
                      </thead>
                      <tbody>
                        {cmp.rows.map((c) => (
                          <tr key={c.seq}>
                            <td data-label="번호" className="num">
                              {c.seq}
                            </td>
                            <td data-label="질문">{c.question}</td>
                            <td data-label="기준">{c.aPass ? "통과" : "실패"}</td>
                            <td data-label="이번">{c.bPass ? "통과" : "실패"}</td>
                            <td>{c.change === "regression" ? <Badge tone="bad">회귀</Badge> : c.change === "improvement" ? <Badge tone="ok">개선</Badge> : null}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </TableWrap>
                </>
              )}
            </>
          )}
        </section>
      )}
    </section>
  );
}
