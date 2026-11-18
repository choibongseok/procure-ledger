// 가져오기 이력(B35 명세 8절): 묶음 표와 상세(행별 결과). import:read.
import { useState } from "react";
import { Badge, Breadcrumbs, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { importApi, STATUS_LABEL, TARGET_ORDER, type Batch } from "./api";
import { CountSummary, RowsTable } from "./ImportParts";

const TARGET_NAMES: Record<string, string> = { account: "계정과목", warehouse: "창고", vendor: "거래처", item: "품목", budget: "부서 예산", opening_stock: "기초 재고", opening_payable: "미지급 잔액" };
const STATUS_TONE: Record<Batch["status"], Tone> = { previewed: "info", applied: "ok", expired: "muted", canceled: "muted" };

function when(s?: string): string {
  return s ? new Date(s).toLocaleString("ko-KR") : "";
}

function Detail({ no }: { no: string }) {
  const d = useLoad(() => importApi.batch(no), [no]);
  const [note, setNote] = useState("");
  const [err, setErr] = useState("");
  if (d.loading && !d.data) return <p className="hint">불러오는 중…</p>;
  if (d.error) return <Notice tone="error">{d.error}</Notice>;
  if (!d.data) return null;
  const b = d.data;
  return (
    <section className="card" aria-label={`${b.no} 상세`}>
      <h3>
        {b.no} · {b.targetName}
      </h3>
      <p>
        <Badge tone={STATUS_TONE[b.status]}>{STATUS_LABEL[b.status]}</Badge> 요청자 {b.createdBy} · 요청 {when(b.createdAt)}
        {b.appliedAt ? ` · 반영 ${when(b.appliedAt)}` : ""}
        {b.baseDate ? ` · 기준일 ${b.baseDate}` : ""}
      </p>
      <CountSummary counts={b.counts} />
      {(b.summary ?? []).map((s) => (
        <p key={s}>{s}</p>
      ))}
      {(b.journalNos ?? []).length > 0 && (
        <p>
          만들어진 기초 전표:{" "}
          {b.journalNos!.map((n) => (
            <Badge key={n} tone="info">
              {n}
            </Badge>
          ))}
        </p>
      )}
      {err && <Notice tone="error">{err}</Notice>}
      {note && <Notice tone="ok">{note}</Notice>}
      {(b.status === "applied" || b.status === "previewed") && (b.counts.error > 0 || b.counts.warn > 0) && (
        <p>
          <button
            type="button"
            className="btn-secondary"
            onClick={() =>
              void importApi
                .errorFileRead(b.no)
                .then((n) => { setErr(""); setNote(`${n} 을(를) 내려받았습니다.`); })
                .catch((e) => { setNote(""); setErr(errorText(e)); })
            }
          >
            오류·경고 파일 내려받기
          </button>
        </p>
      )}
      {b.status === "expired" || b.status === "canceled" ? (
        <EmptyState>만료되었거나 취소한 미리보기라 행별 결과가 남아 있지 않습니다.</EmptyState>
      ) : (
        <RowsTable rows={b.rows} truncated={b.truncated} truncatedHint={`상위 ${b.rows.length}행만 보입니다. 전체는 오류·경고 파일로 받으세요.`} />
      )}
    </section>
  );
}

export function ImportHistoryPage() {
  const [status, setStatus] = useState("");
  const [target, setTarget] = useState("");
  const [open, setOpen] = useState("");
  const list = useLoad(() => importApi.history(status, target), [status, target]);
  const rows = list.data ?? [];
  return (
    <section>
      <Breadcrumbs items={[{ label: "기초 데이터 가져오기", to: "/admin/import" }, { label: "가져오기 이력" }]} />
      <h1>가져오기 이력</h1>
      <div className="form-row">
        <label>
          상태
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">반영됨</option>
            <option value="previewed">미리보기</option>
            <option value="canceled">취소</option>
            <option value="expired">만료</option>
            <option value="all">전체</option>
          </select>
        </label>
        <label>
          대상
          <select value={target} onChange={(e) => setTarget(e.target.value)}>
            <option value="">전체 대상</option>
            {TARGET_ORDER.map((t) => (
              <option key={t} value={t}>
                {TARGET_NAMES[t]}
              </option>
            ))}
          </select>
        </label>
      </div>
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.loading && !list.data && <p className="hint">불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState hint="기초 데이터 가져오기 화면에서 양식을 내려받아 시작하세요.">조건에 맞는 가져오기 묶음이 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>번호</th>
                <th>대상</th>
                <th>상태</th>
                <th className="num">추가</th>
                <th className="num">변경</th>
                <th className="num">오류</th>
                <th>기준일</th>
                <th>요청자</th>
                <th>반영 시각</th>
                <th>상세</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((b) => (
                <tr key={b.no}>
                  <td className="nowrap">{b.no}</td>
                  <td>{b.targetName}</td>
                  <td>
                    <Badge tone={STATUS_TONE[b.status]}>{STATUS_LABEL[b.status]}</Badge>
                  </td>
                  <td className="num">{b.counts.add}</td>
                  <td className="num">{b.counts.change}</td>
                  <td className="num">{b.counts.error}</td>
                  <td>{b.baseDate || "-"}</td>
                  <td>{b.createdBy}</td>
                  <td>{when(b.appliedAt)}</td>
                  <td>
                    <button type="button" className="btn-secondary btn-sm" aria-expanded={open === b.no} onClick={() => setOpen(open === b.no ? "" : b.no)}>
                      {open === b.no ? "접기" : "상세"}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {open && <Detail key={open} no={open} />}
    </section>
  );
}
