//(B46 1단계 화면). 회사별 평가 세트: 목록·상세(문항·버전·CSV). 쓰기는 eval:write, 조회·내보내기는 eval:read.
// 문항 글은 서버가 가려서 저장한 값이다(원문이 없다).
import { useState, type ChangeEvent, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { api, apiDownload, apiFetch } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, LoadingRows, Notice, PageHead, TableWrap } from "../layout/ui";
import { EvalGatePanel } from "./EvalGatePanel";
import { EvalRunPanel } from "./EvalRunPages";
import { serverMessage as errorText } from "../api/serverError";
import { useLoad } from "../purchase/useLoad";

export interface SetRow {
  id: number;
  code: string;
  name: string;
  purpose: string;
  description: string;
  currentVersion: number;
  itemCount: number;
  draftVersion: number;
  baselineRunId: number;
  gateDropPct: number;
  active: boolean;
}

export interface ItemRow {
  id: number;
  seq: number;
  question: string;
  context: string;
  expected: string;
  grader: string;
  tolerance: string;
  rubric: string;
  critical: boolean;
  tags: string;
  maskedCount: number;
}

interface VersionRow {
  version: number;
  status: string;
  revision: number;
  itemCount: number;
  itemsHash: string;
  note: string;
  publishedAt: string;
}

interface Detail extends SetRow {
  versions: VersionRow[];
  selected: VersionRow;
  items: ItemRow[];
}

export const PURPOSE_LABEL: Record<string, string> = { doc: "증빙 문서 판정", policy: "규정 검색", tool: "도구 선택", text: "일반 질의" };
export const GRADER_LABEL: Record<string, string> = { exact: "정확 일치", contains: "포함", number: "숫자(허용 오차)", llm: "LLM 채점" };

const GRADERS_BY_PURPOSE: Record<string, string[]> = { doc: ["exact", "number"], policy: ["contains"], tool: ["exact"], text: ["exact", "contains", "number", "llm"] };

