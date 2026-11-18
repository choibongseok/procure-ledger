// 재고 실사 화면(B24 ③): 실사 표 작성 → 차이 확인 → 조정 전표 결재 → 재고 반영. 수량을 직접 고치는 곳은 없고 조정은 결재가 끝난 뒤에만 반영된다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import { InventoryTabs } from "./InventoryTabs";
import type { StockCount, Warehouse } from "./stockTypes";
import { COUNT_STATUS_LABEL } from "./stockTypes";

const TONE: Record<string, "ok" | "warn" | "bad" | "info" | "muted"> = {
  draft: "info",
  approving: "warn",
  applied: "ok",
  rejected: "bad",
  no_diff: "muted",
  discarded: "muted",
};

function CountEditor({ id, onChanged }: { id: number; onChanged: () => void }) {
  const c = useLoad(() => api.get<StockCount>(`/api/stock-counts/${id}`), [id]);
  const [edits, setEdits] = useState<
    Record<string, { counted: string; reason: string }>
  >({});
  const [step, setStep] = useState<1 | 2>(1);
  const [err, setErr] = useState("");
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);
  if (c.error) return <Notice tone="error">{c.error}</Notice>;
  if (!c.data) return <p>불러오는 중…</p>;
  const d = c.data;
  const editable = d.status === "draft" || d.status === "rejected";
  const key = (l: { itemId: number; lotId: number }) =>
    `${l.itemId}-${l.lotId}`;
  const val = (l: StockCount["lines"][number]) =>
    edits[key(l)] ?? {
      counted: l.countedQty === null ? "" : String(l.countedQty),
      reason: l.reason,
    };
  const diffOf = (l: StockCount["lines"][number]) => {
    const v = val(l).counted;
    return v === "" ? null : Number(v) - l.systemQty;
  };
  const payload = () => ({
    lines: d.lines.map((l) => ({
      itemId: l.itemId,
      lotId: l.lotId,
      countedQty: val(l).counted === "" ? null : Number(val(l).counted),
      reason: val(l).reason,
    })),
  });
  async function run(fn: () => Promise<unknown>, ok?: string) {
    setErr("");
    setDone("");
    setBusy(true);
    try {
      await fn();
      if (ok) setDone(ok);
      setEdits({});
      await c.reload();
      onChanged();
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const diffRows = d.lines.filter((l) => (diffOf(l) ?? 0) !== 0);
  const uncounted = d.lines.filter((l) => val(l).counted === "").length;
  return (
    <div className="card">
      <h3>
        실사 표 {d.no} · {d.warehouseName} · 기준일 {d.baseDate}{" "}
        <Badge tone={TONE[d.status] ?? "muted"}>
          {COUNT_STATUS_LABEL[d.status] ?? d.status}
        </Badge>
      </h3>
      <ol className="hint" aria-label="진행 단계">
        <li>실사 표 작성 {step === 1 && editable ? "← 지금" : ""}</li>
        <li>차이 확인 {step === 2 && editable ? "← 지금" : ""}</li>
        <li>조정 전표 결재 {d.status === "approving" ? "← 지금" : ""}</li>
        <li>재고 반영 {d.status === "applied" ? "← 완료" : ""}</li>
      </ol>
      {done && <Notice tone="ok">{done}</Notice>}
      {err && <Notice tone="error">{err}</Notice>}
      {d.status === "rejected" && (
        <Notice tone="warn">
          조정 전표가 반려되었습니다. 사유: {d.rejectReason || "—"}. 실사 표를
          고쳐 다시 상신할 수 있습니다.
        </Notice>
      )}
      {d.status === "approving" && (
        <Notice tone="info">
          조정 전표 {d.docNo} 결재 중입니다. 승인 전까지 현재 재고는
          그대로입니다.{" "}
          {d.docId > 0 && <Link to={`/docs/${d.docId}`}>조정 전표 보기</Link>}
        </Notice>
      )}
      {d.movedSince > 0 && editable && (
        <Notice tone="warn">
          전산 수량을 잡은 뒤 이 줄들에 입고·출고·이동 기록이 {d.movedSince}건
          생겼습니다. 상신은 막지 않으며 조정은 &quot;실제 수량 − 실사 표의 전산
          수량&quot;만큼 반영됩니다.{" "}
          <button
            type="button"
            className="btn-secondary btn-sm"
            onClick={() =>
              void run(
                () => api.post(`/api/stock-counts/${id}/refresh`),
                "전산 수량을 지금 재고로 다시 잡았습니다.",
              )
            }
          >
            전산 수량 새로 고침
          </button>
        </Notice>
      )}
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th>품목</th>
              <th>로트</th>
              <th className="num nowrap">전산 수량</th>
              <th className="num nowrap">실제 수량</th>
              <th className="num nowrap">차이</th>
              {step === 2 && <th>차이 사유</th>}
            </tr>
          </thead>
          <tbody>
            {(step === 2
              ? d.lines.filter((l) => (diffOf(l) ?? 0) !== 0)
              : d.lines
            ).map((l) => {
              const diff = diffOf(l);
              return (
                <tr key={key(l)}>
                  <td>
                    {l.itemName} ({l.sku})
                  </td>
                  <td>{l.lotNo || "-"}</td>
                  <td className="num nowrap">
                    {l.systemQty.toLocaleString("ko-KR")}
                  </td>
                  <td className="num nowrap">
                    {editable && step === 1 ? (
                      <input
                        aria-label={`${l.itemName} ${l.lotNo || ""} 실제 수량`}
                        inputMode="numeric"
                        style={{ width: "6em" }}
                        value={val(l).counted}
                        onChange={(e) =>
                          setEdits({
                            ...edits,
                            [key(l)]: { ...val(l), counted: e.target.value },
                          })
                        }
                      />
                    ) : l.countedQty === null && val(l).counted === "" ? (
                      "-"
                    ) : (
                      Number(val(l).counted).toLocaleString("ko-KR")
                    )}
                  </td>
                  <td className="num nowrap">
                    {diff === null ? "-" : diff > 0 ? `+${diff}` : diff}
                  </td>
                  {step === 2 && (
                    <td>
                      {editable ? (
                        <input
                          aria-label={`${l.itemName} ${l.lotNo || ""} 차이 사유`}
                          maxLength={100}
                          value={val(l).reason}
                          onChange={(e) =>
                            setEdits({
                              ...edits,
                              [key(l)]: { ...val(l), reason: e.target.value },
                            })
                          }
                        />
                      ) : (
                        l.reason
                      )}
                    </td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      </TableWrap>
      {editable && step === 1 && (
        <p>
          <button
            type="button"
            className="btn-secondary"
            disabled={busy}
            onClick={() =>
              void run(
                () => api.put(`/api/stock-counts/${id}`, payload()),
                "임시 저장했습니다.",
              )
            }
          >
            임시 저장
          </button>{" "}
          <button
            type="button"
            disabled={busy || uncounted > 0}
            onClick={() =>
              void run(async () => {
                await api.put(`/api/stock-counts/${id}`, payload());
                setStep(2);
              })
            }
          >
            차이 확인으로
          </button>{" "}
          <button
            type="button"
            className="btn-secondary"
            disabled={busy}
            onClick={() =>
              void run(
                () => api.post(`/api/stock-counts/${id}/discard`),
                "실사 표를 폐기했습니다.",
              )
            }
          >
            폐기
          </button>
          {uncounted > 0 && (
            <span className="hint">
              {" "}
              실제 수량을 입력하지 않은 줄이 {uncounted}개 있습니다.
            </span>
          )}
        </p>
      )}
      {editable && step === 2 && (
        <>
          <p className="hint">
            {diffRows.length === 0
              ? "차이가 없어 조정할 내용이 없습니다."
              : `차이가 있는 ${diffRows.length}줄입니다. 차이 사유를 적고 조정 전표를 올리면 결재가 끝난 뒤에 재고가 바뀝니다. 수량을 직접 고칠 수는 없습니다. 조정 전표 결재선: 구매팀장 → 재무.`}
          </p>
          <p>
            <button
              type="button"
              className="btn-secondary"
              onClick={() => setStep(1)}
            >
              실사 표로 돌아가기
            </button>{" "}
            <button
              type="button"
              disabled={busy}
              onClick={() =>
                void run(
                  async () => {
                    await api.put(`/api/stock-counts/${id}`, payload());
                    await api.post(`/api/stock-counts/${id}/submit`);
                    setStep(1);
                  },
                  diffRows.length === 0
                    ? "차이가 없어 실사를 종료했습니다."
                    : "조정 전표를 상신했습니다.",
                )
              }
            >
              {diffRows.length === 0 ? "차이 없음으로 종료" : "조정 전표 상신"}
            </button>
          </p>
        </>
      )}
    </div>
  );
}

export function CountPage() {
  const whs = useLoad(() => api.get<Warehouse[]>("/api/warehouses"), []);
  const list = useLoad(
    () => api.get<StockCount[]>("/api/stock-counts?limit=50"),
    [],
  );
  const [wh, setWh] = useState("");
  const [open, setOpen] = useState<number | null>(null);
  const [err, setErr] = useState("");
  async function create() {
    setErr("");
    try {
      const c = await api.post<StockCount>("/api/stock-counts", {
        warehouseId: Number(wh),
      });
      await list.reload();
      setOpen(c.id);
    } catch (e) {
      setErr(errorText(e));
    }
  }
  return (
    <section>
      <h1>재고</h1>
      <InventoryTabs />
      <h2>재고 실사</h2>
      {(err || list.error) && <Notice tone="error">{err || list.error}</Notice>}
      <div className="form-row">
        <label>
          창고
          <select
            aria-label="실사 창고 선택"
            value={wh}
            onChange={(e) => setWh(e.target.value)}
          >
            <option value="">선택하세요</option>
            {(whs.data ?? [])
              .filter((w) => w.active)
              .map((w) => (
                <option key={w.id} value={w.id}>
                  {w.name}
                </option>
              ))}
          </select>
        </label>
        <button type="button" disabled={!wh} onClick={() => void create()}>
          실사 표 만들기
        </button>
      </div>
      {open !== null && (
        <CountEditor id={open} onChanged={() => void list.reload()} />
      )}
      <h3>실사 목록</h3>
      {(list.data ?? []).length === 0 && (
        <EmptyState>실사 기록이 없습니다.</EmptyState>
      )}
      {(list.data ?? []).length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">실사 번호</th>
                <th className="nowrap">기준일</th>
                <th>창고</th>
                <th className="num nowrap">차이 줄 수</th>
                <th className="nowrap">상태</th>
                <th className="nowrap">조정 전표</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map((c) => (
                <tr key={c.id}>
                  <td className="nowrap">{c.no}</td>
                  <td className="nowrap">{c.baseDate}</td>
                  <td>{c.warehouseName}</td>
                  <td className="num nowrap">{c.diffLines}</td>
                  <td className="nowrap">
                    <Badge tone={TONE[c.status] ?? "muted"}>
                      {COUNT_STATUS_LABEL[c.status] ?? c.status}
                    </Badge>
                  </td>
                  <td className="nowrap">
                    {c.docId > 0 ? (
                      <Link to={`/docs/${c.docId}`}>{c.docNo}</Link>
                    ) : (
                      "-"
                    )}
                  </td>
                  <td className="nowrap">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => setOpen(c.id)}
                    >
                      열기
                    </button>
                    <span className="hint"> {dateTime(c.createdAt)}</span>
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
