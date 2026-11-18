// (web/src/org). 조직 일괄 반영(B22): 파일 선택 → 미리보기 → 결과. 6·7절.
import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { ApiError, apiFetch } from "../api/client";
import { Badge, EmptyState, Notice, Stepper, TableWrap, useToast } from "../layout/ui";
import { businessMessage } from "../purchase/useLoad";
import { dayText } from "./types";

interface FileInfo {
  name: string;
  kind: string;
  kindName: string;
  rows: number;
  encoding: string;
}
interface ErrRow {
  file: string;
  line: number;
  col: string;
  msg: string;
  text: string;
}
interface Item {
  effectiveDate: string;
  label: string;
  card: string;
  class: "추가" | "변경" | "퇴사";
  target: string;
  content: string;
  file: string;
  line: number;
}
interface ImpactDoc {
  id: number;
  no: string;
  title: string;
  requester: string;
}
interface Impact {
  who: string;
  why: string;
  count: number;
  docs: ImpactDoc[];
}
interface Preview {
  previewId: string;
  base: string;
  files: FileInfo[];
  counts: Record<string, number>;
  items: Item[];
  itemsTotal: number;
  itemsMore: number;
  errors: ErrRow[];
  moreErrors: number;
  fileErrors: ErrRow[];
  warnings: string[];
  impact: Impact[];
  impactMore: number;
  retroRows: string[];
  noChange: boolean;
}
interface Result {
  batch: number;
  events: number;
}

const CARD_ORDER = [
  "조직 신설",
  "조직 변경",
  "입사",
  "전보",
  "직위 변경",
  "겸직 변경",
  "직무대행",
  "퇴사",
  "변화 없음",
  "정보 차이(반영되지 않음)",
];
const FILTERS = ["전체", "추가", "변경", "퇴사", "변화 없음"] as const;
type Filter = (typeof FILTERS)[number];

const SLOTS: { key: string; label: string }[] = [
  { key: "units", label: "조직 파일" },
  { key: "people", label: "사람 파일" },
  { key: "appointments", label: "발령 파일" },
];

const SERVER_ERROR = "처리하지 못했습니다. 잠시 후 다시 시도하세요.";

const STEPS = ["파일 선택", "미리보기", "결과"] as const;

function errMessage(e: unknown): { text: string; needConfirm?: boolean; retry?: boolean } {
  if (e instanceof ApiError) {
    if (e.status === 409) {
      try {
        const j = JSON.parse(e.message) as { needConfirm?: boolean; message?: string };
        if (j.needConfirm) return { text: j.message ?? "확인이 필요합니다.", needConfirm: true };
      } catch {
        /* 일반 문구 */
      }
      return { text: businessMessage(e.message), retry: true };
    }
    if ((e.status === 400 || e.status === 413) && e.message && !e.message.startsWith("{")) return { text: businessMessage(e.message) };
    if (e.status === 403) return { text: "이 작업을 할 권한이 없습니다." };
  }
  return { text: SERVER_ERROR };
}

