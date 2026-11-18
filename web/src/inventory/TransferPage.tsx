// 창고 이동·창고 관리 화면(B24 ②): 이동 전표(출고 한 줄·입고 한 줄 미리보기), 최근 이동, 창고 목록·등록·중지.
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, EmptyState, Notice, TableWrap, useToast } from "../layout/ui";
import type { Item } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";
import { hasSiteChoice, useSites } from "../finance/useSites";
import { InventoryTabs } from "./InventoryTabs";
import type {
  Allocation,
  LedgerPreview,
  TransferSlip,
  TransferTarget,
  Warehouse,
} from "./stockTypes";

// 서버는 한국 시간(KST) 날짜로 판정하므로 화면의 오늘도 KST 로 맞춘다(UTC 로 하면 새벽에 하루 전 날짜가 된다).
const today = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

interface Line {
  item: Item;
  lotId: number;
  lotNo: string;
  qty: string;
  max: number;
}

/** 이동 목록의 출발·도착 칸. 법인 간 이동이면 법인 이름을 붙이고, 상대 법인이 내 범위 밖이라 창고 이름이 비어 있으면 법인 이름만 보인다(B51). */
function sideName(s: TransferSlip, side: "from" | "to"): string {
  const wh = side === "from" ? s.fromName : s.toName;
  if (!s.intercompany) return wh;
  const ent = side === "from" ? s.fromEntity : s.toEntity;
  return wh ? `${wh} (${ent} 법인)` : `${ent} 법인${side === "from" ? "에서 이동" : "으로 이동"}`;
}

