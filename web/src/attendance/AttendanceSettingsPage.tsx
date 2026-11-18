// (web/src/attendance). 근태·휴가 설정(B50 5절): 연차 발생 규칙(효력일 이력)과 휴가 종류. 권한 attendance:policy(관리자). 모듈을 켜기 전에 쓴다.
// 법정 기준(연차 15일·가산·25일 상한·소멸 시효)은 사업장 규모·취업규칙에 따라 달라 운영 전에 노무 검토가 필요하다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Badge, ConfirmDialog, Notice, PageHead, TableWrap, useToast } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { BASIS_LABEL } from "./leaveFormat";
import { WorkTypesSection } from "./WorkTypesSection";

interface Policy {
  id: number;
  effectiveFrom: string;
  basis: string;
  workdayMinutes: number;
  hourUnitMinutes: number;
  attendanceRule: boolean;
  carryMaxMinutes: number;
  carryValidMonths: number;
  expireMonths: number;
  firstYearMode: string;
  createdByName: string;
  canceled: boolean;
}
interface LType {
  id: number;
  code: string;
  name: string;
  unit: string;
  balance: string;
  balanceLabel: string;
  paid: boolean;
  proofRequired: boolean;
  maxDays: number;
  system: boolean;
  active: boolean;
}

const today = () => new Date().toLocaleDateString("sv-SE", { timeZone: "Asia/Seoul" });

function carryText(p: Policy): string {
  if (p.carryMaxMinutes === 0) return "이월 없음";
  const days = p.carryMaxMinutes < 0 ? "한도 없음" : `${p.carryMaxMinutes / p.workdayMinutes}일까지`;
  return `${days}(${p.carryValidMonths}개월 안에 사용)`;
}

