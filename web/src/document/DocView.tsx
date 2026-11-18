// 양식 문서 상세: 필드·본문, 결재선 표(합의 병렬 묶음), 참조·수신, 첨부, 결재 이력, 내 차례일 때 승인·반려·동의·이견.
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { NudgePanel } from "../approval/NudgePanel";
import { Badge, Breadcrumbs, EmptyState, Notice, TableWrap } from "../layout/ui";
import { ReassignDialog, type ReassignTarget } from "../approval/ReassignDialog";
import { PrStatusBadge } from "../purchase/badges";
import { HISTORY_ACTION_LABEL, STEP_STATUS_LABEL, dateTime, label, won } from "../purchase/labels";
import { actorText } from "../purchase/PurchaseView";
import type { DecisionResult, HistoryRecord, PurchaseRequest } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";
import { AuthorityLine } from "./AuthorityLine";
import { CancelSubmit } from "./CancelSubmit";
import { useUnitNames } from "./FormLineAdmin";
import { ReceiptActions } from "./ReceiptActions";
import type { DocReceipt, FieldDef } from "./types";
import { ROLE_LABEL, receiptStatusLabel } from "./types";

type Tone = "info" | "ok" | "warn" | "bad" | "muted";
const STEP_TONE: Record<string, Tone> = { waiting: "muted", pending: "warn", approved: "ok", rejected: "bad", agreed: "ok", objected: "bad", skipped: "muted" };

