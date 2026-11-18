// 증빙 문서 검증 화면: 업로드 → AI 판정(제안) → 근거 항목 → 재무의 최종 확정.
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { businessMessage } from "../purchase/useLoad";
import { ApiError } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { PermissionGate } from "../auth/PermissionGate";
import { Badge, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import {
  OUTCOME_LABEL,
  confirmVerdict,
  IMAGE_NOTICE,
  fieldLabel,
  formatValue,
  itemColumns,
  AUTO_JOURNAL_NOTICE,
  listReceiptsOfPO,
  type ReceiptChoice,
  NO_RECEIPT_NOTICE,
  noteText,
  citationKey,
  getVerdict,
  splitSource,
  imageUploadStatus,
  listVerdicts,
  verifyDocument,
  type ImageUpload,
  type Outcome,
  type Verdict,
} from "./api";

function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    try {
      const j = JSON.parse(e.message) as { error?: string };
      if (j.error) return businessMessage(j.error);
    } catch {
      /* 본문이 JSON 이 아니면 그대로 쓴다 */
    }
    return businessMessage(e.message);
  }
  return "요청을 처리하지 못했습니다.";
}

const OUTCOME_TONE: Record<Outcome, Tone> = {
  match: "ok",
  mismatch: "bad",
  hold: "warn",
};

function OutcomeBadge({
  outcome,
  testId,
}: {
  outcome: Outcome;
  testId?: string;
}) {
  return (
    <span data-testid={testId}>
      <Badge tone={OUTCOME_TONE[outcome]}>{OUTCOME_LABEL[outcome]}</Badge>
    </span>
  );
}

