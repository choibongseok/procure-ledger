//(B49). 배부 규칙 세트(버전)와 배부 미리보기 화면.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ApiError, api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { EmptyState, Notice, TableWrap } from "../layout/ui";
import { businessMessage } from "../purchase/useLoad";
import { PURPOSE_LABELS, purposeLabel, won } from "./AiCostPages";
import { RunPanel } from "./AiCostRunPanel";

export interface Selector {
  purposes?: string[];
  providers?: string[];
  unassignedDept?: boolean;
}
export interface PoolTarget {
  departmentId: number;
  weightPct?: number;
}
export interface Pool {
  name: string;
  selector: Selector;
  driver: "headcount" | "usage" | "fixed";
  scope?: "entity" | "group";
  targetsAll?: boolean;
  targets?: PoolTarget[];
}
export interface RuleSet {
  version?: number;
  validFromMonth: string;
  note: string;
  createdByName?: string;
  createdAt?: string;
  pools: Pool[];
}
export interface RuleSummary {
  version: number;
  validFromMonth: string;
  note: string;
  createdByName: string;
  createdAt: string;
  poolCount: number;
}
export interface DeptOption {
  id: number;
  name: string;
  entityId: number;
}
export interface PoolShare {
  departmentId: number;
  name: string;
  weight: string;
  shareKrw: number;
}
export interface EntityOption {
  id: number;
  name: string;
}
export interface PoolResult {
  seq: number;
  name: string;
  driver: string;
  group?: boolean;
  amountKrw: number;
  fallbackUsed?: boolean;
  shares: PoolShare[];
}
export interface PreviewLine {
  departmentId: number;
  departmentName: string;
  headcount: number;
  calls: number;
  directKrw: number;
  sharedKrw: number;
  totalKrw: number;
  deltaKrw?: number;
}
export interface PreviewResult {
  month: string;
  entityId: number;
  ruleVersion: number;
  compareVersion?: number;
  fxUsdKrwX1000: number;
  totalKrw: number;
  directKrw: number;
  sharedKrw: number;
  groupShareKrw?: number;
  counterpartyEntityId?: number;
  lines: PreviewLine[];
  pools: PoolResult[];
  adjusted: { provider: string; actualKrw: number }[];
  blockers: string[];
  warnings: string[];
}

export const DRIVER_LABELS: Record<string, string> = { headcount: "인원 비율", usage: "사용량 비율", fixed: "고정 비율" };

function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    try {
      const j = JSON.parse(e.message) as { error?: string };
      if (j.error) return businessMessage(j.error);
    } catch {
      /* 그대로 */
    }
    return businessMessage(e.message);
  }
  return "요청을 처리하지 못했습니다.";
}

function thisMonth(now: Date = new Date()): string {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
}

function blankPool(): Pool {
  return { name: "", selector: {}, driver: "headcount", targetsAll: true, targets: [] };
}

export function describeSelector(s: Selector): string {
  const parts: string[] = [];
  if (s.purposes?.length) parts.push(`용도: ${s.purposes.map(purposeLabel).join("·")}`);
  if (s.providers?.length) parts.push(`공급자: ${s.providers.join("·")}`);
  if (s.unassignedDept) parts.push("부서 미지정 호출");
  return parts.join(" / ") || "조건 없음";
}

