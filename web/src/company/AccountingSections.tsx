// 회사 설정의 회계(회계연도 시작월·통화·부가세율 효력일)와 달력(시간대·공휴일) 구역(B34 단계 2·3).
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface Fiscal {
  startMonth: number;
  name: number;
  thisFrom: string;
  thisTo: string;
  closed: boolean;
  version: number;
  configured: boolean;
}
interface VatRate {
  id: number;
  effectiveFrom: string;
  percent: number;
  createdBy: string;
  canceled: boolean;
  pending: boolean;
}
interface Holiday {
  date: string;
  name: string;
  createdBy: string;
}

type Msg = { tone: "info" | "error"; text: string } | null;

export function AccountingSection() {
  const { can } = useAuth();
  const manage = can("company:manage");
  const fiscal = useLoad(() => api.get<Fiscal>("/api/company/fiscal"), []);
  const rates = useLoad(() => api.get<VatRate[]>("/api/company/vat-rates"), []);
  const [month, setMonth] = useState<number | null>(null);
  const [day, setDay] = useState("");
  const [pct, setPct] = useState("");
  const [confirm, setConfirm] = useState<"fiscal" | "rate" | null>(null);
  const [msg, setMsg] = useState<Msg>(null);
  const [busy, setBusy] = useState(false);
  const f = fiscal.data;
  const chosen = month ?? f?.startMonth ?? 1;

  async function run(fn: () => Promise<unknown>, ok: string) {
    setBusy(true);
    setMsg(null);
    try {
      await fn();
      setMsg({ tone: "info", text: ok });
      setConfirm(null);
      setMonth(null);
      await Promise.all([fiscal.reload(), rates.reload()]);
    } catch (e) {
      setConfirm(null);
      setMsg({ tone: "error", text: errorText(e) });
    } finally {
      setBusy(false);
    }
  }

  if (fiscal.error) return <Notice tone="error">{fiscal.error}</Notice>;
  if (!f) return <p>불러오는 중…</p>;
  const pctNum = Number(pct);
  const rateOk = /^\d{4}-\d{2}-\d{2}$/.test(day) && pct !== "" && Number.isInteger(pctNum) && pctNum >= 0 && pctNum <= 100;

  return (
    <section aria-label="회계 설정">
      <h2>회계</h2>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      <div className="field">
        <label htmlFor="fy-month">회계연도 시작월</label>
        <select id="fy-month" value={chosen} disabled={!manage || busy || !f.configured} onChange={(e) => setMonth(Number(e.target.value))}>
          {Array.from({ length: 12 }, (_, i) => (
            <option key={i + 1} value={i + 1}>
              {i + 1}월
            </option>
          ))}
        </select>
        <span className="hint">
          이번 회계연도: {f.thisFrom} ~ {f.thisTo}({f.name} 회계연도). 시산표의 기본 기간과 수익·비용 이월 표시가 이 설정을 따릅니다. 월 마감·부가세 신고 분기·문서 번호의 연도는 바뀌지 않습니다.
        </span>
        {!f.configured && <span className="hint">회사 정보를 먼저 저장해야 시작월을 정할 수 있습니다.</span>}
        {f.closed && <span className="hint">마감된 월이 있어 시작월을 바꿀 수 없습니다. 마감을 해제한 뒤 바꾸세요.</span>}
        {manage && (
          <button type="button" disabled={busy || !f.configured || chosen === f.startMonth || f.closed} onClick={() => setConfirm("fiscal")}>
            시작월 변경
          </button>
        )}
      </div>
      <p>
        기본 통화: 원, 읽기 전용입니다. 외화는 지원하지 않습니다.
      </p>

      <h3>부가세율</h3>
      <p className="hint">거래일(발주일·입고일·카드 사용일)에 유효한 세율을 씁니다. 이미 만들어진 전표·소명·발주서의 부가세는 바뀌지 않습니다. 등록한 세율은 고치거나 지울 수 없고, 아직 효력이 시작되지 않은 것만 취소할 수 있습니다.</p>
      {(rates.data ?? []).length === 0 ? (
        <EmptyState>등록된 세율이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table className="stack-table" aria-label="부가세율 이력">
            <thead>
              <tr>
                <th className="nowrap">효력일</th>
                <th>세율</th>
                <th>등록자</th>
                <th>상태</th>
                <th>관리</th>
              </tr>
            </thead>
            <tbody>
              {(rates.data ?? []).map((r) => (
                <tr key={r.id}>
                  <td data-label="효력일" className="nowrap">{r.effectiveFrom === "0001-01-01" ? "처음부터" : r.effectiveFrom}</td>
                  <td data-label="세율">{r.percent}%</td>
                  <td data-label="등록자">{r.createdBy || "기본값"}</td>
                  <td data-label="상태">{r.canceled ? <Badge tone="muted">취소됨</Badge> : r.pending ? <Badge tone="warn">시작 전</Badge> : <Badge tone="ok">적용 중</Badge>}</td>
                  <td data-label="관리">
                    {manage && r.pending && (
                      <button type="button" className="btn-secondary" disabled={busy} onClick={() => void run(() => api.post(`/api/company/vat-rates/${r.id}/cancel`), "세율 등록을 취소했습니다.")}>
                        취소
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {manage && (
        <div className="form-row">
          <div className="field">
            <label htmlFor="vr-day">효력일</label>
            <input id="vr-day" type="date" value={day} onChange={(e) => setDay(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="vr-pct">세율(%)</label>
            <input id="vr-pct" inputMode="numeric" value={pct} onChange={(e) => setPct(e.target.value)} />
          </div>
          <button type="button" disabled={busy || !rateOk} onClick={() => setConfirm("rate")}>
            세율 등록
          </button>
        </div>
      )}
      {confirm === "fiscal" && (
        <ConfirmDialog title="회계연도 시작월을 바꿀까요?" confirmLabel="시작월 변경" busy={busy} onCancel={() => setConfirm(null)} onConfirm={() => void run(() => api.put("/api/company/fiscal-start", { month: chosen, version: f.version }), "회계연도 시작월을 바꿨습니다.")}>
          {chosen}월로 바꾸면 시산표의 기본 기간이 새 회계연도 시작일부터로 바뀌고, 수익·비용의 이월 표시가 새 회계연도 기준으로 달라집니다. 전표 데이터는 바뀌지 않습니다.
        </ConfirmDialog>
      )}
      {confirm === "rate" && (
        <ConfirmDialog title="부가세율을 등록할까요?" confirmLabel="세율 등록" busy={busy} onCancel={() => setConfirm(null)} onConfirm={() => void run(() => api.post("/api/company/vat-rates", { effectiveFrom: day, percent: pctNum }), "부가세율을 등록했습니다.")}>
          {day} 이후 거래부터 {pctNum}% 를 적용합니다. 등록한 세율은 고치거나 지울 수 없고, 효력이 시작되기 전에만 취소할 수 있습니다.
        </ConfirmDialog>
      )}
    </section>
  );
}

export function CalendarSection() {
  const { can } = useAuth();
  const manage = can("company:manage");
  const thisYear = new Date(Date.now() + 9 * 3600 * 1000).getUTCFullYear();
  const [year, setYear] = useState(thisYear);
  const list = useLoad(() => api.get<Holiday[]>(`/api/company/holidays?year=${year}`), [year]);
  const [date, setDate] = useState("");
  const [name, setName] = useState("");
  const [bulk, setBulk] = useState("");
  const [preview, setPreview] = useState<{ items: Holiday[]; problems: string[] } | null>(null);
  const [msg, setMsg] = useState<Msg>(null);
  const [busy, setBusy] = useState(false);
  const today = new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

  async function run(fn: () => Promise<unknown>, ok: (r: never) => string) {
    setBusy(true);
    setMsg(null);
    try {
      const r = await fn();
      setMsg({ tone: "info", text: ok(r as never) });
      await list.reload();
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    } finally {
      setBusy(false);
    }
  }
  const noNext = year === thisYear && new Date().getMonth() >= 11 && !(list.data ?? []).some((h) => h.date.startsWith(String(thisYear + 1)));

  return (
    <section aria-label="영업일 달력">
      <h2>시간대·영업일 달력</h2>
      <p>시간대: 한국 표준시, 읽기 전용입니다. 토요일·일요일은 자동으로 휴일이고, 아래에는 공휴일만 등록합니다.</p>
      <p className="hint">휴일에는 소명 기한 알림·부재 알림이 만들어지지 않고 다음 영업일에 합쳐 보냅니다. 소명 기한의 마지막 날이 휴일이면 다음 영업일로 늦춰집니다. 지급 일정표는 휴일 만기일에 다음 영업일을 안내합니다. 만기일과 장부일은 바뀌지 않습니다.</p>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      {noNext && <Notice tone="info">내년 공휴일이 아직 등록되지 않았습니다.</Notice>}
      <div className="field">
        <label htmlFor="hd-year">조회 연도</label>
        <select id="hd-year" value={year} onChange={(e) => setYear(Number(e.target.value))}>
          {[thisYear - 1, thisYear, thisYear + 1].map((y) => (
            <option key={y} value={y}>
              {y}년
            </option>
          ))}
        </select>
      </div>
      {(list.data ?? []).length === 0 ? (
        <EmptyState>{year}년에 등록된 공휴일이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table className="stack-table" aria-label="공휴일">
            <thead>
              <tr>
                <th className="nowrap">날짜</th>
                <th>이름</th>
                <th>등록자</th>
                <th>관리</th>
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map((h) => (
                <tr key={h.date}>
                  <td data-label="날짜" className="nowrap">{h.date}</td>
                  <td data-label="이름">{h.name}</td>
                  <td data-label="등록자">{h.createdBy || "—"}</td>
                  <td data-label="관리">
                    {manage && h.date > today && (
                      <button type="button" className="btn-secondary" disabled={busy} onClick={() => void run(() => api.del(`/api/company/holidays/${h.date}`), () => "공휴일을 삭제했습니다.")}>
                        삭제
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {manage && (
        <>
          <h3>공휴일 등록</h3>
          <div className="form-row">
            <div className="field">
              <label htmlFor="hd-date">날짜</label>
              <input id="hd-date" type="date" value={date} onChange={(e) => setDate(e.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="hd-name">이름</label>
              <input id="hd-name" value={name} maxLength={50} onChange={(e) => setName(e.target.value)} />
            </div>
            <button
              type="button"
              disabled={busy || date === "" || name.trim() === ""}
              onClick={() => void run(() => api.post("/api/company/holidays", { items: [{ date, name }] }), (r: { added: number }) => (r.added > 0 ? "공휴일을 등록했습니다." : "이미 등록된 날짜입니다."))}
            >
              등록
            </button>
          </div>
          <div className="field">
            <label htmlFor="hd-bulk">여러 건 한 번에 입력(한 줄에 “2026-12-25 성탄절”처럼 날짜와 이름, 최대 100줄)</label>
            <textarea id="hd-bulk" rows={4} value={bulk} onChange={(e) => { setBulk(e.target.value); setPreview(null); }} />
            <button
              type="button"
              className="btn-secondary"
              disabled={busy || bulk.trim() === ""}
              onClick={async () => {
                try {
                  setPreview(await api.post<{ items: Holiday[]; problems: string[] }>("/api/company/holidays/preview", { text: bulk }));
                } catch (e) {
                  setMsg({ tone: "error", text: errorText(e) });
                }
              }}
            >
              미리보기
            </button>
          </div>
          {preview && (
            <div aria-label="일괄 입력 미리보기">
              <p>
                등록할 공휴일 {preview.items.length}건, 오류 {preview.problems.length}줄
              </p>
              {preview.problems.length > 0 && (
                <ul>
                  {preview.problems.map((p) => (
                    <li key={p}>{p}</li>
                  ))}
                </ul>
              )}
              <button
                type="button"
                disabled={busy || preview.items.length === 0 || preview.problems.length > preview.items.length}
                onClick={() => void run(async () => (await api.post("/api/company/holidays", { items: preview.items }), setBulk(""), setPreview(null)), () => "공휴일을 등록했습니다. 오류 줄은 제외했습니다.")}
              >
                오류 줄을 빼고 등록
              </button>
              {preview.problems.length > preview.items.length && <span className="hint"> 오류가 절반을 넘어 등록할 수 없습니다. 입력을 고쳐 다시 미리보기 하세요.</span>}
            </div>
          )}
        </>
      )}
      {!manage && <Notice tone="info">공휴일은 관리자만 등록할 수 있습니다.</Notice>}
    </section>
  );
}