function VerdictView({
  verdict,
  onChanged,
}: {
  verdict: Verdict;
  onChanged: (v: Verdict) => void;
}) {
  const [outcome, setOutcome] = useState<Outcome>(verdict.outcome);
  const [comment, setComment] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const confirmed = verdict.confirmedBy !== 0;
  const [picked, setPicked] = useState("");
  const lines = verdict.extraction.lines;
  const citations = verdict.extraction.citations ?? {};
  const source = verdict.sourceText ?? "";
  const hasSource = source !== "" && Object.keys(citations).length > 0;
  const parts =
    picked && citations[picked] ? splitSource(source, citations[picked]) : null;
  const mismatches = verdict.items.filter((it) => it.result === "mismatch");

  async function confirm(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onChanged(await confirmVerdict(verdict.id, outcome, comment));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-label="판정 결과">
      <h2>
        판정 결과: <OutcomeBadge outcome={verdict.outcome} testId="outcome" />
      </h2>
      <Notice tone="info">
        이 판정은 AI 가 낸 제안입니다. 최종 확정은 재무 담당자가 합니다.
      </Notice>
      {mismatches.length > 0 && (
        <section aria-label="불일치 항목 비교">
          <h3>불일치 항목: 발주·입고와 문서 값 비교</h3>
          <TableWrap>
            <table data-testid="mismatch-compare">
              <thead>
                <tr>
                  <th>항목</th>
                  <th>발주</th>
                  <th>입고</th>
                  <th>문서</th>
                </tr>
              </thead>
              <tbody>
                {mismatches.map((it, i) => {
                  const c = itemColumns(it);
                  return (
                    <tr key={i}>
                      <td>{fieldLabel(it.field, lines)}</td>
                      <td>{formatValue(it.field, c.po)}</td>
                      <td>{formatValue(it.field, c.receipt)}</td>
                      <td>{formatValue(it.field, c.document)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </TableWrap>
        </section>
      )}
      {verdict.receiptId === 0 && (
        <Notice tone="info" testId="no-receipt">
          {NO_RECEIPT_NOTICE}
        </Notice>
      )}
      <TableWrap>
        <table aria-label="발주·입고·문서 대조 항목" data-testid="compare-table">
          <thead>
            <tr>
              <th>항목</th>
              <th>발주</th>
              <th>입고</th>
              <th>문서</th>
              <th>결과</th>
              <th>설명</th>
            </tr>
          </thead>
          <tbody>
            {verdict.items.map((it, i) => {
              const c = itemColumns(it);
              return (
              <tr key={i}>
                <td>
                  {hasSource && citations[citationKey(it.field)] ? (
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      aria-pressed={picked === citationKey(it.field)}
                      onClick={() => setPicked(citationKey(it.field))}
                    >
                      {fieldLabel(it.field, lines)}
                    </button>
                  ) : (
                    fieldLabel(it.field, lines)
                  )}
                </td>
                <td>{formatValue(it.field, c.po)}</td>
                <td>{formatValue(it.field, c.receipt)}</td>
                <td>{formatValue(it.field, c.document)}</td>
                <td className="nowrap">
                  <OutcomeBadge outcome={it.result} />
                </td>
                <td>{noteText(it)}</td>
              </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
      {hasSource ? (
        <section aria-label="원문">
          <h3>원문</h3>
          <p className="hint">
            왼쪽 표의 항목을 누르면 AI 가 읽은 값의 근거가 된 원문 부분이
            표시됩니다. 개인정보로 분류된 글자는 가려져 있습니다.
          </p>
          <pre
            data-testid="source-text"
            
          >
            {parts ? (
              <>
                {parts[0]}
                <mark data-testid="source-highlight">{parts[1]}</mark>
                {parts[2]}
              </>
            ) : (
              source
            )}
          </pre>
        </section>
      ) : (
        <Notice tone="warn" testId="no-citation">
          이 증빙은 원문 인용을 표시할 수 없습니다(이미지이거나 글자를 읽지 못한
          문서). 금액·수량은 원본과 직접 확인해 주세요.
        </Notice>
      )}
      {confirmed ? (
        <p>
          확정됨:{" "}
          <strong>
            {verdict.confirmedOutcome
              ? OUTCOME_LABEL[verdict.confirmedOutcome]
              : ""}
          </strong>
        </p>
      ) : (
        <PermissionGate permission="document:confirm">
          <form onSubmit={confirm} aria-label="최종 확정" className="card">
            <h3>최종 확정</h3>
            <div className="form-row">
              <label>
                확정 결과{" "}
                <select
                  value={outcome}
                  onChange={(e) => setOutcome(e.target.value as Outcome)}
                >
                  {(Object.keys(OUTCOME_LABEL) as Outcome[]).map((o) => (
                    <option key={o} value={o}>
                      {OUTCOME_LABEL[o]}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                사유(AI 판정과 다르게 확정할 때 필수){" "}
                <input
                  value={comment}
                  onChange={(e) => setComment(e.target.value)}
                />
              </label>
              <button type="submit" disabled={busy}>
                확정
              </button>
            </div>
            {error && <Notice tone="error">{error}</Notice>}
          </form>
        </PermissionGate>
      )}
    </section>
  );
}

export function DocVerifyPage() {
  const [poId, setPoId] = useState("");
  const [receiptId, setReceiptId] = useState("");
  const [text, setText] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [verdict, setVerdict] = useState<Verdict | null>(null);
  const [recent, setRecent] = useState<Verdict[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  // 상태를 알기 전에는 이미지를 막아 둔다(서버가 최종 판단).
  const [imageUpload, setImageUpload] = useState<ImageUpload | "">("");

  // capabilities 는 서버가 document:verify 를 요구한다. 읽기만 하는 역할(document:read)은 호출하지 않는다(403 방지).
  const { can } = useAuth();
  const canVerify = can("document:verify");
  useEffect(() => {
    if (!canVerify) return;
    imageUploadStatus()
      .then((r) => setImageUpload(r.imageUpload))
      .catch(() => setImageUpload("disabled"));
  }, [canVerify]);
  const imageOk = imageUpload === "allowed";

  // 발주 번호를 넣으면 그 발주의 입고 목록에서 고른다(자동 매입 전표는 입고가 있어야 만들어진다). 목록을 못 읽으면 번호를 직접 넣는다.
  const [receipts, setReceipts] = useState<ReceiptChoice[]>([]);
  useEffect(() => {
    const n = Number(poId);
    setReceipts([]);
    if (!Number.isInteger(n) || n <= 0) return;
    let live = true;
    const t = setTimeout(() => {
      listReceiptsOfPO(n)
        .then((r) => live && setReceipts(r))
        .catch(() => live && setReceipts([]));
    }, 300);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [poId]);

  const reload = useCallback(async () => {
    try {
      setRecent((await listVerdicts()).verdicts);
    } catch {
      setRecent([]);
    }
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (!poId) {
      setError("대조할 발주 번호를 입력하세요.");
      return;
    }
    if (!file && !text.trim()) {
      setError("증빙 파일을 올리거나 문서 내용을 붙여 넣으세요.");
      return;
    }
    if (file && file.type.startsWith("image/") && !imageOk) {
      setError(
        IMAGE_NOTICE[
          imageUpload === "forbidden_role" ? "forbidden_role" : "disabled"
        ],
      );
      return;
    }
    const form = new FormData();
    form.set("po_id", poId);
    if (receiptId) form.set("receipt_id", receiptId);
    if (file) form.set("file", file);
    else form.set("text", text);
    setBusy(true);
    try {
      setVerdict(await verifyDocument(form));
      void reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>문서 검증</h1>
      <PermissionGate permission="document:verify">
        <form onSubmit={submit} aria-label="증빙 업로드" className="card">
          <div className="form-row">
            <label>
              발주 번호{" "}
              <input
                inputMode="numeric"
                value={poId}
                onChange={(e) => setPoId(e.target.value)}
              />
            </label>
            {receipts.length > 0 ? (
              <label>
                입고(선택){" "}
                <select value={receiptId} onChange={(e) => setReceiptId(e.target.value)} data-testid="receipt-select">
                  <option value="">입고 없이 판정</option>
                  {receipts.map((r) => (
                    <option key={r.id} value={String(r.id)}>
                      {r.no} · {r.receivedAt} · {r.summary}
                    </option>
                  ))}
                </select>
              </label>
            ) : (
              <label>
                입고 번호(선택){" "}
                <input
                  inputMode="numeric"
                  value={receiptId}
                  onChange={(e) => setReceiptId(e.target.value)}
                />
              </label>
            )}
          </div>
          <p className="hint" data-testid="auto-journal-hint">
            {AUTO_JOURNAL_NOTICE}
          </p>
          <div className="field">
            <span id="doc-file-label">
              증빙 파일({imageOk ? "PDF·텍스트·이미지" : "PDF·텍스트"})
            </span>
            <div className="form-row">
              {/* 브라우저 기본 영어 문구 대신 한국어 버튼을 쓴다. 입력은 숨기고 버튼이 연다(키보드로 버튼을 눌러 선택). */}
              <input
                ref={fileInput}
                type="file"
                hidden
                tabIndex={-1}
                aria-labelledby="doc-file-label"
                accept={imageOk ? ".pdf,.txt,image/*" : ".pdf,.txt"}
                onChange={(e) => setFile(e.target.files?.[0] ?? null)}
              />
              <button
                type="button"
                className="btn-secondary"
                aria-describedby="doc-file-label"
                onClick={() => fileInput.current?.click()}
              >
                파일 선택
              </button>
              <span aria-live="polite" data-testid="file-name">
                {file ? `선택한 파일: ${file.name}` : "선택한 파일이 없습니다"}
              </span>
            </div>
            <span className="hint">
              글자가 들어 있는 PDF 는 내용을 읽어 판정합니다. 스캔본 PDF 는
              글자를 읽을 수 없어 판단 보류로 표시되니, 원본을 직접 확인해
              주세요.
              {imageOk &&
                " 이미지로 올린 증빙은 글자 가리기 없이 AI 로 전달되고 전달 사실이 기록되니, 민감한 정보가 있는 이미지는 올리지 마세요."}
            </span>
            {imageUpload !== "" && imageUpload !== "allowed" && (
              <Notice tone="warn" testId="image-notice">
                {IMAGE_NOTICE[imageUpload]}
              </Notice>
            )}
          </div>
          <div className="field">
            <label htmlFor="doc-text">또는 문서 내용 붙여 넣기</label>
            <textarea
              id="doc-text"
              rows={8}
              value={text}
              onChange={(e) => setText(e.target.value)}
            />
          </div>
          <button type="submit" disabled={busy}>
            {busy ? "판정 중…" : "판정 요청"}
          </button>
        </form>
      </PermissionGate>
      {error && <Notice tone="error">{error}</Notice>}
      {verdict && (
        <VerdictView
          key={verdict.id + ":" + verdict.confirmedBy}
          verdict={verdict}
          onChanged={setVerdict}
        />
      )}
      <h2>최근 판정</h2>
      {recent.length === 0 ? (
        <EmptyState>아직 판정 기록이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>판정</th>
                <th>결과</th>
                <th>확정</th>
              </tr>
            </thead>
            <tbody>
              {recent.map((v) => (
                <tr key={v.id}>
                  <td className="nowrap">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() =>
                        void getVerdict(v.id)
                          .then(setVerdict)
                          .catch(() => setVerdict(v))
                      }
                    >
                      #{v.id} · 발주 {v.poId}
                    </button>
                  </td>
                  <td className="nowrap">
                    <OutcomeBadge outcome={v.outcome} />
                  </td>
                  <td className="nowrap">
                    {v.confirmedBy !== 0 ? (
                      <Badge tone="info">확정됨</Badge>
                    ) : (
                      "-"
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}