function PoolEditor({ pool, depts, entities, onChange, onRemove, disabled }: { pool: Pool; depts: DeptOption[]; entities: EntityOption[]; onChange: (p: Pool) => void; onRemove: () => void; disabled: boolean }) {
  const purposes = pool.selector.purposes ?? [];
  const targets = pool.targets ?? [];
  const group = pool.scope === "group";
  const multi = entities.length > 1;
  const choices: { id: number; name: string }[] = group ? entities : depts;
  const toggleP = (k: string) => {
    const next = purposes.includes(k) ? purposes.filter((x) => x !== k) : [...purposes, k];
    onChange({ ...pool, selector: { ...pool.selector, purposes: next } });
  };
  const target = (id: number) => targets.find((t) => t.departmentId === id);
  const toggleT = (id: number) =>
    onChange({ ...pool, targets: target(id) ? targets.filter((t) => t.departmentId !== id) : [...targets, { departmentId: id, weightPct: pool.driver === "fixed" ? 0 : undefined }] });
  const setPct = (id: number, v: string) => onChange({ ...pool, targets: targets.map((t) => (t.departmentId === id ? { ...t, weightPct: Number(v) || 0 } : t)) });
  return (
    <fieldset disabled={disabled} aria-label={`풀 ${pool.name || "이름 없음"}`}>
      <legend>{pool.name || "새 풀"}</legend>
      <label>
        풀 이름 <input value={pool.name} maxLength={40} onChange={(e) => onChange({ ...pool, name: e.target.value })} />
      </label>
      <div role="group" aria-label="풀에 넣을 호출">
        <span>풀에 넣을 호출: </span>
        {Object.entries(PURPOSE_LABELS).map(([k, v]) => (
          <label key={k} style={{ marginRight: 12 }}>
            <input type="checkbox" checked={purposes.includes(k)} onChange={() => toggleP(k)} /> {v}
          </label>
        ))}
        <label>
          <input type="checkbox" checked={!!pool.selector.unassignedDept} onChange={(e) => onChange({ ...pool, selector: { ...pool.selector, unassignedDept: e.target.checked } })} /> 부서 미지정 호출
        </label>
      </div>
      <label>
        공급자(쉼표로 구분, 비우면 전체){" "}
        <input
          value={(pool.selector.providers ?? []).join(",")}
          onChange={(e) => onChange({ ...pool, selector: { ...pool.selector, providers: e.target.value.split(",").map((x) => x.trim()).filter(Boolean) } })}
        />
      </label>
      {(multi || group) && (
        <label>
          나누는 범위{" "}
          <select
            value={pool.scope ?? "entity"}
            onChange={(e) => onChange({ ...pool, scope: e.target.value as Pool["scope"], targets: [], targetsAll: pool.driver !== "fixed" })}
          >
            <option value="entity">법인 안(부서에 나눔)</option>
            <option value="group">법인 간 공용(법인에 먼저 나눔)</option>
          </select>
        </label>
      )}
      <label>
        나누는 기준{" "}
        <select value={pool.driver} onChange={(e) => onChange({ ...pool, driver: e.target.value as Pool["driver"], targetsAll: e.target.value === "fixed" ? false : group ? true : pool.targetsAll, targets: [] })}>
          {Object.entries(DRIVER_LABELS).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
      </label>
      {pool.driver !== "fixed" && !group && (
        <label>
          <input type="checkbox" checked={!!pool.targetsAll} onChange={(e) => onChange({ ...pool, targetsAll: e.target.checked, targets: [] })} /> 전체 부서에 나눔
        </label>
      )}
      {group && pool.driver !== "fixed" && <p>모든 법인에 {pool.driver === "usage" ? "직접 비용" : "월말 인원"} 비율로 나눈 뒤, 각 법인 안에서 부서에 같은 기준으로 나눕니다.</p>}
      {!pool.targetsAll && (
        <div role="group" aria-label={group ? "대상 법인" : "대상 부서"}>
          {choices.map((d) => (
            <div key={d.id}>
              <label>
                <input type="checkbox" checked={!!target(d.id)} onChange={() => toggleT(d.id)} /> {d.name}
              </label>
              {pool.driver === "fixed" && target(d.id) && (
                <label style={{ marginLeft: 8 }}>
                  비율(%) <input inputMode="numeric" style={{ width: 64 }} value={target(d.id)?.weightPct ?? 0} onChange={(e) => setPct(d.id, e.target.value)} />
                </label>
              )}
            </div>
          ))}
        </div>
      )}
      <button type="button" onClick={onRemove}>
        풀 삭제
      </button>
    </fieldset>
  );
}

/** 비용 전표의 차변 계정 설정(재무만 바꾼다). */
function SettingsPanel() {
  const { can } = useAuth();
  const allocate = can("cost:allocate");
  const [st, setSt] = useState<{ expenseAccount: string; options: { code: string; name: string }[] } | null>(null);
  const [pick, setPick] = useState("");
  const [msg, setMsg] = useState("");
  const [err, setErr] = useState("");
  const load = useCallback(async () => {
    try {
      const r = await api.get<{ expenseAccount: string; options: { code: string; name: string }[] }>("/api/admin/ai-cost/settings");
      setSt(r);
      setPick(r.expenseAccount);
    } catch (e) {
      setErr(errorText(e));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  async function save(e: FormEvent) {
    e.preventDefault();
    setMsg("");
    setErr("");
    try {
      await api.put("/api/admin/ai-cost/settings", { expenseAccount: pick });
      setMsg("비용 계정을 바꿨습니다. 이미 만든 전표는 그대로이고 다음 확정부터 적용됩니다.");
      await load();
    } catch (e2) {
      setErr(errorText(e2));
    }
  }
  if (!st && !err) return null;
  const name = st?.options.find((o) => o.code === st.expenseAccount)?.name ?? "";
  return (
    <section aria-label="비용 계정">
      <h2>비용 전표의 비용 계정</h2>
      {err && <Notice tone="error">{err}</Notice>}
      {msg && <Notice tone="ok">{msg}</Notice>}
      {st &&
        (allocate ? (
          <form aria-label="비용 계정 설정" onSubmit={save} className="form-row">
            <label>
              월 배부 확정이 만드는 전표의 차변 계정{" "}
              <select value={pick} onChange={(e) => setPick(e.target.value)}>
                {st.options.map((o) => (
                  <option key={o.code} value={o.code}>{`${o.code} ${o.name}`}</option>
                ))}
              </select>
            </label>
            <button type="submit" disabled={pick === st.expenseAccount}>
              계정 저장
            </button>
          </form>
        ) : (
          <p>
            차변 계정: {st.expenseAccount} {name}
          </p>
        ))}
    </section>
  );
}

export function AiCostRulesPage() {
  const { can } = useAuth();
  const allocate = can("cost:allocate");
  const [versions, setVersions] = useState<RuleSummary[] | null>(null);
  const [depts, setDepts] = useState<DeptOption[]>([]);
  const [entities, setEntities] = useState<EntityOption[]>([]);
  const [draft, setDraft] = useState<RuleSet>({ validFromMonth: thisMonth(), note: "", pools: [] });
  const [error, setError] = useState("");
  const [msg, setMsg] = useState("");
  const [warns, setWarns] = useState<string[]>([]);

  const load = useCallback(async () => {
    setError("");
    try {
      const [v, d] = await Promise.all([api.get<{ versions: RuleSummary[] }>("/api/admin/ai-cost/rules"), api.get<{ departments: DeptOption[]; entities?: EntityOption[] }>("/api/admin/ai-cost/departments")]);
      setVersions(v.versions);
      setDepts(d.departments);
      setEntities(d.entities ?? []);
    } catch (e) {
      setError(errorText(e));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  async function useAsBase(version: number) {
    setError("");
    try {
      const r = await api.get<RuleSet>(`/api/admin/ai-cost/rules/${version}`);
      setDraft({ validFromMonth: thisMonth(), note: "", pools: r.pools });
      setMsg(`버전 ${version} 을 바탕으로 편집합니다. 저장하면 새 버전이 만들어집니다.`);
    } catch (e) {
      setError(errorText(e));
    }
  }

  async function makeDefault() {
    setError("");
    setMsg("");
    try {
      await api.post("/api/admin/ai-cost/rules/default");
      setMsg("기본 규칙을 만들었습니다.");
      await load();
    } catch (e) {
      setError(errorText(e));
    }
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setError("");
    setMsg("");
    try {
      const r = await api.post<{ version: number; warnings: string[] }>("/api/admin/ai-cost/rules", draft);
      setMsg(`버전 ${r.version} 으로 저장했습니다.`);
      setWarns(r.warnings ?? []);
      await load();
    } catch (err) {
      setError(errorText(err));
    }
  }

  return (
    <div>
      <h1>AI 비용 배부 규칙</h1>
      <p>
        부서가 없는 호출이나 평가 실행처럼 특정 부서의 일이 아닌 비용(공용 비용)을 어떤 기준으로 나눌지 정합니다. 풀에 넣지 않은 호출은 쓴 부서의 직접 비용입니다. 저장한 버전은 고칠 수 없고, 고치려면 새 버전으로 저장합니다.{" "}
        <Link to="/admin/ai-cost">집계로 돌아가기</Link> · <Link to="/admin/ai-cost/preview">배부 미리보기</Link>
      </p>
      <SettingsPanel />
      {error && <Notice tone="error">{error}</Notice>}
      {msg && <Notice tone="ok">{msg}</Notice>}
      {warns.map((w) => (
        <Notice key={w} tone="warn">
          {w}
        </Notice>
      ))}
      {versions === null ? (
        <p>불러오는 중…</p>
      ) : versions.length === 0 ? (
        <EmptyState
          hint="평가 실행은 쓴 부서의 사용량 비율로, 부서 없는 호출은 부서 인원 비율로 나누는 규칙입니다."
          actions={
            allocate ? (
              <button type="button" onClick={() => void makeDefault()}>
                기본 규칙 만들기
              </button>
            ) : undefined
          }
        >
          배부 규칙이 아직 없습니다. 기본 규칙으로 시작할 수 있습니다.
        </EmptyState>
      ) : (
        <TableWrap>
          <table aria-label="규칙 버전">
            <thead>
              <tr>
                <th className="num nowrap">버전</th>
                <th className="nowrap">적용 시작 월</th>
                <th className="num nowrap">풀</th>
                <th>메모</th>
                <th>만든 사람</th>
                <th aria-label="동작" />
              </tr>
            </thead>
            <tbody>
              {versions.map((v) => (
                <tr key={v.version}>
                  <td className="num">{v.version}</td>
                  <td className="nowrap">{v.validFromMonth}</td>
                  <td className="num">{v.poolCount}</td>
                  <td>{v.note || "-"}</td>
                  <td className="nowrap">{v.createdByName || "-"}</td>
                  <td>
                    <button type="button" onClick={() => void useAsBase(v.version)}>
                      {allocate ? `버전 ${v.version} 을 바탕으로 편집` : `버전 ${v.version} 내용 보기`}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {(allocate || draft.pools.length > 0) && (
        <form aria-label="규칙 편집" onSubmit={save}>
          <h2>{allocate ? "새 버전 만들기" : "선택한 버전 내용"}</h2>
          <label>
            적용 시작 월 <input type="month" value={draft.validFromMonth} disabled={!allocate} onChange={(e) => setDraft({ ...draft, validFromMonth: e.target.value })} required />
          </label>
          <label>
            메모 <input value={draft.note} maxLength={200} disabled={!allocate} onChange={(e) => setDraft({ ...draft, note: e.target.value })} />
          </label>
          {draft.pools.length === 0 && <p>풀이 없습니다. 풀을 더하세요.</p>}
          {draft.pools.map((p, i) => (
            <PoolEditor
              key={i}
              pool={p}
              depts={depts}
              entities={entities}
              disabled={!allocate}
              onChange={(np) => setDraft({ ...draft, pools: draft.pools.map((x, j) => (j === i ? np : x)) })}
              onRemove={() => setDraft({ ...draft, pools: draft.pools.filter((_, j) => j !== i) })}
            />
          ))}
          {!allocate && draft.pools.length > 0 && (
            <ul>
              {draft.pools.map((p, i) => (
                <li key={i}>
                  {p.name}: {describeSelector(p.selector)} → {DRIVER_LABELS[p.driver]}
                </li>
              ))}
            </ul>
          )}
          {allocate && (
            <div>
              <button type="button" onClick={() => setDraft({ ...draft, pools: [...draft.pools, blankPool()] })}>
                풀 더하기
              </button>{" "}
              <button type="submit">새 버전으로 저장</button>
            </div>
          )}
        </form>
      )}
    </div>
  );
}

export function AiCostPreviewPage() {
  const [month, setMonth] = useState(thisMonth());
  const [version, setVersion] = useState("");
  const [compare, setCompare] = useState("");
  const [entity, setEntity] = useState("");
  const [versions, setVersions] = useState<RuleSummary[]>([]);
  const [entities, setEntities] = useState<number[]>([]);
  const [data, setData] = useState<PreviewResult | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    void (async () => {
      try {
        const [v, d] = await Promise.all([api.get<{ versions: RuleSummary[] }>("/api/admin/ai-cost/rules"), api.get<{ departments: DeptOption[] }>("/api/admin/ai-cost/departments")]);
        setVersions(v.versions);
        setEntities([...new Set(d.departments.map((x) => x.entityId))].sort((a, b) => a - b));
      } catch (e) {
        setError(errorText(e));
      }
    })();
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    const q = new URLSearchParams({ month });
    if (version) q.set("version", version);
    if (compare) q.set("compare", compare);
    if (entity) q.set("entity", entity);
    try {
      setData(await api.get<PreviewResult>(`/api/admin/ai-cost/preview?${q.toString()}`));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  }, [month, version, compare, entity]);
  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <h1>AI 비용 배부 미리보기</h1>
      <p>
        규칙을 적용했을 때 부서별로 얼마씩 부담하는지 미리 계산합니다. 저장하지 않으며 열 때마다 다시 계산합니다. <Link to="/admin/ai-cost">집계</Link> · <Link to="/admin/ai-cost/rules">배부 규칙</Link>
      </p>
      <form aria-label="미리보기 조건" className="form-row" onSubmit={(e) => { e.preventDefault(); void load(); }}>
        <label>
          월 <input type="month" value={month} onChange={(e) => setMonth(e.target.value)} required />
        </label>
        {entities.length > 1 && (
          <label>
            법인{" "}
            <select value={entity} onChange={(e) => setEntity(e.target.value)}>
              <option value="">현재 법인</option>
              {entities.map((id) => (
                <option key={id} value={id}>{`법인 ${id}`}</option>
              ))}
            </select>
          </label>
        )}
        <label>
          규칙 버전{" "}
          <select value={version} onChange={(e) => setVersion(e.target.value)}>
            <option value="">그 달에 적용되는 버전</option>
            {versions.map((v) => (
              <option key={v.version} value={v.version}>{`버전 ${v.version} (${v.validFromMonth}부터)`}</option>
            ))}
          </select>
        </label>
        <label>
          비교할 버전{" "}
          <select value={compare} onChange={(e) => setCompare(e.target.value)}>
            <option value="">비교 안 함</option>
            {versions.map((v) => (
              <option key={v.version} value={v.version}>{`버전 ${v.version}`}</option>
            ))}
          </select>
        </label>
      </form>
      {error && <Notice tone="error">{error}</Notice>}
      {loading && !data && <p>불러오는 중…</p>}
      {data && (
        <>
          <RunPanel month={month} entity={entity} ruleVersion={version} canConfirm={data.blockers.length === 0 && data.lines.length > 0 && !data.compareVersion} onChanged={() => void load()} />
          {data.blockers.map((b) => (
            <Notice key={b} tone="error">
              {b}
            </Notice>
          ))}
          {data.warnings.map((w) => (
            <Notice key={w} tone="warn">
              {w}
            </Notice>
          ))}
          {data.lines.length === 0 && data.blockers.length === 0 ? (
            <EmptyState>이 달에는 AI 사용 기록이 없습니다.</EmptyState>
          ) : (
            data.lines.length > 0 && (
              <>
                <p>
                  {data.month} · 규칙 버전 {data.ruleVersion} · 합계 <strong>{won(data.totalKrw)}</strong> (직접 {won(data.directKrw)} + 공용 {won(data.sharedKrw)})
                  {data.adjusted.length > 0 && <> · 실제 청구액으로 보정한 공급자: {data.adjusted.map((a) => a.provider).join(", ")}</>}
                </p>
                {(data.groupShareKrw ?? 0) > 0 && (
                  <Notice tone="info">
                    법인 간 공용 풀에서 이 법인이 받은 몫: {won(data.groupShareKrw ?? 0)}.
                    {data.counterpartyEntityId ? ` 기본 법인이 대신 지급한 공용 비용이 들어 있어 법인 간 거래로 표시됩니다(표시만 하며 상계·연결 조정은 하지 않습니다).` : ""}
                  </Notice>
                )}
                <TableWrap>
                  <table aria-label="부서별 배부 결과">
                    <thead>
                      <tr>
                        <th>부서</th>
                        <th className="num nowrap">월말 인원</th>
                        <th className="num nowrap">호출</th>
                        <th className="num nowrap">직접 비용</th>
                        <th className="num nowrap">공용 비용 몫</th>
                        <th className="num nowrap">부담액</th>
                        {data.compareVersion ? <th className="num nowrap">{`버전 ${data.compareVersion} 대비`}</th> : null}
                      </tr>
                    </thead>
                    <tbody>
                      {data.lines.map((l) => (
                        <tr key={l.departmentId}>
                          <td>{l.departmentName}</td>
                          <td className="num">{l.headcount.toLocaleString("ko-KR")}</td>
                          <td className="num">{l.calls.toLocaleString("ko-KR")}</td>
                          <td className="num">{won(l.directKrw)}</td>
                          <td className="num">{won(l.sharedKrw)}</td>
                          <td className="num">{won(l.totalKrw)}</td>
                          {data.compareVersion ? <td className="num">{l.deltaKrw === undefined ? "-" : `${l.deltaKrw > 0 ? "+" : ""}${l.deltaKrw.toLocaleString("ko-KR")}원`}</td> : null}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </TableWrap>
                {data.pools.length > 0 && (
                  <>
                    <h2>풀별 나눔</h2>
                    <TableWrap>
                      <table aria-label="풀별 나눔">
                        <thead>
                          <tr>
                            <th>풀</th>
                            <th>기준</th>
                            <th className="num nowrap">풀 금액</th>
                            <th>부서별 몫</th>
                          </tr>
                        </thead>
                        <tbody>
                          {data.pools.map((p) => (
                            <tr key={p.seq}>
                              <td>
                                {p.name}
                                {p.group ? " (법인 간 공용 몫)" : ""}
                              </td>
                              <td>{DRIVER_LABELS[p.driver] ?? "기준"}{p.fallbackUsed ? " (인원 비율로 대체)" : ""}</td>
                              <td className="num">{won(p.amountKrw)}</td>
                              <td>{p.shares.map((s) => `${s.name} ${won(s.shareKrw)}(${s.weight})`).join(", ")}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </TableWrap>
                  </>
                )}
              </>
            )
          )}
        </>
      )}
    </div>
  );
}
