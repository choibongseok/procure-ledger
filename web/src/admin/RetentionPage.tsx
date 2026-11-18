// 보존·폐기 화면(B29 ③, 6절).
// 보존 기한이 지난 완료 문서를 목록으로 보이고, 관리자가 사유와 함께 승인해야만 폐기한다. 자동으로 지우지 않는다.
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap } from "../layout/ui";

interface Candidate {
  id: number;
  no: string;
  title: string;
  formCode: string;
  formName: string;
  requesterName: string;
  completedAt: string;
  retentionUntil: string;
  daysOver: number;
  held: boolean;
  holdReason?: string;
}

const PAGE = 50;
const MAX_BATCH = 100;
const MAX_REASON = 100;

type Tab = "candidates" | "holds";
type Dialog = { kind: "dispose" } | { kind: "hold"; doc: Candidate } | { kind: "clear"; doc: Candidate } | null;

function dayText(d: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(d);
  return m ? `${m[1]}. ${Number(m[2])}. ${Number(m[3])}.` : d;
}

export function RetentionPage() {
  const { can } = useAuth();
  const canDispose = can("retention:dispose");
  const [tab, setTab] = useState<Tab>("candidates");
  const [rows, setRows] = useState<Candidate[] | null>(null);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [picked, setPicked] = useState<Set<number>>(new Set());
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const [dialog, setDialog] = useState<Dialog>(null);
  const [reason, setReason] = useState("");
  const [dialogError, setDialogError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setError("");
    try {
      if (tab === "candidates") {
        const r = await api.get<{ rows: Candidate[]; total: number }>(`/api/retention/candidates?limit=${PAGE}&offset=${offset}`);
        setRows(r.rows ?? []);
        setTotal(r.total);
      } else {
        const r = await api.get<{ rows: Candidate[] }>("/api/retention/holds");
        setRows(r.rows ?? []);
        setTotal((r.rows ?? []).length);
      }
    } catch {
      setRows([]);
      setError("목록을 불러오지 못했습니다. 잠시 후 다시 시도하세요.");
    }
  }, [tab, offset]);

  useEffect(() => {
    setRows(null);
    void load();
  }, [load]);

  function openDialog(d: Dialog) {
    setReason("");
    setDialogError("");
    setDialog(d);
  }

  function toggle(id: number) {
    setPicked((prev) => {
      const n = new Set(prev);
      if (!n.delete(id)) n.add(id);
      return n;
    });
  }

  async function submit() {
    if (!dialog) return;
    const r = reason.trim();
    if (dialog.kind !== "clear" && !r) {
      setDialogError(dialog.kind === "dispose" ? "폐기 사유를 입력하세요." : "폐기 보류 사유를 입력하세요.");
      return;
    }
    setBusy(true);
    setDialogError("");
    try {
      if (dialog.kind === "dispose") {
        const res = await api.post<{ disposed: number }>("/api/retention/dispose", { ids: [...picked], reason: r });
        setDone(`${res.disposed}건을 폐기했습니다.`);
        setPicked(new Set());
      } else if (dialog.kind === "hold") {
        await api.post("/api/retention/hold", { id: dialog.doc.id, reason: r });
        setDone(`${dialog.doc.no} 문서를 폐기 보류했습니다. 보류를 풀기 전에는 폐기 후보에 나오지 않습니다.`);
      } else {
        await api.post(`/api/retention/hold/${dialog.doc.id}/clear`, { reason: r });
        setDone(`${dialog.doc.no} 문서의 폐기 보류를 풀었습니다.`);
      }
      setDialog(null);
      await load();
    } catch (e) {
      // 서버 문구(폐기할 수 없는 문서 번호 등)를 그대로 보인다. 지워진 것은 없다.
      setDialogError(e instanceof Error && e.message ? e.message : "처리하지 못했습니다. 아무 문서도 바뀌지 않았습니다. 잠시 후 다시 시도하세요.");
    } finally {
      setBusy(false);
    }
  }

  const list = rows ?? [];
  const pickedDocs = list.filter((r) => picked.has(r.id));
  const overLimit = picked.size > MAX_BATCH;

  return (
    <section>
      <h1>보존·폐기</h1>
      <p>
        보존 기한이 지난 결재 완료 문서입니다. 법정 보존 양식(구매 품의서·지출 결의서·재고 조정 전표)과 영구 보존 문서는 나타나지 않습니다. 폐기하면 제목·본문·필드 값·첨부·결재 의견이 지워지고
        문서 번호·양식·기안자·기안일·결재 완료일·폐기일만 남습니다. <strong>되돌릴 수 없고</strong>, 서버가 자동으로 지우지 않습니다.
      </p>
      <div className="form-row" role="tablist" aria-label="보존·폐기">
        {(["candidates", "holds"] as Tab[]).map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            className={tab === t ? "" : "btn-secondary"}
            onClick={() => {
              setTab(t);
              setOffset(0);
              setPicked(new Set());
              setDone("");
            }}
          >
            {t === "candidates" ? "폐기 후보" : "폐기 보류"}
          </button>
        ))}
      </div>
      {done && <Notice tone="ok">{done}</Notice>}
      {error && <Notice tone="error">{error}</Notice>}
      {rows === null && !error && <p className="hint">불러오는 중…</p>}
      {rows && rows.length === 0 && !error && (
        <EmptyState hint={tab === "candidates" ? "보존 기한이 지난 완료 문서만 나타납니다. 법정 보존 양식은 나타나지 않습니다." : undefined}>
          {tab === "candidates" ? "폐기할 문서가 없습니다." : "폐기 보류 중인 문서가 없습니다."}
        </EmptyState>
      )}
      {rows && rows.length > 0 && (
        <>
          <TableWrap>
            <table>
              <thead>
                <tr>
                  {tab === "candidates" && canDispose && <th>선택</th>}
                  <th>문서 번호</th>
                  <th>제목</th>
                  <th>양식</th>
                  <th>기안자</th>
                  <th>결재 완료일</th>
                  <th>보존 기한</th>
                  <th>{tab === "candidates" ? "경과" : "보류 사유"}</th>
                  {canDispose && <th />}
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.id}>
                    {tab === "candidates" && canDispose && (
                      <td>
                        <input type="checkbox" aria-label={`${r.no} 선택`} checked={picked.has(r.id)} onChange={() => toggle(r.id)} />
                      </td>
                    )}
                    <td className="nowrap">{r.no}</td>
                    <td>
                      <Link to={`/docs/${r.id}`}>{r.title}</Link>
                    </td>
                    <td className="nowrap">{r.formName}</td>
                    <td className="nowrap">{r.requesterName}</td>
                    <td className="nowrap">{dayText(r.completedAt)}</td>
                    <td className="nowrap">{r.retentionUntil ? dayText(r.retentionUntil) : "-"}</td>
                    <td>{tab === "candidates" ? <Badge tone="warn">{r.daysOver}일 지남</Badge> : r.holdReason || "-"}</td>
                    {canDispose && (
                      <td className="nowrap">
                        {tab === "candidates" ? (
                          <button type="button" className="btn-secondary btn-sm" onClick={() => openDialog({ kind: "hold", doc: r })}>
                            폐기 보류
                          </button>
                        ) : (
                          <button type="button" className="btn-secondary btn-sm" onClick={() => openDialog({ kind: "clear", doc: r })}>
                            보류 해제
                          </button>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          {tab === "candidates" && (
            <div className="form-row">
              <span className="hint">
                전체 {total}건 중 {offset + 1}~{offset + rows.length}
              </span>
              <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
                이전 쪽
              </button>
              <button type="button" className="btn-secondary" disabled={offset + PAGE >= total} onClick={() => setOffset(offset + PAGE)}>
                다음 쪽
              </button>
            </div>
          )}
          {tab === "candidates" && canDispose && (
            <div className="form-row">
              <button type="button" className="btn-danger" disabled={picked.size === 0 || overLimit} onClick={() => openDialog({ kind: "dispose" })}>
                {picked.size > 0 ? `선택한 ${picked.size}건 폐기 승인` : "폐기 승인"}
              </button>
              {picked.size === 0 && <span className="hint">폐기할 문서를 선택하세요.</span>}
              {overLimit && <span className="hint">한 번에 {MAX_BATCH}건까지 폐기할 수 있습니다. 나누어 선택하세요.</span>}
            </div>
          )}
        </>
      )}
      {dialog && (
        <ConfirmDialog
          title={dialog.kind === "dispose" ? `${picked.size}건을 폐기할까요?` : dialog.kind === "hold" ? "폐기 보류" : "폐기 보류 해제"}
          confirmLabel={dialog.kind === "dispose" ? "폐기" : dialog.kind === "hold" ? "보류" : "해제"}
          onConfirm={() => void submit()}
          onCancel={() => setDialog(null)}
          busy={busy}
        >
          {dialog.kind === "dispose" ? (
            <>
              <p>
                폐기하면 제목·본문·필드 값·첨부·결재 의견이 지워지고 문서 번호·양식·기안자·기안일·결재 완료일·폐기일만 남습니다. <strong>되돌릴 수 없습니다.</strong>
              </p>
              <p className="hint">
                {pickedDocs
                  .slice(0, 10)
                  .map((d) => d.no)
                  .join(", ")}
                {pickedDocs.length > 10 ? ` 외 ${pickedDocs.length - 10}건` : ""}
              </p>
            </>
          ) : dialog.kind === "hold" ? (
            <p>
              {dialog.doc.no} 문서를 폐기 후보에서 뺍니다. 법적 분쟁이나 감사가 진행 중인 문서에 씁니다. 보류를 풀기 전에는 폐기할 수 없습니다.
            </p>
          ) : (
            <p>{dialog.doc.no} 문서의 폐기 보류를 풉니다. 보존 기한이 지났으면 다시 폐기 후보에 나옵니다.</p>
          )}
          <div className="field">
            <label htmlFor="retention-reason">{dialog.kind === "dispose" ? "폐기 사유" : dialog.kind === "hold" ? "보류 사유" : "해제 사유(선택)"}</label>
            <input
              id="retention-reason"
              value={reason}
              maxLength={MAX_REASON}
              onChange={(e) => setReason(e.target.value)}
              placeholder={dialog.kind === "dispose" ? "예: 보존 연한 만료 정기 정리" : ""}
            />
          </div>
          {dialogError && <Notice tone="error">{dialogError}</Notice>}
        </ConfirmDialog>
      )}
    </section>
  );
}
