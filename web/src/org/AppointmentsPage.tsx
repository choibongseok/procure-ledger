// (web/src/org). 인사 발령(admin): 등록(미리보기 뒤 등록), 목록, 취소·정정.
import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { ApiError, api } from "../api/client";
import { Badge, EmptyState, Notice, Pagination, TableWrap, useToast } from "../layout/ui";
import { CommandForm, KIND_FORM } from "./CommandForm";
import { KIND_LABEL, STATUS_LABEL, dayText, flatten, readError, type EventRow, type PersonCard, type Rank, type TreeNode } from "./types";

const PERSON_KINDS = [
  "hire",
  "transfer",
  "rank_change",
  "concurrent_add",
  "concurrent_end",
  "leave",
  "absence_start",
  "absence_end",
  "acting_set",
  "head_set",
  "head_clear",
];
const PAGE_SIZE = 30;
const NEEDS_UNIT_PICK = new Set(["acting_set", "head_set", "head_clear"]);

export function AppointmentsPage() {
  const [params] = useSearchParams();
  const [root, setRoot] = useState<TreeNode | null>(null);
  const [ranks, setRanks] = useState<Rank[]>([]);
  const [events, setEvents] = useState<EventRow[] | null>(null);
  const [kind, setKind] = useState("");
  const [unitId, setUnitId] = useState(0);
  const [person, setPerson] = useState<PersonCard | null>(null);
  const [status, setStatus] = useState("");
  const [q, setQ] = useState("");
  const [kindFilter, setKindFilter] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [offset, setOffset] = useState(0);
  const [total, setTotal] = useState(0);
  const toast = useToast();
  const [error, setError] = useState("");
  const [cancelFor, setCancelFor] = useState<EventRow | null>(null);
  const [cancelReason, setCancelReason] = useState("");
  const [cancelConfirm, setCancelConfirm] = useState("");
  const [reasonView, setReasonView] = useState<{ id: number; name: string; text: string } | null>(null);
  const [correct, setCorrect] = useState<{ ev: EventRow; person: PersonCard | null; unit: TreeNode | null } | null>(null);
  const [formKey, setFormKey] = useState(0);

  const load = useCallback(async () => {
    try {
      const p = new URLSearchParams();
      if (status) p.set("status", status);
      if (q) p.set("q", q);
      if (kindFilter) p.set("kind", kindFilter);
      if (from) p.set("from", from);
      if (to) p.set("to", to);
      p.set("limit", String(PAGE_SIZE));
      p.set("offset", String(offset));
      const [t, r, e] = await Promise.all([
        api.get<{ root: TreeNode | null }>("/api/org/tree"),
        api.get<{ ranks: Rank[] }>("/api/org/ranks"),
        api.get<{ events: EventRow[]; total: number }>(`/api/org/events?${p}`),
      ]);
      setRoot(t.root);
      setRanks(r.ranks);
      setEvents(e.events);
      setTotal(e.total ?? e.events.length);
    } catch {
      setError("발령 목록을 불러오지 못했습니다. 잠시 후 다시 시도하세요.");
    }
  }, [status, q, kindFilter, from, to, offset]);
  useEffect(() => {
    void load();
  }, [load]);

  // 사람 카드에서 넘어온 경우(?user=번호): 그 사람을 미리 고른다.
  useEffect(() => {
    const id = params.get("user");
    if (!id) return;
    api
      .get<PersonCard>(`/api/org/people/${id}`)
      .then((c) => {
        setPerson(c);
        setKind("transfer");
      })
      .catch(() => setError("이 사람의 정보를 찾을 수 없습니다."));
  }, [params]);

  const flat = flatten(root);
  const pickedUnit = flat.find((u) => u.node.id === unitId)?.node ?? null;

  async function doCancel(confirm: boolean) {
    if (!cancelFor) return;
    setError("");
    try {
      await api.post(`/api/org/events/${cancelFor.id}/cancel`, { reason: cancelReason, confirm });
      toast("발령을 취소했습니다.");
      setCancelFor(null);
      setCancelReason("");
      setCancelConfirm("");
      void load();
    } catch (e) {
      const m = readError(e, "취소하지 못했습니다. 이후 발령과 맞지 않을 수 있습니다.");
      if (typeof m === "string") setError(m);
      else setCancelConfirm(m.message);
    }
  }

  // 휴직 사유 원문은 이 요청으로만 읽는다. 본인이 아닌 사람이 읽으면 서버가 열람 기록을 남기고, 기록하지 못하면 사유 없이 실패한다.
  async function showReason(ev: EventRow) {
    setError("");
    try {
      const r = await api.get<{ reason: string }>(`/api/org/events/${ev.id}/absence-reason`);
      setReasonView({ id: ev.id, name: ev.subjectName, text: r.reason });
    } catch (e) {
      setReasonView(null);
      if (e instanceof ApiError && e.status === 503) {
        setError(e.message); // 열람 기록을 남기지 못해 사유를 내지 않았다(서버 문구 그대로)
        return;
      }
      if (e instanceof ApiError && e.status === 404) {
        setError("이 휴직 사유를 볼 수 있는 범위가 아닙니다.");
        return;
      }
      const m = readError(e, "사유를 불러오지 못했습니다. 잠시 후 다시 시도하세요.");
      setError(typeof m === "string" ? m : m.message);
    }
  }

  async function startCorrect(ev: EventRow) {
    setError("");
    try {
      if (ev.subject === "person") {
        const c = await api.get<PersonCard>(`/api/org/people/${ev.subjectId}`);
        setCorrect({ ev, person: c, unit: null });
      } else {
        const unit = flat.find((u) => u.node.id === ev.subjectId)?.node ?? null;
        setCorrect({ ev, person: null, unit });
      }
    } catch {
      setError("정정할 대상을 불러오지 못했습니다.");
    }
  }

  return (
    <section>
      <h1>인사 발령</h1>
      {error && <Notice tone="error">{error}</Notice>}

      <h2>발령 등록</h2>
      <div className="form-row">
        <label>
          종류
          <select
            value={kind}
            onChange={(e) => {
              setKind(e.target.value);
              setCorrect(null);
              setFormKey((k) => k + 1);
            }}
          >
            <option value="">선택하세요</option>
            {PERSON_KINDS.map((k) => (
              <option key={k} value={k}>
                {KIND_FORM[k]}
              </option>
            ))}
          </select>
        </label>
        {NEEDS_UNIT_PICK.has(kind) && (
          <label>
            조직
            <select
              value={unitId}
              onChange={(e) => {
                setUnitId(Number(e.target.value));
                setFormKey((k) => k + 1);
              }}
            >
              <option value={0}>선택하세요</option>
              {flat
                .filter((u) => !u.node.closed)
                .map(({ node, depth }) => (
                  <option key={node.id} value={node.id}>
                    {"  ".repeat(depth)}
                    {node.name} ({KIND_LABEL[node.kind]})
                  </option>
                ))}
            </select>
          </label>
        )}
      </div>
      {kind && !correct && (!NEEDS_UNIT_PICK.has(kind) || pickedUnit) && (
        <CommandForm
          key={`${kind}-${unitId}-${formKey}-${person?.id ?? 0}`}
          kind={kind}
          tree={root}
          ranks={ranks}
          unit={NEEDS_UNIT_PICK.has(kind) ? pickedUnit : null}
          person={kind === "hire" ? null : person}
          onDone={(m) => {
            toast(m);
            setKind("");
            setPerson(null);
            void load();
          }}
          onCancel={() => setKind("")}
        />
      )}
      {correct && (
        <CommandForm
          key={`correct-${correct.ev.id}`}
          kind={correct.ev.kind}
          tree={root}
          ranks={ranks}
          unit={correct.unit}
          person={correct.person}
          correcting={correct.ev}
          onDone={(m) => {
            toast(m);
            setCorrect(null);
            void load();
          }}
          onCancel={() => setCorrect(null)}
        />
      )}

      <h2>발령 목록</h2>
      <div className="form-row">
        <label>
          상태
          <select
            value={status}
            onChange={(e) => {
              setStatus(e.target.value);
              setOffset(0);
            }}
          >
            <option value="">전체</option>
            <option value="scheduled">예약</option>
            <option value="applied">적용됨</option>
            <option value="cancelled">취소됨</option>
          </select>
        </label>
        <label>
          종류
          <select
            value={kindFilter}
            onChange={(e) => {
              setKindFilter(e.target.value);
              setOffset(0);
            }}
          >
            <option value="">전체</option>
            {Object.entries(KIND_FORM).map(([k, label]) => (
              <option key={k} value={k}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <label>
          효력일 시작
          <input
            type="date"
            value={from}
            onChange={(e) => {
              setFrom(e.target.value);
              setOffset(0);
            }}
          />
        </label>
        <label>
          효력일 끝
          <input
            type="date"
            value={to}
            onChange={(e) => {
              setTo(e.target.value);
              setOffset(0);
            }}
          />
        </label>
        <label>
          검색{" "}
          <input
            value={q}
            onChange={(e) => {
              setQ(e.target.value);
              setOffset(0);
            }}
            placeholder="사람·조직·종류"
          />
        </label>
      </div>
      {reasonView && (
        <div className="card" role="status" aria-label="휴직 사유">
          <p>
            <strong>{reasonView.name}</strong> 휴직 사유(열람 기록이 남습니다): {reasonView.text || "적힌 사유가 없습니다."}
          </p>
          <button type="button" className="btn-secondary btn-sm" onClick={() => setReasonView(null)}>
            닫기
          </button>
        </div>
      )}
      {cancelFor && (
        <div className="card">
          <p>
            <strong>
              {dayText(cancelFor.effectiveDate)} {cancelFor.kindLabel}
            </strong>{" "}
            ({cancelFor.subjectName}) 발령을 취소합니다.
          </p>
          <label>
            취소 사유 <input value={cancelReason} onChange={(e) => setCancelReason(e.target.value)} />
          </label>
          {cancelConfirm && (
            <Notice tone="warn">
              {cancelConfirm}{" "}
              <button type="button" className="btn-sm" onClick={() => void doCancel(true)}>
                계속
              </button>
            </Notice>
          )}
          <div className="form-row">
            <button type="button" className="btn-danger" onClick={() => void doCancel(false)}>
              취소하기
            </button>
            <button type="button" className="btn-secondary" onClick={() => setCancelFor(null)}>
              닫기
            </button>
          </div>
        </div>
      )}
      {events && events.length === 0 && (
        <EmptyState>
          {offset > 0 || status || q || kindFilter || from || to ? "조건에 맞는 발령이 없습니다." : "등록된 발령이 없습니다."}
        </EmptyState>
      )}
      {events && events.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th>효력일</th>
                <th>종류</th>
                <th>대상</th>
                <th>내용</th>
                <th>사유</th>
                <th>등록자</th>
                <th>상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {events.map((ev) => (
                <tr key={ev.id}>
                  <td data-label="효력일" className="nowrap">
                    {dayText(ev.effectiveDate)}
                  </td>
                  <td data-label="종류" className="nowrap">
                    {ev.kindLabel}
                    {ev.corrects > 0 && <span className="hint"> (정정, 원본 #{ev.corrects})</span>}
                  </td>
                  <td data-label="대상" className="nowrap">
                    {ev.subjectName}
                  </td>
                  <td data-label="내용">
                    {ev.summary}
                    {ev.hasAbsenceReason && (
                      <>
                        {" "}
                        <button type="button" className="btn-secondary btn-sm" onClick={() => void showReason(ev)}>
                          사유 보기
                        </button>
                      </>
                    )}
                  </td>
                  <td data-label="사유">{ev.reason || "-"}</td>
                  <td data-label="등록자" className="nowrap">
                    {ev.createdBy}
                  </td>
                  <td data-label="상태" className="nowrap">
                    <Badge tone={ev.status === "cancelled" ? "muted" : ev.status === "scheduled" ? "warn" : "ok"}>
                      {STATUS_LABEL[ev.status]}
                    </Badge>
                  </td>
                  <td className="nowrap">
                    {ev.status !== "cancelled" && (
                      <>
                        <button
                          type="button"
                          className="btn-secondary btn-sm"
                          onClick={() => {
                            setCancelFor(ev);
                            setCancelReason("");
                            setCancelConfirm("");
                          }}
                        >
                          취소
                        </button>{" "}
                        <button type="button" className="btn-secondary btn-sm" onClick={() => void startCorrect(ev)}>
                          정정
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {events && <Pagination offset={offset} limit={PAGE_SIZE} total={total} onChange={setOffset} />}
    </section>
  );
}