function WarehouseAdmin({
  whs,
  reload,
}: {
  whs: Warehouse[];
  reload: () => Promise<void>;
}) {
  const [f, setF] = useState({ code: "", name: "", location: "", siteId: 0 });
  const siteChoices = useSites();
  const multiSite = hasSiteChoice(siteChoices);
  const [err, setErr] = useState("");
  const toast = useToast();
  async function run(fn: () => Promise<unknown>, ok: string) {
    setErr("");
    try {
      await fn();
      toast(ok);
      await reload();
    } catch (e) {
      setErr(errorText(e));
    }
  }
  return (
    <>
      <h2>창고·위치 관리</h2>
      {err && <Notice tone="error">{err}</Notice>}
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th className="nowrap">창고 코드</th>
              <th>이름</th>
              <th>위치</th>
              <th className="nowrap">상태</th>
              {multiSite && <th className="nowrap">사업장</th>}
              <th className="num nowrap">품목 수</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {whs.map((w) => (
              <tr key={w.id}>
                <td className="nowrap">{w.code}</td>
                <td>{w.name}</td>
                <td>{w.location || "-"}</td>
                <td className="nowrap">{w.active ? "사용 중" : "사용 중지"}</td>
                {multiSite && (
                  <td className="nowrap">
                    <select
                      aria-label={`${w.name} 사업장`}
                      value={w.siteId ?? 0}
                      onChange={(e) =>
                        void run(
                          () => api.put(`/api/warehouses/${w.id}`, { code: w.code, name: w.name, location: w.location, siteId: Number(e.target.value) }),
                          "사업장을 바꿨습니다.",
                        )
                      }
                    >
                      <option value={0}>주사업장</option>
                      {siteChoices
                        .filter((s) => s.kind !== "head")
                        .map((s) => (
                          <option key={s.id} value={s.id}>
                            {s.name}
                          </option>
                        ))}
                    </select>
                  </td>
                )}
                <td className="num nowrap">{w.itemCount}</td>
                <td className="nowrap">
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    onClick={() =>
                      void run(
                        () =>
                          api.post(`/api/warehouses/${w.id}/active`, {
                            active: !w.active,
                          }),
                        w.active ? "사용을 중지했습니다." : "다시 사용합니다.",
                      )
                    }
                  >
                    {w.active ? "사용 중지" : "다시 사용"}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      <h3>창고 등록</h3>
      <div className="form-row">
        <label>
          창고 코드
          <input
            value={f.code}
            placeholder="WH-03"
            onChange={(e) => setF({ ...f, code: e.target.value })}
          />
        </label>
        <label>
          이름
          <input
            value={f.name}
            onChange={(e) => setF({ ...f, name: e.target.value })}
          />
        </label>
        <label>
          위치
          <input
            value={f.location}
            onChange={(e) => setF({ ...f, location: e.target.value })}
          />
        </label>
        {multiSite && (
          <label>
            사업장
            <select value={f.siteId} onChange={(e) => setF({ ...f, siteId: Number(e.target.value) })}>
              <option value={0}>주사업장</option>
              {siteChoices
                .filter((s) => s.kind !== "head")
                .map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
            </select>
          </label>
        )}
        <button
          type="button"
          onClick={() =>
            void run(async () => {
              await api.post("/api/warehouses", f);
              setF({ code: "", name: "", location: "", siteId: 0 });
            }, "창고를 등록했습니다.")
          }
        >
          창고 등록
        </button>
      </div>
    </>
  );
}

export function TransferPage() {
  const whsLoad = useLoad(() => api.get<Warehouse[]>("/api/warehouses"), []);
  const recent = useLoad(
    () => api.get<TransferSlip[]>("/api/stock-transfers?limit=30"),
    [],
  );
  // B51: 다른 법인의 도착 창고 후보. 양쪽 법인에서 재고 관리 권한이 있을 때만 서버가 준다(없거나 실패하면 빈 목록).
  const targetsLoad = useLoad(() => api.get<TransferTarget[]>("/api/stock-transfers?targets=1").catch(() => [] as TransferTarget[]), []);
  const targets = targetsLoad.data ?? [];
  const { currentEntity } = useAuth();
  const whs = whsLoad.data ?? [];
  const active = whs.filter((w) => w.active);
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [date, setDate] = useState(today());
  const [memo, setMemo] = useState("");
  const [lines, setLines] = useState<Line[]>([]);
  const [q, setQ] = useState("");
  const items = useLoad(
    () =>
      q.trim()
        ? api.get<Item[]>(`/api/items?q=${encodeURIComponent(q)}&limit=20`)
        : Promise.resolve([] as Item[]),
    [q],
  );
  const [pick, setPick] = useState("");
  const [lots, setLots] = useState<Allocation[]>([]);
  const [lotPick, setLotPick] = useState("");
  const [preview, setPreview] = useState<LedgerPreview[]>([]);
  const [err, setErr] = useState("");
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  async function choose(id: string) {
    setPick(id);
    setLots([]);
    setLotPick("");
    const item = (items.data ?? []).find((i) => i.id === Number(id));
    if (!item || !from) return;
    try {
      setLots(
        await api.post<Allocation[]>("/api/stock-issues/propose", {
          warehouseId: Number(from),
          itemId: item.id,
          qty: 0,
          method: "manual",
        }),
      );
    } catch (e) {
      setErr(errorText(e));
    }
  }
  function addLine() {
    const item = (items.data ?? []).find((i) => i.id === Number(pick));
    const lot =
      lots.find((l) => String(l.lotId) === lotPick) ??
      (!item?.trackLot ? lots[0] : undefined);
    if (!item || !lot) {
      setErr("품목과 로트를 고르세요(출발 창고에 재고가 있어야 합니다).");
      return;
    }
    setErr("");
    setPreview([]);
    setLines([
      ...lines.filter((l) => !(l.item.id === item.id && l.lotId === lot.lotId)),
      { item, lotId: lot.lotId, lotNo: lot.lotNo, qty: "", max: lot.onHand },
    ]);
  }
  const body = () => ({
    fromId: Number(from),
    toId: Number(to),
    moveDate: date,
    memo,
    lines: lines.map((l) => ({
      itemId: l.item.id,
      lotId: l.lotId,
      qty: Number(l.qty) || 0,
    })),
  });

  async function doPreview() {
    setErr("");
    try {
      setPreview(
        await api.post<LedgerPreview[]>("/api/stock-transfers/preview", body()),
      );
    } catch (e) {
      setPreview([]);
      setErr(errorText(e));
    }
  }
  async function doTransfer() {
    setErr("");
    setBusy(true);
    try {
      const s = await api.post<TransferSlip>("/api/stock-transfers", body());
      toast(`이동을 처리했습니다. 전표 번호 ${s.no}`);
      setLines([]);
      setPreview([]);
      setMemo("");
      await recent.reload();
      await whsLoad.reload();
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const recentValued = (recent.data ?? []).some((s) => s.intercompany && s.costAmount !== undefined);
  const target = targets.find((t) => String(t.id) === to);
  const inter = !!target;
  const fromEntityName = currentEntity?.name ?? "출발";
  const valued = preview.some((p) => p.unitCost !== undefined);
  const noAvg = preview.some((p) => p.noAvg);
  const ready =
    !!from &&
    !!to &&
    from !== to &&
    lines.length > 0 &&
    lines.every((l) => Number(l.qty) > 0);
  return (
    <section>
      <h1>재고</h1>
      <InventoryTabs />
      <h2>이동 전표 작성</h2>
      {(err || whsLoad.error) && (
        <Notice tone="error">{err || whsLoad.error}</Notice>
      )}
      <div className="form-row">
        <label>
          출발 창고
          <select
            aria-label="출발 창고 선택"
            value={from}
            onChange={(e) => {
              setFrom(e.target.value);
              setLines([]);
              setPreview([]);
              setLots([]);
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
          도착 창고
          <select aria-label="도착 창고 선택" value={to} onChange={(e) => setTo(e.target.value)}>
            <option value="">선택하세요</option>
            {active
              .filter((w) => String(w.id) !== from)
              .map((w) => (
                <option key={w.id} value={w.id}>
                  {w.name}
                </option>
              ))}
            {targets.length > 0 && (
              <optgroup label="다른 법인">
                {targets.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name} ({t.entityName})
                  </option>
                ))}
              </optgroup>
            )}
          </select>
        </label>
        <label>
          이동일
          <input
            type="date"
            lang="ko-KR"
            value={date}
            max={today()}
            onChange={(e) => setDate(e.target.value)}
          />
        </label>
      </div>
      {inter && target && (
        <Notice tone="warn" testId="intercompany-notice">
          법인 간 이동입니다. {fromEntityName} 법인의 재고가 {target.entityName} 법인으로 넘어가며, 출발 법인의 이동 시점 평균 단가로 기록됩니다. 전표는 자동으로 만들어지지 않습니다. 재무 담당이 법인 간 거래 전표를 따로 쓰세요.
        </Notice>
      )}
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
          <select aria-label="품목 선택" value={pick} onChange={(e) => void choose(e.target.value)}>
            <option value="">선택하세요</option>
            {(items.data ?? []).map((i) => (
              <option key={i.id} value={i.id}>
                {i.name} ({i.sku})
              </option>
            ))}
          </select>
        </label>
        <label>
          로트
          <select aria-label="로트 선택" value={lotPick} onChange={(e) => setLotPick(e.target.value)}>
            <option value="">
              {lots.length ? "선택하세요" : "출발 창고 재고 없음"}
            </option>
            {lots.map((l) => (
              <option key={l.lotId} value={l.lotId}>
                {l.lotNo || "로트 없음"} (재고{" "}
                {l.onHand.toLocaleString("ko-KR")})
              </option>
            ))}
          </select>
        </label>
        <button type="button" className="btn-secondary" onClick={addLine}>
          품목 줄 추가
        </button>
      </div>
      {lines.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>품목</th>
                <th>로트</th>
                <th className="num nowrap">출발 창고 재고</th>
                <th className="num nowrap">이동 수량</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {lines.map((l, i) => (
                <tr key={`${l.item.id}-${l.lotId}`}>
                  <td>
                    {l.item.name} ({l.item.sku})
                  </td>
                  <td>{l.lotNo || "-"}</td>
                  <td className="num nowrap">
                    {l.max.toLocaleString("ko-KR")}
                  </td>
                  <td className="num nowrap">
                    <input
                      aria-label={`${l.item.name} 이동 수량`}
                      inputMode="numeric"
                      style={{ width: "6em" }}
                      value={l.qty}
                      onChange={(e) => {
                        setPreview([]);
                        setLines(
                          lines.map((x, j) =>
                            j === i ? { ...x, qty: e.target.value } : x,
                          ),
                        );
                      }}
                    />
                  </td>
                  <td>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => setLines(lines.filter((_, j) => j !== i))}
                    >
                      빼기
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
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
          className="btn-secondary"
          disabled={!ready}
          onClick={() => void doPreview()}
        >
          재고 기록 미리보기
        </button>{" "}
        <button
          type="button"
          disabled={!ready || busy}
          onClick={() => void doTransfer()}
        >
          이동 처리
        </button>
      </p>
      {preview.length > 0 && (
        <>
          {noAvg && <Notice tone="warn">출발 법인에 이 품목의 평균 단가가 없어 0원으로 이동합니다.</Notice>}
          <p className="hint">
            이동은 출발 창고의 출고 한 줄과 도착 창고의 입고 한 줄이 함께
            기록됩니다. 둘 중 하나만 반영되는 일은 없습니다.
          </p>
          <TableWrap>
            <table>
              <thead>
                <tr>
                  <th>구분</th>
                  <th>창고</th>
                  <th>품목</th>
                  <th>로트</th>
                  <th className="num nowrap">수량</th>
                  {valued && <th className="num nowrap">이동 단가(출발 법인 평균)</th>}
                  {valued && <th className="num nowrap">이동 금액</th>}
                </tr>
              </thead>
              <tbody>
                {preview.map((p, i) => (
                  <tr key={i}>
                    <td>
                      {p.kind === "transfer_out" ? "이동 출고" : "이동 입고"}
                    </td>
                    <td>
                      {p.warehouseName}
                      {p.intercompany && p.entityName ? ` (${p.entityName} 법인)` : ""}
                    </td>
                    <td>{p.itemName}</td>
                    <td>{p.lotNo || "-"}</td>
                    <td className="num nowrap">
                      {p.qty > 0
                        ? `+${p.qty.toLocaleString("ko-KR")}`
                        : p.qty.toLocaleString("ko-KR")}
                    </td>
                    {valued && <td className="num nowrap">{p.unitCost !== undefined ? `${p.unitCost.toLocaleString("ko-KR")}원` : "-"}</td>}
                    {valued && <td className="num nowrap">{p.amount !== undefined ? `${p.amount.toLocaleString("ko-KR")}원` : "-"}</td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </>
      )}
      <h2>최근 이동</h2>
      <p className="hint">
        잘못 이동했다면 반대 방향으로 이동하세요.
        {(targets.length > 0 || (recent.data ?? []).some((s) => s.intercompany)) && " 반대 이동은 그 시점의 단가로 기록되어 금액이 달라질 수 있습니다."}
      </p>
      {(recent.data ?? []).length === 0 && (
        <EmptyState>이동한 기록이 없습니다.</EmptyState>
      )}
      {(recent.data ?? []).length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">전표 번호</th>
                <th className="nowrap">이동일</th>
                <th>출발</th>
                <th>도착</th>
                <th>품목</th>
                <th className="num nowrap">수량</th>
                {recentValued && <th className="num nowrap">이동 금액</th>}
              </tr>
            </thead>
            <tbody>
              {(recent.data ?? []).flatMap((s) =>
                s.lines.map((l, i) => (
                  <tr key={`${s.id}-${i}`}>
                    <td className="nowrap">
                      {i === 0 ? s.no : ""}
                      {i === 0 && s.intercompany && (
                        <>
                          {" "}
                          <Badge tone="info">법인 간 이동</Badge>
                        </>
                      )}
                    </td>
                    <td className="nowrap">{i === 0 ? s.moveDate : ""}</td>
                    <td>{i === 0 ? sideName(s, "from") : ""}</td>
                    <td>{i === 0 ? sideName(s, "to") : ""}</td>
                    <td>
                      {l.itemName}
                      {l.lotNo ? ` (${l.lotNo})` : ""}
                    </td>
                    <td className="num nowrap">
                      {l.qty.toLocaleString("ko-KR")}
                    </td>
                    {recentValued && <td className="num nowrap">{i === 0 && s.costAmount !== undefined ? `${s.costAmount.toLocaleString("ko-KR")}원` : ""}</td>}
                  </tr>
                )),
              )}
            </tbody>
          </table>
        </TableWrap>
      )}
      <WarehouseAdmin
        whs={whs}
        reload={async () => void (await whsLoad.reload())}
      />
    </section>
  );
}