export function EvalSetsPage() {
  const { can } = useAuth();
  const write = can("eval:write");
  const data = useLoad(() => api.get<{ sets: SetRow[] }>("/api/admin/eval/sets"), []);
  const [form, setForm] = useState({ code: "", name: "", purpose: "text", description: "" });
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const sets = data.data?.sets ?? [];

  async function create(ev: FormEvent) {
    ev.preventDefault();
    setBusy(true);
    setNote(null);
    try {
      await api.post("/api/admin/eval/sets", form);
      setForm({ code: "", name: "", purpose: "text", description: "" });
      setNote({ tone: "ok", text: "평가 세트를 만들었습니다." });
      await data.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <PageHead title="평가 세트" />
      <p>우리 회사 업무 문항으로 AI 품질을 확인하는 세트입니다. 모델·라우팅을 바꾸기 전후에 같은 문항으로 비교할 수 있습니다. 문항의 개인정보(사업자번호·계좌·연락처)는 저장 전에 가려집니다.</p>
      {data.error && <Notice tone="error">{data.error}</Notice>}
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      <section className="card" aria-labelledby="eval-sets-list">
        <h2 id="eval-sets-list">세트 목록</h2>
        <TableWrap busy={data.loading}>
          <table className="stack-table" aria-label="평가 세트 목록">
            <thead>
              <tr>
                <th>이름</th>
                <th>용도</th>
                <th>현재 버전</th>
                <th>문항 수</th>
                <th>상태</th>
              </tr>
            </thead>
            <tbody>
              {data.loading && !data.data ? <LoadingRows cols={5} /> : null}
              {!data.loading && sets.length === 0 && (
                <tr>
                  <td colSpan={5}>평가 세트가 없습니다. 자주 쓰는 업무로 10문항만 만들어도 모델을 바꿀 때 큰 도움이 됩니다.</td>
                </tr>
              )}
              {sets.map((s) => (
                <tr key={s.id}>
                  <td data-label="이름">
                    <Link to={`/admin/eval/sets/${s.code}`}>{s.name}</Link> <span className="hint">({s.code})</span>
                  </td>
                  <td data-label="용도">{PURPOSE_LABEL[s.purpose] ?? s.purpose}</td>
                  <td data-label="현재 버전">{s.currentVersion > 0 ? `v${s.currentVersion}` : "게시 전"}{s.draftVersion > 0 && <span className="hint"> · 초안 v{s.draftVersion}</span>}</td>
                  <td data-label="문항 수" className="num">
                    {s.itemCount}개
                  </td>
                  <td data-label="상태">
                    <Badge tone={s.active ? "ok" : "muted"}>{s.active ? "사용 중" : "꺼짐"}</Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      </section>
      {write && (
        <section className="card" aria-labelledby="eval-set-new">
          <h2 id="eval-set-new">새 세트</h2>
          <form onSubmit={create} aria-label="세트 양식">
            <div className="form-row">
              <div className="field">
                <label htmlFor="es-code">세트 코드</label>
                <input id="es-code" value={form.code} maxLength={30} required onChange={(e) => setForm({ ...form, code: e.target.value.toLowerCase() })} />
                <span className="hint">영문 소문자·숫자·-. 한 번 정하면 바꿀 수 없습니다.</span>
              </div>
              <div className="field">
                <label htmlFor="es-name">이름</label>
                <input id="es-name" value={form.name} maxLength={60} required onChange={(e) => setForm({ ...form, name: e.target.value })} />
              </div>
              <div className="field">
                <label htmlFor="es-purpose">용도</label>
                <select id="es-purpose" value={form.purpose} onChange={(e) => setForm({ ...form, purpose: e.target.value })}>
                  {Object.entries(PURPOSE_LABEL).map(([k, v]) => (
                    <option key={k} value={k}>
                      {v}
                    </option>
                  ))}
                </select>
              </div>
            </div>
            <div className="form-row">
              <div className="field">
                <label htmlFor="es-desc">설명(선택)</label>
                <input id="es-desc" value={form.description} maxLength={500} onChange={(e) => setForm({ ...form, description: e.target.value })} />
              </div>
              <button type="submit" disabled={busy}>
                만들기
              </button>
            </div>
          </form>
        </section>
      )}
    </section>
  );
}

interface ItemForm {
  id: number;
  question: string;
  expected: string;
  grader: string;
  tolerance: string;
  rubric: string;
  context: string;
  critical: boolean;
  tags: string;
}

const emptyItem = (grader: string): ItemForm => ({ id: 0, question: "", expected: "", grader, tolerance: "", rubric: "", context: "", critical: false, tags: "" });

export function EvalSetPage() {
  const { code = "" } = useParams();
  const { can } = useAuth();
  const write = can("eval:write");
  const [version, setVersion] = useState(0);
  const data = useLoad(() => api.get<Detail>(`/api/admin/eval/sets/${code}${version ? `?version=${version}` : ""}`), [code, version]);
  const d = data.data;
  const [item, setItem] = useState<ItemForm | null>(null);
  const [note, setNote] = useState<{ tone: "ok" | "error" | "warn"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [pubNote, setPubNote] = useState("");

  const isDraft = d?.selected?.status === "draft";
  const graders = GRADERS_BY_PURPOSE[d?.purpose ?? "text"] ?? ["exact"];

  async function run(fn: () => Promise<string | void>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      const warn = await fn();
      setNote(warn ? { tone: "warn", text: `${ok} ${warn}` } : { tone: "ok", text: ok });
      await data.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  async function saveItem(ev: FormEvent) {
    ev.preventDefault();
    if (!item || !d) return;
    const body = { ...item, revision: d.selected.revision };
    await run(async () => {
      const res =
        item.id === 0
          ? await api.post<{ warnings: string[] }>(`/api/admin/eval/sets/${code}/items`, body)
          : await api.put<{ warnings: string[] }>(`/api/admin/eval/sets/${code}/items/${item.id}`, body);
      setItem(null);
      return (res.warnings ?? []).join(" ");
    }, "문항을 저장했습니다.");
  }

  async function onFile(ev: ChangeEvent<HTMLInputElement>) {
    const f = ev.target.files?.[0];
    ev.target.value = "";
    if (!f) return;
    const text = await f.text();
    await run(async () => {
      const out = await apiFetch<{ imported: number; warnings: string[] }>(`/api/admin/eval/sets/${code}/import`, { method: "POST", headers: { "Content-Type": "text/csv" }, body: text });
      setVersion(0);
      return `${out.imported}개 문항을 가져왔습니다. ${(out.warnings ?? []).join(" ")}`;
    }, "CSV 를 가져왔습니다.");
  }

  if (data.error && !d) return <Notice tone="error">{data.error}</Notice>;
  if (!d) return <p>불러오는 중…</p>;

  return (
    <section>
      <PageHead title={d.name} />
      <p>
        <Link to="/admin/eval/sets">← 평가 세트 목록</Link> · {PURPOSE_LABEL[d.purpose] ?? d.purpose} · 현재 버전 {d.currentVersion > 0 ? `v${d.currentVersion}` : "게시 전"}
      </p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      <section className="card" aria-labelledby="eval-versions">
        <h2 id="eval-versions">버전</h2>
        <TableWrap>
          <table className="stack-table" aria-label="버전 목록">
            <thead>
              <tr>
                <th>버전</th>
                <th>상태</th>
                <th>문항 수</th>
                <th>메모</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {d.versions.map((v) => (
                <tr key={v.version}>
                  <td data-label="버전">v{v.version}</td>
                  <td data-label="상태">
                    <Badge tone={v.status === "published" ? "ok" : "info"}>{v.status === "published" ? "게시됨(고칠 수 없음)" : "초안"}</Badge>
                  </td>
                  <td data-label="문항 수" className="num">
                    {v.itemCount}개
                  </td>
                  <td data-label="메모">{v.note || "-"}</td>
                  <td>
                    <button type="button" className="btn-secondary btn-sm" aria-label={`v${v.version} 보기`} onClick={() => setVersion(v.version)}>
                      {d.selected.version === v.version ? "보는 중" : "보기"}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
        <div className="form-row">
          {write && d.active && !d.draftVersion && (
            <button type="button" disabled={busy} onClick={() => void run(async () => void (await api.post(`/api/admin/eval/sets/${code}/draft`, {})), "새 초안을 만들었습니다.").then(() => setVersion(0))}>
              새 초안 만들기
            </button>
          )}
          {write && isDraft && (
            <>
              <input aria-label="버전 메모" placeholder="버전 메모(선택)" value={pubNote} maxLength={200} onChange={(e) => setPubNote(e.target.value)} />
              <button type="button" disabled={busy} onClick={() => void run(async () => void (await api.post(`/api/admin/eval/sets/${code}/publish`, { note: pubNote })), "버전을 게시했습니다. 게시된 버전은 고칠 수 없습니다.").then(() => (setPubNote(""), setVersion(0)))}>
                이 초안 게시
              </button>
            </>
          )}
          <button type="button" className="btn-secondary" disabled={busy} onClick={() => void run(async () => void (await apiDownload(`/api/admin/eval/sets/${code}/export?version=${d.selected.version}`)), "CSV 를 내려받았습니다.")}>
            CSV 내보내기
          </button>
          {write && d.active && (
            <label className="btn-secondary" style={{ cursor: "pointer" }}>
              CSV 가져오기
              <input type="file" accept=".csv,text/csv" aria-label="CSV 파일 선택" style={{ display: "none" }} onChange={(e) => void onFile(e)} />
            </label>
          )}
        </div>
      </section>

      <EvalRunPanel code={code} published={d.currentVersion > 0} />

      <EvalGatePanel code={code} name={d.name} description={d.description} gateDropPct={d.gateDropPct} hasBaseline={d.baselineRunId > 0} onChanged={data.reload} />

      <section className="card" aria-labelledby="eval-items">
        <h2 id="eval-items">
          문항 (v{d.selected.version} {isDraft ? "초안" : "게시"})
        </h2>
        <TableWrap busy={data.loading}>
          <table className="stack-table" aria-label="문항 목록">
            <thead>
              <tr>
                <th>번호</th>
                <th>질문</th>
                <th>기대 답</th>
                <th>채점</th>
                <th>치명</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {d.items.length === 0 && !data.loading && (
                <tr>
                  <td colSpan={6}>문항이 없습니다. 문항을 추가하거나 CSV 로 가져오세요.</td>
                </tr>
              )}
              {d.items.map((it) => (
                <tr key={it.id}>
                  <td data-label="번호" className="num">
                    {it.seq}
                  </td>
                  <td data-label="질문">
                    {it.question.length > 60 ? `${it.question.slice(0, 60)}…` : it.question} {it.maskedCount > 0 && <Badge tone="warn">가림 {it.maskedCount}</Badge>}
                  </td>
                  <td data-label="기대 답">{it.expected.length > 40 ? `${it.expected.slice(0, 40)}…` : it.expected}</td>
                  <td data-label="채점">
                    {GRADER_LABEL[it.grader] ?? it.grader}
                    {it.grader === "number" && it.tolerance && <span className="hint"> · {it.tolerance}</span>}
                  </td>
                  <td data-label="치명">{it.critical ? <Badge tone="bad">치명</Badge> : "-"}</td>
                  <td>
                    {write && isDraft && (
                      <>
                        <button type="button" className="btn-secondary btn-sm" disabled={busy} aria-label={`${it.seq}번 문항 수정`} onClick={() => setItem({ id: it.id, question: it.question, expected: it.expected, grader: it.grader, tolerance: it.tolerance, rubric: it.rubric, context: it.context, critical: it.critical, tags: it.tags })}>
                          수정
                        </button>{" "}
                        <button type="button" className="btn-secondary btn-sm" disabled={busy} aria-label={`${it.seq}번 문항 삭제`} onClick={() => void run(async () => void (await api.del(`/api/admin/eval/sets/${code}/items/${it.id}?revision=${d.selected.revision}`)), "문항을 지웠습니다.")}>
                          삭제
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
        {write && isDraft && !item && (
          <button type="button" onClick={() => setItem(emptyItem(graders[0]))}>
            문항 추가
          </button>
        )}
        {!isDraft && write && <p className="hint">게시된 버전은 고칠 수 없습니다. 수정하려면 새 초안을 만드세요.</p>}
      </section>

      {write && isDraft && item && (
        <section className="card" aria-labelledby="eval-item-form">
          <h2 id="eval-item-form">{item.id === 0 ? "문항 추가" : "문항 수정"}</h2>
          <form onSubmit={saveItem} aria-label="문항 양식">
            <div className="form-row">
              <div className="field">
                <label htmlFor="ei-q">질문</label>
                <textarea id="ei-q" rows={3} value={item.question} required onChange={(e) => setItem({ ...item, question: e.target.value })} />
              </div>
              <div className="field">
                <label htmlFor="ei-a">기대 답</label>
                <textarea id="ei-a" rows={3} value={item.expected} required={item.grader !== "llm"} onChange={(e) => setItem({ ...item, expected: e.target.value })} />
                {item.grader === "llm" && <span className="hint">LLM 채점은 비워 둘 수 있습니다(아래 채점 기준 글의 필수·금지 줄로 채점합니다).</span>}
              </div>
            </div>
            <div className="form-row">
              <div className="field">
                <label htmlFor="ei-g">채점 기준</label>
                <select id="ei-g" value={item.grader} onChange={(e) => setItem({ ...item, grader: e.target.value })}>
                  {graders.map((g) => (
                    <option key={g} value={g}>
                      {GRADER_LABEL[g]}
                    </option>
                  ))}
                </select>
              </div>
              {item.grader === "number" && (
                <div className="field">
                  <label htmlFor="ei-t">허용 오차</label>
                  <input id="ei-t" value={item.tolerance} placeholder="abs:0.5 또는 rel:1" onChange={(e) => setItem({ ...item, tolerance: e.target.value })} />
                  <span className="hint">abs:값 은 절대값, rel:퍼센트 는 기대 값 대비 비율입니다. 비우면 abs:0.</span>
                </div>
              )}
              <div className="field">
                <label htmlFor="ei-c">
                  <input id="ei-c" type="checkbox" checked={item.critical} onChange={(e) => setItem({ ...item, critical: e.target.checked })} /> 치명 문항
                </label>
                <span className="hint">치명 문항이 하나라도 틀리면 합격률과 상관없이 실패입니다.</span>
              </div>
            </div>
            {item.grader === "llm" && (
              <div className="form-row">
                <div className="field">
                  <label htmlFor="ei-r">채점 기준 글</label>
                  <textarea id="ei-r" rows={3} value={item.rubric} placeholder={"필수: 꼭 들어가야 할 낱말\n금지: 들어가면 안 되는 낱말"} onChange={(e) => setItem({ ...item, rubric: e.target.value })} />
                </div>
              </div>
            )}
            <div className="form-row">
              <div className="field">
                <label htmlFor="ei-tags">태그(선택)</label>
                <input id="ei-tags" value={item.tags} maxLength={200} onChange={(e) => setItem({ ...item, tags: e.target.value })} />
              </div>
              <div className="field">
                <label htmlFor="ei-ctx">맥락(선택, JSON)</label>
                <input id="ei-ctx" value={item.context} placeholder='{"role":"staff"}' onChange={(e) => setItem({ ...item, context: e.target.value })} />
              </div>
            </div>
            <div className="form-row">
              <button type="submit" disabled={busy}>
                저장
              </button>
              <button type="button" className="btn-secondary" onClick={() => setItem(null)}>
                취소
              </button>
            </div>
          </form>
        </section>
      )}
    </section>
  );
}
