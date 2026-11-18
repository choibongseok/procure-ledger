// 대상별 가져오기 화면(B35 명세 8절): ① 양식 내려받기 ② 파일 올리기 ③ 미리보기 ④ 반영 확인.
// 어떤 대상이든 같은 화면이다(대상별 판정·반영은 서버의 대상 어댑터가 한다). 파일은 서버에 보관하지 않으므로 반영 때 같은 파일을 다시 보낸다.
import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";
import { Badge, Breadcrumbs, ConfirmDialog, EmptyState, Notice, Stepper, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { importApi, type Batch, type Preview } from "./api";
import { CountSummary, RowsTable, STEPS } from "./ImportParts";

type Busy = "" | "download" | "preview" | "apply";

function today(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export function ImportTargetPage() {
  const { target = "" } = useParams();
  const { can } = useAuth();
  const targets = useLoad(() => importApi.targets(), []);
  const info = targets.data?.find((t) => t.target === target);
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [baseDate, setBaseDate] = useState(today());
  const [preview, setPreview] = useState<Preview | null>(null);
  const [result, setResult] = useState<Batch | null>(null);
  const [busy, setBusy] = useState<Busy>("");
  const [error, setError] = useState("");
  const [note, setNote] = useState("");
  const [confirm, setConfirm] = useState(false);

  if (targets.loading && !targets.data) return <p className="hint">불러오는 중…</p>;
  if (targets.error) return <Notice tone="error">{targets.error}</Notice>;
  if (!info) return <Notice tone="error">알 수 없는 가져오기 대상입니다. 기초 데이터 가져오기 화면에서 대상을 고르세요.</Notice>;
  if (!info.allowed || !info.ready) {
    return (
      <section>
        <Breadcrumbs items={[{ label: "기초 데이터 가져오기", to: "/admin/import" }, { label: `${info.name} 가져오기` }]} />
      <h1>{info.name} 가져오기</h1>
        <Notice tone="warn">{info.reason || "지금은 가져올 수 없습니다."}</Notice>
        <p>
          <Link to="/admin/import">기초 데이터 가져오기로 돌아가기</Link>
        </p>
      </section>
    );
  }

  const step: 1 | 2 | 3 | 4 = result ? 4 : preview ? 3 : file ? 2 : 1;
  const busyLabel = busy === "preview" ? "검사 중…" : busy === "apply" ? "반영 중…" : busy === "download" ? "내려받는 중…" : undefined;

  async function run(kind: Busy, fn: () => Promise<void>) {
    setBusy(kind);
    setError("");
    setNote("");
    try {
      await fn();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy("");
    }
  }

  async function discardPreview() {
    // 반영하지 않은 미리보기는 버린다(실패해도 만료로 정리된다).
    if (preview?.batchNo && preview.applicable) await importApi.cancel(target, preview.batchNo).catch(() => undefined);
    setPreview(null);
  }

  const doPreview = () =>
    run("preview", async () => {
      if (!file) return;
      await discardPreview();
      setPreview(await importApi.preview(target, file, info.needsBaseDate ? baseDate : ""));
    });

  const doApply = () =>
    run("apply", async () => {
      if (!file || !preview) return;
      try {
        setResult(await importApi.apply(target, preview.batchNo, preview.token, file));
        setPreview(null);
      } finally {
        setConfirm(false);
      }
    });

  const errorFile = () =>
    run("download", async () => {
      if (!preview?.batchNo) return;
      const name = await importApi.errorFile(target, preview.batchNo);
      setNote(`${name} 을(를) 내려받았습니다.`);
    });

  function pick(f: File | null) {
    setFile(f);
    void discardPreview();
    setResult(null);
    setError("");
  }

  const changes = preview ? preview.counts.add + preview.counts.change : 0;

  return (
    <section>
      <h1>{info.name} 가져오기</h1>
      <Stepper steps={STEPS} current={step} busyLabel={busyLabel} />
      {error && <Notice tone="error">{error}</Notice>}
      {note && <Notice tone="ok">{note}</Notice>}

      {!result && (
        <>
          <section className="card" aria-labelledby="imp-step1">
            <h2 id="imp-step1">1. 양식 내려받기</h2>
            <p className="hint">{info.keyRule}</p>
            <button type="button" className="btn-secondary" disabled={busy !== ""} onClick={() => void run("download", async () => setNote(`${await importApi.template(target)} 을(를) 내려받았습니다.`))}>
              {info.name} 양식 내려받기
            </button>
            <p className="hint">양식의 둘째 시트에 양식 버전과 열별 작성 안내가 들어 있습니다. 아래 표에서도 볼 수 있습니다.</p>
            <TableWrap>
              <table>
                <caption className="hint">열별 작성 안내(* 는 필수)</caption>
                <thead>
                  <tr>
                    <th>열</th>
                    <th>설명</th>
                    <th>예시</th>
                  </tr>
                </thead>
                <tbody>
                  {info.columns.map((c) => (
                    <tr key={c.label}>
                      <td className="nowrap">
                        {c.label}
                        {c.required ? "*" : ""}
                      </td>
                      <td>{c.help}</td>
                      <td>{c.example}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          </section>

          <section className="card" aria-labelledby="imp-step2">
            <h2 id="imp-step2">2. 파일 올리기</h2>
            {info.needsBaseDate && (
              <div className="field">
                <label htmlFor="imp-base">기준일(이 날 현재 값입니다)</label>
                <input id="imp-base" type="date" value={baseDate} max={today()} onChange={(e) => { setBaseDate(e.target.value); void discardPreview(); }} />
                <span className="hint">기준일은 마감되지 않은 달 안의 오늘 이전 날짜여야 합니다. 이 날이 장부일이 됩니다.</span>
              </div>
            )}
            <div className="form-row">
              {/* 브라우저 기본 영어 문구 대신 한국어 버튼을 쓴다. 입력은 숨기고 버튼이 연다. */}
              <input ref={input} type="file" hidden tabIndex={-1} aria-label="가져올 파일" accept=".xlsx" onChange={(e) => pick(e.target.files?.[0] ?? null)} />
              <button type="button" className="btn-secondary" disabled={busy !== ""} onClick={() => input.current?.click()}>
                파일 선택
              </button>
              <span aria-live="polite" data-testid="import-file-name">
                {file ? `선택한 파일: ${file.name}` : "선택한 파일이 없습니다"}
              </span>
              <button type="button" disabled={!file || busy !== "" || (info.needsBaseDate && !baseDate)} onClick={() => void doPreview()}>
                미리보기
              </button>
            </div>
            <p className="hint">엑셀(XLSX) 양식 파일만 올릴 수 있습니다. 파일은 서버에 저장하지 않고 검사만 합니다.</p>
          </section>
        </>
      )}

      {preview && !result && (
        <section className="card" aria-labelledby="imp-step3">
          <h2 id="imp-step3">3. 미리보기</h2>
          <p>{preview.keyRule}</p>
          <CountSummary counts={preview.counts} />
          {(preview.summary ?? []).map((s) => (
            <p key={s}>
              <strong>{s}</strong>
            </p>
          ))}
          {(preview.fileWarnings ?? []).map((w) => (
            <Notice key={w} tone="warn">
              {w}
            </Notice>
          ))}
          {preview.message && <Notice tone={preview.counts.error > 0 ? "error" : "info"}>{preview.message}</Notice>}
          {preview.sameFileBatch && !preview.applicable && preview.counts.error === 0 && <p className="hint">같은 파일을 반영한 묶음: {preview.sameFileBatch}</p>}
          <RowsTable rows={preview.rows} truncated={preview.rowsTruncated} truncatedHint={`상위 ${preview.rows.length}행만 보입니다. 전체는 오류 파일로 받으세요.`} />
          <p>
            {(preview.counts.error > 0 || preview.counts.warn > 0) && preview.batchNo && (
              <button type="button" className="btn-secondary" disabled={busy !== ""} onClick={() => void errorFile()}>
                오류 파일 내려받기
              </button>
            )}{" "}
            <button type="button" disabled={!preview.applicable || busy !== ""} onClick={() => setConfirm(true)}>
              반영하기
            </button>{" "}
            <button type="button" className="btn-secondary" disabled={busy !== ""} onClick={() => void discardPreview()}>
              미리보기 버리기
            </button>
          </p>
        </section>
      )}

      {confirm && preview && (
        <ConfirmDialog title={`${info.name} 반영`} confirmLabel="반영" onConfirm={() => void doApply()} onCancel={() => setConfirm(false)} busy={busy === "apply"}>
          {changes.toLocaleString("ko-KR")}건을 반영합니다. 되돌릴 수 없습니다.
          {info.needsBaseDate ? ` 기준일은 ${preview.baseDate} 입니다.` : ""}
        </ConfirmDialog>
      )}

      {result && (
        <section className="card" aria-labelledby="imp-step4">
          <h2 id="imp-step4">4. 반영 결과</h2>
          <Notice tone="ok">
            {result.targetName} {result.no} 을(를) 반영했습니다.
          </Notice>
          <CountSummary counts={result.counts} />
          {(result.journalNos ?? []).length > 0 && (
            <p>
              만들어진 기초 전표: {result.journalNos!.map((n) => (
                <Badge key={n} tone="info">
                  {n}
                </Badge>
              ))}{" "}
              <span className="hint">승인 요청과 결재는 전표 화면에서 진행합니다.</span>
            </p>
          )}
          {(result.summary ?? []).map((s) => (
            <p key={s}>{s}</p>
          ))}
          <p>
            <Link to="/admin/import">다른 대상 가져오기</Link>
            {can("import:read") && (
              <>
                {" · "}
                <Link to="/admin/import/history">가져오기 이력</Link>
              </>
            )}
          </p>
        </section>
      )}
      {!preview && !result && !file && <EmptyState>양식을 채운 파일을 선택하면 미리보기를 할 수 있습니다.</EmptyState>}
    </section>
  );
}