export function AttendanceSettingsPage() {
  const toast = useToast();
  const pol = useLoad(() => api.get<{ policies: Policy[] }>("/api/admin/attendance/policies"), []);
  const types = useLoad(() => api.get<{ types: LType[] }>("/api/admin/attendance/types"), []);
  const [basis, setBasis] = useState("hire");
  const [workHours, setWorkHours] = useState(8);
  const [hourUnit, setHourUnit] = useState(60);
  const [firstYear, setFirstYear] = useState("monthly");
  const [carryMode, setCarryMode] = useState("none");
  const [carryDays, setCarryDays] = useState(5);
  const [carryMonths, setCarryMonths] = useState(12);
  const [expire, setExpire] = useState(12);
  const [effective, setEffective] = useState(today());
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [cancelFor, setCancelFor] = useState<Policy | null>(null);
  const [tCode, setTCode] = useState("");
  const [tName, setTName] = useState("");
  const [tUnit, setTUnit] = useState("day");
  const [tProof, setTProof] = useState(false);
  const [tMax, setTMax] = useState(0);
  const first = (pol.data?.policies ?? []).filter((p) => !p.canceled).length === 0;

  async function savePolicy(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const workday = workHours * 60;
    try {
      await api.post("/api/admin/attendance/policies", {
        effectiveFrom: effective,
        basis,
        workdayMinutes: workday,
        hourUnitMinutes: hourUnit,
        attendanceRule: true,
        carryMaxMinutes: carryMode === "none" ? 0 : carryMode === "unlimited" ? -1 : carryDays * workday,
        carryValidMonths: carryMonths,
        expireMonths: expire,
        firstYearMode: firstYear,
      });
      toast("연차 규칙을 저장했습니다. 이미 발생한 연차는 바뀌지 않고 효력일부터 새 규칙을 따릅니다.");
      await pol.reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function cancelPolicy(p: Policy) {
    setBusy(true);
    try {
      await api.del(`/api/admin/attendance/policies/${p.id}`);
      toast("규칙을 취소했습니다.");
      setCancelFor(null);
      await pol.reload();
    } catch (err) {
      setError(errorText(err));
      setCancelFor(null);
    } finally {
      setBusy(false);
    }
  }

  async function addType(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.post("/api/admin/attendance/types", { code: tCode, name: tName, unit: tUnit, balance: "", paid: true, proofRequired: tProof, maxDays: tMax, active: true });
      toast("휴가 종류를 추가했습니다.");
      setTCode("");
      setTName("");
      await types.reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function toggle(t: LType) {
    setBusy(true);
    setError("");
    try {
      await api.put(`/api/admin/attendance/types/${t.id}`, { name: t.name, unit: t.unit, balance: t.balance, paid: t.paid, proofRequired: t.proofRequired, maxDays: t.maxDays, active: !t.active });
      await types.reload();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  if (pol.error) return <Notice tone="error">{pol.error}</Notice>;
  return (
    <section>
      <PageHead title="근태·휴가 설정" />
      <Notice tone="warn">
        연차 발생·소멸 같은 법정 기준은 사업장 규모·취업규칙에 따라 달라, <strong>운영 전에 노무 검토가 필요합니다</strong>. 이 화면의 설정이 법정 기준보다 불리하면 저장되지 않습니다.
      </Notice>
      {error && <Notice tone="error">{error}</Notice>}
      <h2>연차 발생 규칙</h2>
      <p className="hint">{first ? "처음 저장하는 규칙은 지난 날짜도 됩니다(그 날짜부터 발생을 따라잡습니다. 그 전의 잔여는 인사 담당이 초기 이관으로 넣습니다)." : "새 규칙의 효력일은 오늘 이후여야 합니다."}</p>
      <form onSubmit={savePolicy} aria-label="연차 규칙 양식">
        <div className="field">
          <label htmlFor="ap-basis">발생 기준</label>
          <select id="ap-basis" value={basis} onChange={(e) => setBasis(e.target.value)}>
            <option value="hire">입사일 기준(입사 기념일마다 발생)</option>
            <option value="fiscal">회계연도 기준(회계연도 시작일에 발생)</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="ap-wd">하루 분량(시간)</label>
          <input id="ap-wd" type="number" min={6} max={10} value={workHours} onChange={(e) => setWorkHours(Number(e.target.value))} required />
        </div>
        <div className="field">
          <label htmlFor="ap-unit">시간 단위 휴가의 최소 단위</label>
          <select id="ap-unit" value={hourUnit} onChange={(e) => setHourUnit(Number(e.target.value))}>
            <option value={60}>60분</option>
            <option value={30}>30분</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="ap-fy">입사 첫해 발생 방식</label>
          <select id="ap-fy" value={firstYear} onChange={(e) => setFirstYear(e.target.value)}>
            <option value="monthly">월 개근 시 월 1일(법정)</option>
            <option value="upfront">입사일에 11일 선지급</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="ap-exp">소멸 기한(개월, 12 이상)</label>
          <input id="ap-exp" type="number" min={12} max={60} value={expire} onChange={(e) => setExpire(Number(e.target.value))} required />
        </div>
        <div className="field">
          <label htmlFor="ap-carry">이월</label>
          <select id="ap-carry" value={carryMode} onChange={(e) => setCarryMode(e.target.value)}>
            <option value="none">이월 없음</option>
            <option value="limited">일수 한도까지 이월</option>
            <option value="unlimited">한도 없이 이월</option>
          </select>
        </div>
        {carryMode === "limited" && (
          <div className="field">
            <label htmlFor="ap-cd">이월 한도(일)</label>
            <input id="ap-cd" type="number" min={1} max={30} value={carryDays} onChange={(e) => setCarryDays(Number(e.target.value))} required />
          </div>
        )}
        {carryMode !== "none" && (
          <div className="field">
            <label htmlFor="ap-cm">이월분 사용 기한(개월)</label>
            <input id="ap-cm" type="number" min={1} max={24} value={carryMonths} onChange={(e) => setCarryMonths(Number(e.target.value))} required />
          </div>
        )}
        <div className="field">
          <label htmlFor="ap-eff">효력일</label>
          <input id="ap-eff" type="date" value={effective} min={first ? undefined : today()} onChange={(e) => setEffective(e.target.value)} required />
        </div>
        <button type="submit" disabled={busy}>
          규칙 저장
        </button>
      </form>
      <h3>규칙 이력</h3>
      <TableWrap>
        <table className="stack-table" aria-label="연차 규칙 이력">
          <thead>
            <tr>
              <th>효력일</th>
              <th>발생 기준</th>
              <th>하루 분량</th>
              <th>소멸·이월</th>
              <th>상태</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {(pol.data?.policies ?? []).map((p) => (
              <tr key={p.id}>
                <td data-label="효력일">{p.effectiveFrom}</td>
                <td data-label="발생 기준">{BASIS_LABEL[p.basis]}</td>
                <td data-label="하루 분량">{p.workdayMinutes / 60}시간</td>
                <td data-label="소멸·이월">
                  {p.expireMonths}개월 뒤 소멸 · {carryText(p)}
                </td>
                <td data-label="상태">{p.canceled ? <Badge tone="muted">취소됨</Badge> : p.effectiveFrom > today() ? <Badge tone="info">예정</Badge> : <Badge tone="ok">적용 중</Badge>}</td>
                <td>
                  {!p.canceled && p.effectiveFrom > today() && (
                    <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => setCancelFor(p)}>
                      취소
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      <h2>휴가 종류</h2>
      <TableWrap>
        <table className="stack-table" aria-label="휴가 종류">
          <thead>
            <tr>
              <th>이름</th>
              <th>단위</th>
              <th>차감 잔여</th>
              <th>증빙</th>
              <th>일수 상한</th>
              <th>상태</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {(types.data?.types ?? []).map((t) => (
              <tr key={t.id}>
                <td data-label="이름">
                  {t.name}
                  {t.system && <span className="hint"> · 기본 종류</span>}
                </td>
                <td data-label="단위">{t.unit === "hour" ? "시간" : "일"}</td>
                <td data-label="차감 잔여">{t.balanceLabel || "차감 없음"}</td>
                <td data-label="증빙">{t.proofRequired ? "필요" : "-"}</td>
                <td data-label="일수 상한">{t.maxDays > 0 ? `${t.maxDays}일` : "-"}</td>
                <td data-label="상태">
                  <Badge tone={t.active ? "ok" : "muted"}>{t.active ? "사용 중" : "사용 중지"}</Badge>
                </td>
                <td>
                  {t.code !== "annual" && (
                    <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void toggle(t)}>
                      {t.active ? "사용 중지" : "다시 사용"}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      <h3>휴가 종류 추가</h3>
      <p className="hint">연차를 차감하는 종류는 새로 만들 수 없습니다(연차·시간 단위 연차를 씁니다). 종류는 지우지 않고 사용 중지만 합니다.</p>
      <form onSubmit={addType} aria-label="휴가 종류 추가 양식">
        <div className="field">
          <label htmlFor="at-code">코드(영문 소문자)</label>
          <input id="at-code" value={tCode} onChange={(e) => setTCode(e.target.value)} pattern="[a-z][a-z0-9_]{1,29}" required />
        </div>
        <div className="field">
          <label htmlFor="at-name">이름</label>
          <input id="at-name" value={tName} maxLength={30} onChange={(e) => setTName(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="at-unit">단위</label>
          <select id="at-unit" value={tUnit} onChange={(e) => setTUnit(e.target.value)}>
            <option value="day">일</option>
            <option value="hour">시간</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="at-max">일수 상한(0 은 없음)</label>
          <input id="at-max" type="number" min={0} max={365} value={tMax} onChange={(e) => setTMax(Number(e.target.value))} />
        </div>
        <div className="field">
          <label>
            <input type="checkbox" checked={tProof} onChange={(e) => setTProof(e.target.checked)} /> 증빙 첨부 필요
          </label>
        </div>
        <button type="submit" disabled={busy}>
          종류 추가
        </button>
      </form>
      <WorkTypesSection />
      {cancelFor && (
        <ConfirmDialog title="규칙 취소" confirmLabel="취소하기" cancelLabel="닫기" busy={busy} onConfirm={() => void cancelPolicy(cancelFor)} onCancel={() => setCancelFor(null)}>
          <p>{cancelFor.effectiveFrom} 부터 적용하려던 규칙을 취소합니다. 이력에는 취소됨으로 남습니다.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}