/** plain 이면 첨부를 링크 없이 이름만 보인다(출력 화면). */
export function show(def: FieldDef, v: unknown, names: Record<number, string>, units: Record<number, string> = {}, plain = false): React.ReactNode {
  if (v === undefined || v === null || v === "") return "—";
  switch (def.type) {
    case "amount":
      return typeof v === "number" ? `${won(v)}` : String(v);
    case "unit":
      return typeof v === "number" ? units[v] ?? `조직 ${v}` : String(v);
    case "table": {
      const rows = Array.isArray(v) ? (v as Record<string, unknown>[]) : [];
      return (
        <TableWrap>
          <table>
            <thead>
              <tr>
                {(def.columns ?? []).map((c) => (
                  <th key={c.key}>{c.label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={i}>
                  {(def.columns ?? []).map((c) => (
                    <td key={c.key} className={c.type === "amount" ? "num" : undefined}>
                      {show(c, r[c.key], names, units, plain)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      );
    }
    case "attachment":
      return (Array.isArray(v) ? (v as number[]) : []).map((id) => (
        <div key={id}>{plain ? names[id] ?? `첨부 ${id}` : <a href={`/api/approval-docs/attachments/${id}`}>{names[id] ?? `첨부 ${id}`}</a>}</div>
      ));
    default:
      return String(v);
  }
}

export function DocView() {
  const { id } = useParams();
  const nav = useNavigate();
  const { user, can } = useAuth();
  const [target, setTarget] = useState<ReassignTarget | null>(null);
  const units = useUnitNames();
  const doc = useLoad(() => api.get<PurchaseRequest>(`/api/approval-docs/${id}`), [id]);
  const hist = useLoad(() => api.get<HistoryRecord[]>(`/api/approvals/${id}/history`), [id]);
  const receipts = useLoad(() => api.get<DocReceipt[]>(`/api/receipts?docId=${id}`), [id]);
  const [comment, setComment] = useState("");
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);

  if (doc.error) return <Notice tone="error">{doc.error.includes("찾을 수") ? "문서를 찾을 수 없습니다." : doc.error}</Notice>;
  if (!doc.data) return <p>불러오는 중…</p>;
  const d = doc.data;
  const names = Object.fromEntries((d.attachments ?? []).map((a) => [a.id, a.filename]));
  const steps = d.steps;
  const mine = steps.find((s) => s.status === "pending" && s.assigneeId === user?.id);
  const myRole = mine?.role ?? "approve";
  // 위임으로 대신 결재하는 경우: 결재 단계가 내 차례가 아니어도 결재 권한이 있으면 버튼을 보이고 서버가 판정한다.
  const delegable = !mine && steps.some((s) => s.status === "pending" && (s.role ?? "approve") === "approve") && (user?.role === "manager" || user?.role === "finance");
  const parallel = new Map<number, number>();
  for (const s of steps) if (s.role === "agree") parallel.set(s.stage ?? 0, (parallel.get(s.stage ?? 0) ?? 0) + 1);
  const canAct = (mine || delegable) && (d.status === "submitted" || d.status === "in_review");
  const agreeStep = myRole === "agree" && !!mine;

  async function withdraw() {
    setError("");
    try {
      await api.post(`/api/approval-docs/${id}/withdraw`);
      setDone("회수했습니다. 임시저장 상태에서 고쳐 다시 상신할 수 있습니다.");
      await doc.reload();
      await hist.reload();
    } catch (e) {
      setError(errorText(e));
    }
  }

  async function decide(approve: boolean) {
    setBusy(true);
    setError("");
    try {
      const res = await api.post<DecisionResult>(`/api/approvals/${id}/decision`, { approve, comment: comment.trim() });
      setComment("");
      setDone(`처리했습니다. 현재 상태는 ${label({ draft: "임시저장", submitted: "상신", in_review: "결재중", rejected: "반려", approved: "결재 완료", ordered: "발주완료" }, res.request.status)}입니다.`);
      await doc.reload();
      await hist.reload();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <Breadcrumbs items={[{ label: "전자결재", to: "/docs" }, { label: d.no }]} />
      {done && <Notice tone="ok">{done}</Notice>}
      <h1>
        {d.title} <small>({d.no})</small>
      </h1>
      <dl className="kv">
        <dt>양식</dt>
        <dd>{d.formName}</dd>
        <dt>상태</dt>
        <dd>
          <PrStatusBadge status={d.status} />
          {d.urgent && <> <Badge tone="bad">긴급</Badge></>}
        </dd>
        <dt>기안자</dt>
        <dd>
          {d.requesterName}
          {d.requesterUnitPath ? ` (${d.requesterUnitPath})` : ""}
        </dd>
        <dt>기안일</dt>
        <dd>{dateTime(d.createdAt)}</dd>
        {d.retentionUntil && (
          <>
            <dt>보존 기한</dt>
            <dd>{d.retentionUntil}</dd>
          </>
        )}
        {d.rejectReason && (
          <>
            <dt>반려 사유</dt>
            <dd>{d.rejectReason}</dd>
          </>
        )}
      </dl>

      {d.disposedAt && (
        <Notice tone="warn">
          폐기된 문서입니다({d.disposedAt.replace(/-/g, ". ")}. 폐기{d.retentionUntil ? `, 보존 기한 ${d.retentionUntil.replace(/-/g, ". ")}. 만료` : ""}). 제목·본문·필드 값·첨부·결재 의견은 지워졌고 문서 번호·양식·기안자·기안일·결재 완료일만 남아 있습니다.
        </Notice>
      )}
      {!d.disposedAt && (
        <>
          <h2>내용</h2>
          <dl className="kv">
            {(d.formFields ?? []).map((f) => (
              <div key={f.key}>
                <dt>{f.label}</dt>
                <dd>{show(f, d.fields?.[f.key], names, units)}</dd>
              </div>
            ))}
          </dl>
        </>
      )}

      <h2>결재선</h2>
      {d.steps.length === 0 ? (
        <EmptyState>아직 상신하지 않아 결재선이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">단계</th>
                <th className="nowrap">역할</th>
                <th className="nowrap">직책</th>
                <th>결재자</th>
                <th>소속 조직</th>
                <th className="nowrap">상태</th>
                <th className="nowrap">처리 시각</th>
                <th>의견</th>
                {can("approval:reassign") && <th />}
              </tr>
            </thead>
            <tbody>
              {steps.map((s) => (
                <tr key={s.seq}>
                  <td className="nowrap">{s.seq}</td>
                  <td className="nowrap">
                    {ROLE_LABEL[s.role ?? "approve"]}
                    {s.role === "agree" && (parallel.get(s.stage ?? 0) ?? 0) > 1 && <> (병렬)</>}
                  </td>
                  <td className="nowrap">{s.title}</td>
                  <td style={{ minWidth: "9em" }}>
                    {s.kind === "finance" ? "재무 담당" : s.assigneeName || "—"} {s.acting && <Badge tone="info">직무대행</Badge>}
                  </td>
                  <td style={{ minWidth: "12em" }}>{s.unitPath || "—"}</td>
                  <td className="nowrap">
                    {s.skipReason ? <Badge tone="muted">({s.skipReason})</Badge> : <Badge tone={STEP_TONE[s.status] ?? "muted"}>{label(STEP_STATUS_LABEL, s.status)}</Badge>}
                    {s.finalAuthority && <> <Badge tone="info">전결</Badge></>}
                  </td>
                  <td className="nowrap">{s.decidedAt ? dateTime(s.decidedAt) : ""}</td>
                  <td>{s.comment}</td>
                  {can("approval:reassign") && (
                    <td className="nowrap">
                      {(s.status === "pending" || s.status === "waiting") && (d.status === "submitted" || d.status === "in_review") && s.kind !== "finance" && (
                        <button
                          type="button"
                          className="btn-secondary btn-sm"
                          onClick={() =>
                            setTarget({
                              prId: d.id,
                              seq: s.seq,
                              title: s.role === "agree" ? "합의" : s.title || "결재자",
                              currentName: s.assigneeName ?? "",
                              currentUnit: s.unitPath,
                              stalled: !!d.noApprover && s.status === "pending",
                            })
                          }
                        >
                          {s.role === "agree" ? "합의자 변경" : "결재자 변경"}
                        </button>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {target && (
        <ReassignDialog
          target={target}
          onClose={() => setTarget(null)}
          onDone={(m) => {
            setTarget(null);
            setDone(m);
            void doc.reload();
            void hist.reload();
          }}
        />
      )}
      <AuthorityLine note={d.authority} />
      <p>
        <Link to={`/docs/${d.id}/print`}>출력 화면</Link>
      </p>
      {steps.some((s) => s.status === "objected") && (
        <Notice tone="warn">
          {steps
            .filter((s) => s.status === "objected")
            .map((s) => `합의자 ${s.assigneeName}님이 이견을 남겼습니다: ${s.comment}`)
            .join(" ")}
        </Notice>
      )}
      {d.noApprover && (
        <Notice tone="warn">
          결재자 없음: 지금 차례 단계를 결재할 수 있는 사람이 없어 결재가 멈춰 있습니다. 결재자가 퇴사했거나 결재 권한을 잃었을 수 있습니다.
          {user?.id === d.requesterId && (
            <>
              {" "}
              <button type="button" className="btn-danger btn-sm" onClick={() => void withdraw()}>
                회수(임시저장으로 되돌리기)
              </button>
            </>
          )}
        </Notice>
      )}
      {user?.id === d.requesterId && (d.status === "submitted" || d.status === "in_review") && <NudgePanel key={`${d.id}-${d.status}`} docId={d.id} />}
      {user?.id === d.requesterId && d.canCancelSubmit && (
        <CancelSubmit
          path={`/api/approval-docs/${id}/cancel-submit`}
          onDone={() => {
            setDone("상신을 취소했습니다. 임시저장 상태에서 고쳐 다시 상신할 수 있습니다.");
            void doc.reload();
            void hist.reload();
          }}
        />
      )}
      {(receipts.data ?? []).length > 0 && (
        <>
          <h2>수신 처리</h2>
          <TableWrap>
            <table>
              <thead>
                <tr>
                  <th>수신처</th>
                  <th className="nowrap">상태</th>
                  <th className="nowrap">담당자</th>
                  <th>비고</th>
                  <th>처리</th>
                </tr>
              </thead>
              <tbody>
                {(receipts.data ?? []).map((r) => (
                  <tr key={r.id}>
                    <td>{r.unitName}</td>
                    <td className="nowrap">{receiptStatusLabel(r.status)}</td>
                    <td className="nowrap">{r.assigneeName || r.receivedBy || "—"}</td>
                    <td>{r.status === "returned" ? `반송 사유: ${r.reason}` : r.comment || "—"}</td>
                    <td>{user && can("receipt:handle") && r.canHandle ? <ReceiptActions receiptId={r.id} status={r.status} onChanged={() => void receipts.reload()} /> : null}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </>
      )}
      {(d.recipients ?? []).filter((r) => !(r.kind === "cc" && r.targetType === "user" && r.note)).length > 0 && (
        <ul>
          {(d.recipients ?? [])
            .filter((r) => !(r.kind === "cc" && r.targetType === "user" && r.note))
            .map((r) => (
              <li key={`${r.kind}-${r.targetType}-${r.targetId}`}>
                {ROLE_LABEL[r.kind]}: {r.name}
                {r.path ? ` (${r.path})` : ""}
              </li>
            ))}
        </ul>
      )}
      {(d.attachments ?? []).length > 0 && (
        <>
          <h2>첨부</h2>
          <ul>
            {(d.attachments ?? []).map((a) => (
              <li key={a.id}>
                <a href={`/api/approval-docs/attachments/${a.id}`}>{a.filename}</a> <span className="hint">{Math.ceil(a.size / 1024).toLocaleString("ko-KR")}KB</span>
              </li>
            ))}
          </ul>
        </>
      )}

      <h2>결재 이력</h2>
      {(hist.data ?? []).length === 0 ? (
        <EmptyState>이력이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">일시</th>
                <th>처리자</th>
                <th className="nowrap">동작</th>
                <th>의견</th>
              </tr>
            </thead>
            <tbody>
              {(hist.data ?? []).map((h) => (
                <tr key={h.id}>
                  <td className="nowrap">{dateTime(h.at)}</td>
                  <td>{actorText(h)}</td>
                  <td className="nowrap">{label(HISTORY_ACTION_LABEL, h.action)}</td>
                  <td>{h.comment}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      {canAct ? (
        <div className="card">
          <h3>{agreeStep ? "합의" : "결재"}</h3>
          <div className="field">
            <label htmlFor="decision-comment">{agreeStep ? "의견 (이견은 사유가 필요합니다)" : "의견 (반려할 때는 사유가 필요합니다)"}</label>
            <textarea id="decision-comment" value={comment} onChange={(e) => setComment(e.target.value)} rows={3} maxLength={1000} />
          </div>
          {error && <Notice tone="error">{error}</Notice>}
          <div className="form-row">
            <button type="button" disabled={busy} onClick={() => void decide(true)}>
              {agreeStep ? "동의" : "승인"}
            </button>
            <button type="button" className="btn-danger" disabled={busy || comment.trim() === ""} onClick={() => void decide(false)}>
              {agreeStep ? "이견" : "반려"}
            </button>
          </div>
        </div>
      ) : null}
      {(d.status === "draft" || d.status === "rejected") && d.requesterId === user?.id && (
        <div className="form-row">
          <button type="button" onClick={() => nav(`/docs/${d.id}/edit`)}>
            수정하기
          </button>
        </div>
      )}
    </section>
  );
}
