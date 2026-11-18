// (web/src/org). 조직 변경·인사 발령 입력 폼. 미리보기를 본 뒤에만 등록할 수 있고, 30일 넘는 소급은 한 번 더 확인한다.
import { useMemo, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { PersonPicker } from "./PersonPicker";
import {
  ABSENCE_KINDS,
  KIND_LABEL,
  LEAVE_KINDS,
  ROLE_OPTIONS,
  dayText,
  flatten,
  readError,
  todayKST,
  type EventRow,
  type OrgKind,
  type PersonCard,
  type Rank,
  type TreeNode,
} from "./types";

/** 발령 종류의 한국어 이름과 필요한 입력. */
export const KIND_FORM: Record<string, string> = {
  unit_create: "조직 신설",
  unit_rename: "조직 이름 변경",
  unit_move: "조직 이동",
  unit_sort: "조직 순서 변경",
  unit_close: "조직 폐지",
  unit_reopen: "조직 재개",
  head_set: "조직장 보임",
  head_clear: "조직장 해임",
  acting_set: "직무대행",
  hire: "입사",
  transfer: "전보",
  rank_change: "직위 변경",
  concurrent_add: "겸직 발령",
  concurrent_end: "겸직 해제",
  leave: "퇴사",
  absence_start: "휴직",
  absence_end: "복직",
};

interface Props {
  kind: string;
  tree: TreeNode | null;
  ranks: Rank[];
  /** 대상 조직이 정해진 경우(조직 관리 화면). */
  unit?: TreeNode | null;
  /** 대상 사람이 미리 정해진 경우(조직도 사람 카드에서 넘어옴). */
  person?: PersonCard | null;
  /** 정정할 발령. 있으면 취소와 새 발령 등록을 한 번에 한다. */
  correcting?: EventRow | null;
  onDone: (message: string) => void;
  onCancel?: () => void;
}

interface Preview {
  effects: string[];
  warnings: string[];
  pendingApprovals: number;
  pendingDocs?: { id: number; no: string; title: string; requester: string }[];
  retroactive: boolean;
}

const NEEDS_PERSON = new Set([
  "head_set",
  "acting_set",
  "transfer",
  "rank_change",
  "concurrent_add",
  "concurrent_end",
  "leave",
  "absence_start",
  "absence_end",
]);
const NEEDS_TO_UNIT = new Set(["hire", "transfer", "concurrent_add", "concurrent_end"]);
const NEEDS_RANK = new Set(["hire", "rank_change"]);

export function CommandForm({ kind, tree, ranks, unit, person, correcting, onDone, onCancel }: Props) {
  const today = todayKST();
  const [effective, setEffective] = useState(today);
  const [reason, setReason] = useState("");
  const [who, setWho] = useState<PersonCard | null>(person ?? null);
  const [toUnit, setToUnit] = useState<number>(0);
  const [parentId, setParentId] = useState<number>(unit?.id && kind === "unit_create" ? unit.id : 0);
  const [rank, setRank] = useState<number>(0);
  const [name, setName] = useState(kind === "unit_rename" ? (unit?.name ?? "") : "");
  const [code, setCode] = useState("");
  const [unitKind, setUnitKind] = useState<OrgKind>("team");
  const [sort, setSort] = useState(0);
  const [email, setEmail] = useState("");
  const [employeeNo, setEmployeeNo] = useState("");
  const [role, setRole] = useState("staff");
  const [phone, setPhone] = useState("");
  const [extension, setExtension] = useState("");
  const [lastDay, setLastDay] = useState(today);
  const [leaveKind, setLeaveKind] = useState("자진");
  const [endDay, setEndDay] = useState(today);
  const [absenceKind, setAbsenceKind] = useState("");
  const [returnDay, setReturnDay] = useState("");
  const [absenceReason, setAbsenceReason] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [previewKey, setPreviewKey] = useState("");
  const [error, setError] = useState("");
  const [confirmMsg, setConfirmMsg] = useState("");
  const [busy, setBusy] = useState(false);

  const units = useMemo(() => flatten(tree), [tree]);
  const openUnits = units.filter((u) => !u.node.closed);
  const effectiveDate = kind === "leave" ? lastDay : effective;

  function payload(confirm = false) {
    const p: Record<string, unknown> = { kind, effectiveDate: kind === "leave" ? "" : effective, reason, confirm };
    if (kind.startsWith("unit_") || kind === "head_set" || kind === "head_clear" || kind === "acting_set") {
      if (kind !== "unit_create") p.unitId = unit?.id ?? 0;
    }
    if (kind === "unit_create") Object.assign(p, { code, name, unitKind, parentId, sort });
    if (kind === "unit_rename") p.name = name;
    if (kind === "unit_move") p.parentId = parentId;
    if (kind === "unit_sort") p.sort = sort;
    if (NEEDS_PERSON.has(kind)) p.userId = who?.id ?? 0;
    if (NEEDS_TO_UNIT.has(kind)) p.toUnit = toUnit;
    if (NEEDS_RANK.has(kind)) p.rank = rank;
    if (kind === "hire") Object.assign(p, { name, email, employeeNo, role, phone, extension });
    if (kind === "leave") Object.assign(p, { lastDay, leaveKind });
    if (kind === "acting_set") p.endDay = endDay;
    if (kind === "absence_start") Object.assign(p, { absenceKind, returnDay, absenceReason });
    return p;
  }

  const key = JSON.stringify(payload());
  const previewed = preview !== null && previewKey === key;

  async function runPreview(e: FormEvent) {
    e.preventDefault();
    setError("");
    setConfirmMsg("");
    setBusy(true);
    try {
      const r = await api.post<Preview>("/api/org/events/preview", payload(true));
      setPreview(r);
      setPreviewKey(key);
    } catch (err) {
      const m = readError(err, "미리보기를 만들지 못했습니다. 입력한 내용을 확인하세요.");
      setPreview(null);
      setError(typeof m === "string" ? m : m.message);
    } finally {
      setBusy(false);
    }
  }

  async function register(confirm: boolean) {
    setError("");
    setBusy(true);
    try {
      const url = correcting ? `/api/org/events/${correcting.id}/correct` : "/api/org/events";
      const r = await api.post<{ effectiveDate: string }>(url, payload(confirm));
      onDone(
        effectiveDate > today || r.effectiveDate > today
          ? "예약 발령으로 등록했습니다."
          : `발령을 등록했습니다(효력일 ${dayText(r.effectiveDate)}).`,
      );
    } catch (err) {
      const m = readError(err, "저장하지 못했습니다. 입력한 내용을 확인하세요.");
      if (typeof m !== "string") setConfirmMsg(m.message);
      else setError(m);
    } finally {
      setBusy(false);
    }
  }

  const title = KIND_FORM[kind] ?? kind;
  const unitOptions = (list: typeof units) =>
    list.map(({ node, depth }) => (
      <option key={node.id} value={node.id}>
        {"  ".repeat(depth)}
        {node.name} ({KIND_LABEL[node.kind]})
      </option>
    ));

  return (
    <form onSubmit={runPreview} aria-label={title} className="card">
      <h3>{correcting ? `${title} 정정(원본 #${correcting.id})` : title}</h3>
      {unit && kind !== "unit_create" && (
        <p className="hint">
          대상 조직: <strong>{unit.name}</strong>
        </p>
      )}
      {kind === "unit_create" && (
        <>
          <div className="form-row">
            <label>
              조직 코드 <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="예: PU-3" />
            </label>
            <label>
              조직 이름 <input value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <label>
              종류
              <select value={unitKind} onChange={(e) => setUnitKind(e.target.value as OrgKind)}>
                {(["division", "department", "team"] as OrgKind[]).map((k) => (
                  <option key={k} value={k}>
                    {KIND_LABEL[k]}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <div className="field">
            <label htmlFor="cf-parent">상위 조직</label>
            <select id="cf-parent" value={parentId} onChange={(e) => setParentId(Number(e.target.value))}>
              <option value={0}>선택하세요</option>
              {unitOptions(openUnits)}
            </select>
          </div>
        </>
      )}
      {kind === "unit_rename" && (
        <div className="field">
          <label htmlFor="cf-name">새 이름</label>
          <input id="cf-name" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
      )}
      {kind === "unit_move" && (
        <div className="field">
          <label htmlFor="cf-parent">새 상위 조직</label>
          <select id="cf-parent" value={parentId} onChange={(e) => setParentId(Number(e.target.value))}>
            <option value={0}>선택하세요</option>
            {unitOptions(openUnits.filter((u) => u.node.id !== unit?.id))}
          </select>
        </div>
      )}
      {kind === "unit_sort" && (
        <div className="field">
          <label htmlFor="cf-sort">정렬 순서(작을수록 위)</label>
          <input id="cf-sort" type="number" value={sort} onChange={(e) => setSort(Number(e.target.value))} />
        </div>
      )}
      {kind === "hire" && (
        <>
          <div className="form-row">
            <label>
              이름 <input value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <label>
              이메일 <input value={email} onChange={(e) => setEmail(e.target.value)} />
            </label>
            <label>
              사번(비우면 자동) <input value={employeeNo} onChange={(e) => setEmployeeNo(e.target.value)} />
            </label>
          </div>
          <div className="form-row">
            <label>
              권한 역할
              <select value={role} onChange={(e) => setRole(e.target.value)}>
                {ROLE_OPTIONS.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </select>
            </label>
            <label>
              휴대전화 <input value={phone} onChange={(e) => setPhone(e.target.value)} />
            </label>
            <label>
              내선 <input value={extension} onChange={(e) => setExtension(e.target.value)} />
            </label>
          </div>
        </>
      )}
      {NEEDS_PERSON.has(kind) && (
        <PersonPicker
          label={kind === "acting_set" ? "대행할 사람" : "대상 사람"}
          value={who}
          onChange={setWho}
          unitId={kind === "head_set" ? unit?.id : undefined}
        />
      )}
      {NEEDS_TO_UNIT.has(kind) && (
        <div className="field">
          <label htmlFor="cf-to">{kind === "hire" ? "소속 조직" : kind === "concurrent_end" ? "해제할 겸직 조직" : "조직"}</label>
          <select id="cf-to" value={toUnit} onChange={(e) => setToUnit(Number(e.target.value))}>
            <option value={0}>선택하세요</option>
            {unitOptions(kind === "concurrent_end" ? units : openUnits)}
          </select>
        </div>
      )}
      {NEEDS_RANK.has(kind) && (
        <div className="field">
          <label htmlFor="cf-rank">직위</label>
          <select id="cf-rank" value={rank} onChange={(e) => setRank(Number(e.target.value))}>
            <option value={0}>선택하세요</option>
            {ranks
              .filter((r) => r.active)
              .map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
          </select>
        </div>
      )}
      {kind === "leave" && (
        <div className="form-row">
          <label>
            퇴사일(마지막 근무일) <input type="date" value={lastDay} onChange={(e) => setLastDay(e.target.value)} />
          </label>
          <label>
            퇴사 구분
            <select value={leaveKind} onChange={(e) => setLeaveKind(e.target.value)}>
              {LEAVE_KINDS.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </label>
        </div>
      )}
      {kind === "absence_start" && (
        <>
          <div className="form-row">
            <label>
              휴직 구분
              <select value={absenceKind} onChange={(e) => setAbsenceKind(e.target.value)}>
                <option value="">선택하세요</option>
                {ABSENCE_KINDS.map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
              </select>
            </label>
            <label>
              예정 복직일(선택) <input type="date" value={returnDay} onChange={(e) => setReturnDay(e.target.value)} />
            </label>
          </div>
          <div className="field">
            <label htmlFor="cf-absence-reason">휴직 사유(선택, 민감 정보)</label>
            <input
              id="cf-absence-reason"
              value={absenceReason}
              maxLength={200}
              onChange={(e) => setAbsenceReason(e.target.value)}
              aria-describedby="cf-absence-reason-hint"
            />
            <p id="cf-absence-reason-hint" className="hint">
              병명·진단 내용은 적지 마세요. 본인·직속 조직장·인사 담당만 볼 수 있고, 본인이 아닌 사람이 보면 열람 기록이 남습니다.
            </p>
          </div>
          <p className="hint">휴직 중에도 로그인은 되지만 결재자·대결자로는 지정되지 않습니다. 휴직 근로자의 연차 출근 간주 등 법정 기준은 운영 전 노무 검토가 필요합니다.</p>
        </>
      )}
      <div className="form-row">
        {kind !== "leave" && (
          <label>
            {kind === "acting_set" ? "대행 시작일" : kind === "absence_start" ? "휴직 시작일" : kind === "absence_end" ? "복직일" : "효력일"}{" "}
            <input type="date" value={effective} onChange={(e) => setEffective(e.target.value)} />
          </label>
        )}
        {kind === "acting_set" && (
          <label>
            대행 종료일(포함) <input type="date" value={endDay} onChange={(e) => setEndDay(e.target.value)} />
          </label>
        )}
        <label>
          사유 <input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="과거 날짜·정정·취소에는 필수" />
        </label>
      </div>
      {error && <Notice tone="error">{error}</Notice>}
      {confirmMsg && (
        <Notice tone="warn">
          {confirmMsg}{" "}
          <button type="button" className="btn-sm" onClick={() => void register(true)}>
            계속
          </button>
        </Notice>
      )}
      {preview && previewed && (
        <div className="notice notice-info" role="status">
          <strong>등록하면 이렇게 바뀝니다</strong>
          <ul>
            {preview.effects.map((e) => (
              <li key={e}>{e}</li>
            ))}
            {preview.warnings.map((w) => (
              <li key={w}>{w}</li>
            ))}
            {preview.pendingApprovals > 0 && (
              <li>
                이 사람이 결재(합의)할 차례인 진행 중 건이 {preview.pendingApprovals}건 있습니다. 반영 뒤에도 상신 당시의 결재선은 그대로
                유지되지만, {kind === "absence_start" ? "휴직 중인 사람의" : "퇴사한 사람의"} 단계는 "결재·합의자가 사라진 진행 중 건"에 나타납니다.
                {kind === "absence_start" && " 그 화면에서 '장기 부재' 원인으로 결재자를 바꾸세요."}
                {(preview.pendingDocs ?? []).length > 0 && (
                  <ul aria-label="진행 중 건 목록">
                    {(preview.pendingDocs ?? []).map((d) => (
                      <li key={d.id}>
                        {d.title}{" "}
                        <span className="hint">
                          {d.no} ({d.requester})
                        </span>
                      </li>
                    ))}
                    {preview.pendingApprovals > (preview.pendingDocs ?? []).length && (
                      <li className="hint">외 {preview.pendingApprovals - (preview.pendingDocs ?? []).length}건</li>
                    )}
                  </ul>
                )}
              </li>
            )}
          </ul>
        </div>
      )}
      <div className="form-row">
        <button type="submit" className="btn-secondary" disabled={busy}>
          미리보기
        </button>
        <button type="button" disabled={!previewed || busy} onClick={() => void register(false)}>
          등록
        </button>
        {onCancel && (
          <button type="button" className="btn-secondary" onClick={onCancel}>
            닫기
          </button>
        )}
      </div>
    </form>
  );
}