function download(name: string, text: string) {
  const blob = new Blob([text], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

export function ImportPage() {
  const toast = useToast();
  const [files, setFiles] = useState<Record<string, File | undefined>>({});
  const [step, setStep] = useState<1 | 2 | 3>(1);
  const [preview, setPreview] = useState<Preview | null>(null);
  const [filter, setFilter] = useState<Filter>("전체");
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(false);
  const [result, setResult] = useState<Result | null>(null);
  const inputs = useRef<Record<string, HTMLInputElement | null>>({});

  const chosen = SLOTS.map((s) => files[s.key]).filter((f): f is File => !!f);

  function form(extra?: Record<string, string>): FormData {
    const fd = new FormData();
    for (const s of SLOTS) {
      const f = files[s.key];
      if (f) fd.append(s.key, f, f.name);
    }
    for (const [k, v] of Object.entries(extra ?? {})) fd.append(k, v);
    return fd;
  }

  function reset() {
    setStep(1);
    setPreview(null);
    setConfirm(false);
    setError("");
    setRetry(false);
    setResult(null);
    setFilter("전체");
  }

  async function doPreview() {
    setError("");
    setRetry(false);
    if (chosen.length === 0) {
      setError("올릴 파일을 하나 이상 선택하세요.");
      return;
    }
    setBusy(true);
    try {
      const p = await apiFetch<Preview>("/api/org/import/preview", { method: "POST", body: form() });
      setPreview(p);
      setConfirm(false);
      setFilter("전체");
      setStep(2);
    } catch (e) {
      setError(errMessage(e).text);
    } finally {
      setBusy(false);
    }
  }

  async function doApply() {
    if (!preview) return;
    setError("");
    setRetry(false);
    setBusy(true);
    try {
      const r = await apiFetch<Result>("/api/org/import/apply", {
        method: "POST",
        body: form({ previewId: preview.previewId, confirm_retroactive: confirm ? "true" : "false" }),
      });
      setResult(r);
      setStep(3);
      toast("조직 일괄 반영을 마쳤습니다.");
    } catch (e) {
      const m = errMessage(e);
      setError(m.text);
      setRetry(!!m.retry);
    } finally {
      setBusy(false);
    }
  }

  async function downloadOf(path: string, name: string) {
    setError("");
    try {
      const text = await apiFetch<string>(path, { method: "POST", body: form() });
      download(name, text);
    } catch (e) {
      setError(errMessage(e).text);
    }
  }

  const hasErrors = !!preview && preview.errors.length + preview.fileErrors.length > 0;
  const needConfirm = !!preview && preview.retroRows.length > 0;
  const canApply = !!preview && !busy && !hasErrors && !preview.noChange && !!preview.previewId && (!needConfirm || confirm);
  const shown = preview ? preview.items.filter((it) => filter === "전체" || it.class === filter) : [];

  return (
    <section>
      <h1>조직 일괄 반영</h1>
      <Stepper steps={STEPS} current={step} />
      {error && (
        <Notice tone="error">
          {error}{" "}
          {retry && (
            <button type="button" className="btn-sm" onClick={reset}>
              다시 미리보기
            </button>
          )}
        </Notice>
      )}

      {step === 1 && (
        <div>
          <p>인사 시스템에서 내려받은 CSV(UTF-8 또는 EUC-KR)를 올립니다. 먼저 미리보기로 변경 내용을 확인하고, 확인한 뒤에만 반영됩니다.</p>
          <div className="import-slots">
            {SLOTS.map((s) => (
              <div key={s.key} className="import-slot">
                <label htmlFor={`import-${s.key}`}>{s.label}</label>
                <input
                  id={`import-${s.key}`}
                  ref={(el) => {
                    inputs.current[s.key] = el;
                  }}
                  type="file"
                  accept=".csv,text/csv"
                  className="import-file"
                  onChange={(e) => setFiles((f) => ({ ...f, [s.key]: e.target.files?.[0] }))}
                />
                <span className="hint">{files[s.key]?.name ?? "선택한 파일 없음"}</span>
              </div>
            ))}
          </div>
          <details className="import-columns">
            <summary>열 설명서 펼치기</summary>
            <TableWrap>
              <table>
                <thead>
                  <tr>
                    <th>파일</th>
                    <th>필수 열</th>
                    <th>그 밖의 열</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td>조직 파일</td>
                    <td>조직코드, 조직명, 종류(회사·본부·부서·팀), 상위조직코드(회사 행만 비움)</td>
                    <td>정렬, 조직장사번, 상태(사용·폐지), 효력일</td>
                  </tr>
                  <tr>
                    <td>사람 파일</td>
                    <td>사번, 이름, 직위, 주소속조직코드(재직자), 입사일(신규), 이메일(입사 행)</td>
                    <td>겸직조직코드(세미콜론 구분), 권한역할, 휴대전화, 내선, 퇴사일, 퇴사구분, 효력일</td>
                  </tr>
                  <tr>
                    <td>발령 파일</td>
                    <td>사번, 종류(전보·직위변경·겸직발령·겸직해제·직무대행·퇴사), 효력일</td>
                    <td>대상조직코드, 새직위, 대행시작일, 대행종료일, 퇴사구분, 사유(소급이면 필수)</td>
                  </tr>
                </tbody>
              </table>
            </TableWrap>
          </details>
          <div className="form-row">
            <button type="button" disabled={busy} onClick={() => void doPreview()}>
              미리보기
            </button>
          </div>
        </div>
      )}

      {step === 2 && preview && (
        <div>
          {hasErrors ? (
            <>
              <Notice tone="error">
                오류 {preview.errors.length + preview.moreErrors + preview.fileErrors.length}개가 있어 반영할 수 없습니다. 오류 행을 고쳐
                다시 올리세요.
              </Notice>
              <ul className="import-errors">
                {[...preview.fileErrors, ...preview.errors].map((e, i) => (
                  <li key={i}>{e.text}</li>
                ))}
              </ul>
              {preview.moreErrors > 0 && (
                <p className="hint">오류가 {preview.moreErrors}개 더 있습니다. 오류 행 전체 내려받기로 확인하세요.</p>
              )}
              <div className="form-row">
                <button
                  type="button"
                  className="btn-secondary"
                  onClick={() => void downloadOf("/api/org/import/errors.csv", "import-errors.csv")}
                >
                  오류 행 내려받기
                </button>
                <button type="button" disabled className="btn-secondary" aria-disabled>
                  이 내용으로 반영
                </button>
                <button type="button" className="btn-secondary" onClick={reset}>
                  다시 선택
                </button>
              </div>
            </>
          ) : (
            <>
              <Notice tone="info">
                인사 시스템에서 받은 파일의 변경 내용을 미리 본 것입니다. 아직 아무것도 반영되지 않았습니다. 이 미리보기는 10분 동안
                유효합니다. 시스템을 다시 시작하면 미리보기를 다시 만들어야 합니다.
              </Notice>
              <h2>읽은 결과</h2>
              <TableWrap>
                <table>
                  <thead>
                    <tr>
                      <th>파일</th>
                      <th>종류</th>
                      <th>읽은 행</th>
                      <th>글자 인코딩</th>
                    </tr>
                  </thead>
                  <tbody>
                    {preview.files.map((f) => (
                      <tr key={f.kind}>
                        <td>{f.name}</td>
                        <td>{f.kindName} 파일</td>
                        <td>{f.rows}행</td>
                        <td>{f.encoding}로 읽었습니다</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
              <h2>건수</h2>
              <ul className="import-cards" aria-label="건수">
                {CARD_ORDER.map((c) => (
                  <li key={c} className="import-card">
                    <span className="hint">{c}</span>
                    <strong>{preview.counts[c] ?? 0}</strong>
                  </li>
                ))}
              </ul>
              {preview.impact.length > 0 && (
                <>
                  <h2>결재 진행 중 건 영향</h2>
                  <p>
                    퇴사·전보·조직 폐지·조직장 해임 대상 중 지금 결재할 차례인 진행 중 건이 있습니다. 반영 뒤에도 상신 당시의 결재선은
                    그대로 유지되지만, 퇴사한 사람의 단계는 "결재·합의자가 사라진 진행 중 건" 에 나타납니다.
                  </p>
                  <TableWrap>
                    <table>
                      <thead>
                        <tr>
                          <th>대상</th>
                          <th>구분</th>
                          <th>건수</th>
                          <th>문서 제목(기안자)</th>
                        </tr>
                      </thead>
                      <tbody>
                        {preview.impact.map((im) => (
                          <tr key={im.who + im.why}>
                            <td>{im.who}</td>
                            <td>{im.why}</td>
                            <td>{im.count}건</td>
                            <td>
                              {im.docs.map((d) => (
                                <div key={d.id}>
                                  {d.title}{" "}
                                  <span className="hint">
                                    {d.no} ({d.requester})
                                  </span>
                                </div>
                              ))}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </TableWrap>
                  {preview.impactMore > 0 && <p className="hint">최대 20건까지 보이고 넘으면 "외 {preview.impactMore}건" 이 붙는다.</p>}
                </>
              )}
              {preview.warnings.length > 0 && (
                <>
                  <h2>경고(반영은 할 수 있습니다)</h2>
                  <ul>
                    {preview.warnings.map((w) => (
                      <li key={w}>{w}</li>
                    ))}
                  </ul>
                </>
              )}
              {preview.noChange ? (
                <EmptyState>반영할 변경이 없습니다. 이미 모두 반영된 파일입니다.</EmptyState>
              ) : (
                <>
                  <h2>변경 목록</h2>
                  <div className="form-row" role="group" aria-label="상태 필터">
                    {FILTERS.map((f) => (
                      <button
                        key={f}
                        type="button"
                        className={filter === f ? "btn-sm" : "btn-secondary btn-sm"}
                        aria-pressed={filter === f}
                        onClick={() => setFilter(f)}
                      >
                        {f}
                      </button>
                    ))}
                  </div>
                  {shown.length === 0 ? (
                    <EmptyState>
                      {filter === "변화 없음" ? "변화 없음 행은 목록에 나오지 않고 건수만 셉니다." : "이 상태의 변경이 없습니다."}
                    </EmptyState>
                  ) : (
                    <TableWrap>
                      <table className="stack-table">
                        <thead>
                          <tr>
                            <th>효력일</th>
                            <th>종류</th>
                            <th>대상</th>
                            <th>내용</th>
                            <th>원본 파일</th>
                            <th>행 번호</th>
                          </tr>
                        </thead>
                        <tbody>
                          {shown.map((it, i) => (
                            <tr key={i}>
                              <td data-label="효력일" className="nowrap">
                                {dayText(it.effectiveDate)}
                              </td>
                              <td data-label="종류" className="nowrap">
                                {it.label}
                              </td>
                              <td data-label="대상">{it.target}</td>
                              <td data-label="내용">{it.content}</td>
                              <td data-label="원본 파일" className="nowrap">
                                {it.file} 파일
                              </td>
                              <td data-label="행 번호" className="nowrap">
                                {it.line}행
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </TableWrap>
                  )}
                  <p className="hint">
                    모두 {preview.itemsTotal}건.
                    {preview.itemsMore > 0 && (
                      <>
                        {" "}
                        500건을 넘어 외 {preview.itemsMore}건은 보이지 않습니다.{" "}
                        <button
                          type="button"
                          className="btn-secondary btn-sm"
                          onClick={() => void downloadOf("/api/org/import/plan.csv", "import-plan.csv")}
                        >
                          전체 계획 내려받기
                        </button>
                      </>
                    )}
                  </p>
                </>
              )}
              {needConfirm && (
                <Notice tone="warn">
                  <p>
                    효력일이 30일을 넘게 지난 행이 있습니다({preview.retroRows[0]}). 소급 반영은 이미 끝난 결재선과 열람 범위에 영향을 줄 수
                    있습니다.
                  </p>
                  <label>
                    <input type="checkbox" checked={confirm} onChange={(e) => setConfirm(e.target.checked)} /> 확인했습니다
                  </label>
                </Notice>
              )}
              <div className="form-row">
                <button type="button" disabled={!canApply} onClick={() => void doApply()}>
                  이 내용으로 반영
                </button>
                <button type="button" className="btn-secondary" onClick={reset}>
                  다시 선택
                </button>
              </div>
            </>
          )}
        </div>
      )}

      {step === 3 && result && (
        <div>
          <p>
            반영했습니다. 변경 {result.events}건이 반영되었습니다(반영 번호 {result.batch}).
          </p>
          <p>
            <Link to="/org">조직도에서 보기</Link>
          </p>
          <Badge tone="ok">완료</Badge>{" "}
          <button
            type="button"
            className="btn-secondary"
            onClick={() => {
              setFiles({});
              reset();
            }}
          >
            새 파일 올리기
          </button>
        </div>
      )}
    </section>
  );
}
