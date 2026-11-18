// 출고 화면(B24 ①): 부서 사용 출고 전표 작성(로트 고르는 방식 3가지)과 최근 출고.
// 선입선출·유효기한 임박 우선은 서버가 제안한 로트 표를 채워 주고, 줄마다 수량을 바꿀 수 있으며 합계가 출고 수량과 같아야 한다.
import { useState } from "react";
import { api } from "../api/client";
import { EmptyState, Notice, TableWrap, useToast } from "../layout/ui";
import type { Item } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";
import { InventoryTabs } from "./InventoryTabs";
import type {
  Allocation,
  Department,
  IssueSlip,
  Warehouse,
} from "./stockTypes";
import { ISSUE_METHOD_LABEL } from "./stockTypes";

// 서버는 한국 시간(KST) 날짜로 판정하므로 화면의 오늘도 KST 로 맞춘다(UTC 로 하면 새벽에 하루 전 날짜가 된다).
const today = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

export function IssuePage() {
  const depts = useLoad(
    () => api.get<Department[]>("/api/stock-issues/departments"),
    [],
  );
  const whs = useLoad(() => api.get<Warehouse[]>("/api/warehouses"), []);
  const recent = useLoad(
    () => api.get<IssueSlip[]>("/api/stock-issues?limit=30"),
    [],
  );
  const [dept, setDept] = useState("");
  const [wh, setWh] = useState("");
  const [q, setQ] = useState("");
  const items = useLoad(
    () =>
      q.trim()
        ? api.get<Item[]>(`/api/items?q=${encodeURIComponent(q)}&limit=20`)
        : Promise.resolve([] as Item[]),
    [q],
  );
  const [item, setItem] = useState<Item | null>(null);
  const [qty, setQty] = useState("");
  const [date, setDate] = useState(today());
  const [method, setMethod] = useState<"fifo" | "expiry" | "manual">("fifo");
  const [rows, setRows] = useState<Allocation[]>([]);
  const [memo, setMemo] = useState("");
  const [err, setErr] = useState("");
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  const active = (whs.data ?? []).filter((w) => w.active);
  const want = Number(qty) || 0;
  const sum = rows.reduce((a, r) => a + r.qty, 0);

  async function propose(m = method) {
    setErr("");
    if (!item || !wh) {
      setErr("출고 창고와 품목을 고르세요.");
      return;
    }
    if (m !== "manual" && want <= 0) {
      setErr("출고 수량을 입력하세요.");
      return;
    }
    try {
      setRows(
        await api.post<Allocation[]>("/api/stock-issues/propose", {
          warehouseId: Number(wh),
          itemId: item.id,
          qty: want,
          method: m,
        }),
      );
    } catch (e) {
      setRows([]);
      setErr(errorText(e));
    }
  }

  async function submit() {
    setErr("");
    setBusy(true);
    try {
      const lines = rows
        .filter((r) => r.qty > 0)
        .map((r) => ({ itemId: r.itemId, lotId: r.lotId, qty: r.qty }));
      const s = await api.post<IssueSlip>("/api/stock-issues", {
        departmentId: Number(dept),
        warehouseId: Number(wh),
        issueDate: date,
        method,
        memo,
        lines,
      });
      toast(`출고를 처리했습니다. 전표 번호 ${s.no}`);
      setRows([]);
      setQty("");
      setMemo("");
      await recent.reload();
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const ready =
    !!dept &&
    !!wh &&
    !!item &&
    rows.some((r) => r.qty > 0) &&
    (method === "manual" || sum === want);
  return (
    <section>
      <h1>재고</h1>
      <InventoryTabs />
      <h2>출고 전표 작성</h2>
      {(err || depts.error || whs.error) && (
        <Notice tone="error">{err || depts.error || whs.error}</Notice>
      )}
      <div className="form-row">
        <label>
          출고 부서
          <select value={dept} onChange={(e) => setDept(e.target.value)}>
            <option value="">선택하세요</option>
            {(depts.data ?? []).map((d) => (
              <option key={d.id} value={d.id}>
                {d.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          출고 창고
          <select
            value={wh}
            onChange={(e) => {
              setWh(e.target.value);
              setRows([]);
            }}
          >
            <option value="">선택하세요</option>
            {active.map((w) => (
              <option key={w.id} value={w.id}>
                {w.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          출고일
          <input
            type="date"
            lang="ko-KR"
            value={date}
            max={today()}
            onChange={(e) => setDate(e.target.value)}
          />
        </label>
      </div>
      <div className="form-row">
        <label>
          품목 찾기
          <input
            value={q}
            placeholder="품목 이름 또는 코드"
            onChange={(e) => setQ(e.target.value)}
          />
        </label>
        <label>
          품목
          <select
            aria-label="품목 선택"
            value={item?.id ?? ""}
            onChange={(e) => {
              setItem(
                (items.data ?? []).find(
                  (i) => i.id === Number(e.target.value),
                ) ?? null,
              );
              setRows([]);
            }}
          >
            <option value="">선택하세요</option>
            {(items.data ?? []).map((i) => (
              <option key={i.id} value={i.id}>
                {i.name} ({i.sku})
              </option>
            ))}
          </select>
        </label>
        <label>
          출고 수량
          <input
            inputMode="numeric"
            value={qty}
            onChange={(e) => setQty(e.target.value)}
          />
        </label>
      </div>
      <fieldset>
        <legend>로트 고르는 방식</legend>
        {(["fifo", "expiry", "manual"] as const).map((m) => (
          <label key={m} style={{ marginRight: 16 }}>
            <input
              type="radio"
              name="method"
              checked={method === m}
              onChange={() => {
                setMethod(m);
                void propose(m);
              }}
            />{" "}
            {m === "fifo"
              ? "선입선출(기본): 먼저 입고된 로트부터"
              : m === "expiry"
                ? "유효기한 임박 우선: 기한이 가까운 로트부터"
                : "직접 고르기"}
          </label>
        ))}
      </fieldset>
      <p>
        <button
          type="button"
          className="btn-secondary"
          onClick={() => void propose()}
        >
          로트 배분 계산
        </button>
      </p>
      {rows.length > 0 && (
        <>
          <TableWrap>
            <table>
              <thead>
                <tr>
                  <th>로트</th>
                  <th className="nowrap">첫 입고일</th>
                  <th className="nowrap">유효기한</th>
                  <th className="num nowrap">창고 재고</th>
                  <th className="num nowrap">이번 출고</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r, i) => (
                  <tr key={r.lotId}>
                    <td>{r.lotNo || "로트 없음"}</td>
                    <td className="nowrap">
                      {r.received ? r.received.slice(0, 10) : "-"}
                    </td>
                    <td className="nowrap">{r.expiry || "-"}</td>
                    <td className="num nowrap">
                      {r.onHand.toLocaleString("ko-KR")}
                    </td>
                    <td className="num nowrap">
                      <input
                        aria-label={`${r.lotNo || "로트 없음"} 출고 수량`}
                        inputMode="numeric"
                        style={{ width: "6em" }}
                        value={r.qty}
                        onChange={(e) =>
                          setRows(
                            rows.map((x, j) =>
                              j === i
                                ? {
                                    ...x,
                                    qty: Math.max(
                                      0,
                                      Number(e.target.value) || 0,
                                    ),
                                  }
                                : x,
                            ),
                          )
                        }
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <p
            className={method !== "manual" && sum !== want ? "hint" : undefined}
          >
            {method === "manual"
              ? `합계 ${sum.toLocaleString("ko-KR")}`
              : `수량은 줄마다 바꿀 수 있고 합계가 출고 수량과 같아야 합니다. (합계 ${sum.toLocaleString("ko-KR")} / 출고 수량 ${want.toLocaleString("ko-KR")})`}
          </p>
        </>
      )}
      <label>
        비고(선택)
        <input
          value={memo}
          maxLength={200}
          onChange={(e) => setMemo(e.target.value)}
        />
      </label>
      <p>
        <button
          type="button"
          disabled={!ready || busy}
          onClick={() => void submit()}
        >
          출고 처리
        </button>
      </p>
      <h2>최근 출고</h2>
      {(recent.data ?? []).length === 0 && (
        <EmptyState>출고한 기록이 없습니다.</EmptyState>
      )}
      {(recent.data ?? []).length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">전표 번호</th>
                <th className="nowrap">출고일</th>
                <th>부서</th>
                <th>창고</th>
                <th>품목</th>
                <th>로트</th>
                <th className="num nowrap">수량</th>
                <th className="nowrap">방식</th>
                <th className="nowrap">처리자</th>
              </tr>
            </thead>
            <tbody>
              {(recent.data ?? []).flatMap((s) =>
                s.lines.map((l, i) => (
                  <tr key={`${s.id}-${i}`}>
                    <td className="nowrap">{i === 0 ? s.no : ""}</td>
                    <td className="nowrap">{i === 0 ? s.issueDate : ""}</td>
                    <td>{i === 0 ? s.departmentName : ""}</td>
                    <td>{i === 0 ? s.warehouseName : ""}</td>
                    <td>{l.itemName}</td>
                    <td>{l.lotNo || "-"}</td>
                    <td className="num nowrap">
                      {l.qty.toLocaleString("ko-KR")}
                    </td>
                    <td className="nowrap">
                      {i === 0 ? (ISSUE_METHOD_LABEL[s.method] ?? "") : ""}
                    </td>
                    <td className="nowrap">{i === 0 ? s.actorName : ""}</td>
                  </tr>
                )),
              )}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}
