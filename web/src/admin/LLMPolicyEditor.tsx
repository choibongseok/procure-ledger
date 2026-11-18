// 관리자 설정 화면의 모델 허용 목록·일일 한도 편집(정본 아트보드 w3-admin-settings).
// 서버 API 는 ai 의 LLM 정책 API(/api/llm/policies, /api/llm/quotas)를 그대로 쓴다. 변경은 서버가 감사 로그에 남긴다.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiError, api } from "../api/client";
import { roleName, useRoleOptions } from "./roleNames";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap } from "../layout/ui";
import { businessMessage } from "../purchase/useLoad";

interface Policy {
  id: number;
  role: string;
  departmentId: number;
  modelPattern: string;
  allowed: boolean;
}

interface Quota {
  id: number;
  scopeType: string;
  scopeValue: string;
  dailyTokenLimit: number;
}

const SCOPE_LABEL: Record<string, string> = { role: "역할", department: "부서", user: "사용자" };

const roleLabel = roleName;

/** 하루 한도 입력("1,000" 처럼 쉼표·공백이 든 숫자 포함)을 정수로 바꾼다. 숫자가 아니면 null. */
export function parseLimit(input: string): number | null {
  const t = input.replace(/[,\s]/g, "");
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return Number.isSafeInteger(n) ? n : null;
}

function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    try {
      const j = JSON.parse(e.message) as { error?: string };
      if (j.error) return businessMessage(j.error);
    } catch {
      /* 서버 문구 그대로 */
    }
    return e.message ? businessMessage(e.message) : "저장하지 못했습니다. 입력한 내용을 확인하세요.";
  }
  return "저장하지 못했습니다. 입력한 내용을 확인하세요.";
}

export function LLMPolicyEditor() {
  const { options: roleOptions } = useRoleOptions();
  const [policies, setPolicies] = useState<Policy[] | null>(null);
  const [quotas, setQuotas] = useState<Quota[] | null>(null);
  const [error, setError] = useState("");
  const [role, setRole] = useState("");
  const [pattern, setPattern] = useState("");
  const [allowed, setAllowed] = useState(true);
  const [scopeType, setScopeType] = useState("role");
  const [scopeValue, setScopeValue] = useState("");
  const [limit, setLimit] = useState("");
  const [removing, setRemoving] = useState<Policy | null>(null);
  const [zeroLimit, setZeroLimit] = useState<{ scopeType: string; scopeValue: string } | null>(null);

  const load = useCallback(async () => {
    try {
      setPolicies((await api.get<{ policies: Policy[] }>("/api/llm/policies")).policies);
      setQuotas((await api.get<{ quotas: Quota[] }>("/api/llm/quotas")).quotas);
    } catch {
      setError("설정을 불러오지 못했습니다. 화면을 새로고침하고, 계속되면 관리자에게 알려 주세요.");
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  async function addPolicy(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await api.post("/api/llm/policies", { role, modelPattern: pattern, allowed });
      setPattern("");
      await load();
    } catch (err) {
      setError(errorText(err));
    }
  }

  async function removePolicy() {
    if (!removing) return;
    const id = removing.id;
    setRemoving(null);
    setError("");
    try {
      await api.del(`/api/llm/policies/${id}`);
      await load();
    } catch (err) {
      setError(errorText(err));
    }
  }

  async function putQuota(type: string, value: string, n: number) {
    setError("");
    try {
      await api.put("/api/llm/quotas", { scopeType: type, scopeValue: value, dailyTokenLimit: n });
      setLimit("");
      await load();
    } catch (err) {
      setError(errorText(err));
    }
  }

  function saveQuota(e: FormEvent) {
    e.preventDefault();
    setError("");
    const n = parseLimit(limit);
    if (n === null) {
      setError("하루 한도(토큰)는 0 이상의 숫자로 입력하세요. 쉼표(,)는 넣어도 됩니다.");
      return;
    }
    if (scopeValue.trim() === "") {
      setError(scopeType === "role" ? "대상 역할을 고르세요." : "대상 번호를 입력하세요.");
      return;
    }
    if (n === 0) {
      setZeroLimit({ scopeType, scopeValue: scopeValue.trim() });
      return;
    }
    void putQuota(scopeType, scopeValue.trim(), n);
  }

  return (
    <>
      {error && <Notice tone="error">{error}</Notice>}
      <div className="card">
        <h2>AI 모델 허용 목록</h2>
        <p>역할별로 쓸 수 있는 모델을 정합니다. 목록에 없는 모델은 호출이 거부됩니다. 더 구체적인 규칙(역할·부서 지정)이 우선합니다.</p>
        {policies && policies.length === 0 ? (
          <EmptyState>등록된 모델 규칙이 없습니다.</EmptyState>
        ) : (
          policies && (
            <TableWrap>
              <table aria-label="모델 규칙">
                <thead>
                  <tr>
                    <th className="nowrap">역할</th>
                    <th className="nowrap">부서</th>
                    <th className="nowrap">모델</th>
                    <th className="nowrap">허용 여부</th>
                    <th className="nowrap">관리</th>
                  </tr>
                </thead>
                <tbody>
                  {policies.map((p) => (
                    <tr key={p.id}>
                      <td className="nowrap">{roleLabel(p.role)}</td>
                      <td className="nowrap">{p.departmentId ? `부서 ${p.departmentId}` : "전체"}</td>
                      <td className="nowrap">{p.modelPattern}</td>
                      <td className="nowrap">
                        <Badge tone={p.allowed ? "ok" : "bad"}>{p.allowed ? "허용" : "금지"}</Badge>
                      </td>
                      <td className="nowrap">
                        <button type="button" className="btn-danger btn-sm" onClick={() => setRemoving(p)}>
                          삭제
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )
        )}
        <form onSubmit={addPolicy} aria-label="모델 규칙 추가" className="form-row">
          <label>
            역할
            <select value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="">전체 역할</option>
              {roleOptions.map((r) => (
                <option key={r.key} value={r.key}>
                  {r.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            모델 이름
            <input value={pattern} onChange={(e) => setPattern(e.target.value)} required />
          </label>
          <label>
            <input type="checkbox" checked={allowed} onChange={(e) => setAllowed(e.target.checked)} /> 허용
          </label>
          <button type="submit">규칙 추가</button>
        </form>
        <p className="hint">모델 이름 끝에 *를 붙이면 그 이름으로 시작하는 모델 전체에 적용됩니다.</p>
      </div>

      <div className="card">
        <h2>일일 사용 한도</h2>
        {quotas && quotas.length === 0 ? (
          <EmptyState>설정된 일일 한도가 없습니다.</EmptyState>
        ) : (
          quotas && (
            <TableWrap>
              <table aria-label="일일 사용 한도">
                <thead>
                  <tr>
                    <th className="nowrap">범위</th>
                    <th className="nowrap">대상</th>
                    <th className="num nowrap">하루 한도(토큰)</th>
                  </tr>
                </thead>
                <tbody>
                  {quotas.map((q) => (
                    <tr key={q.id}>
                      <td className="nowrap">{SCOPE_LABEL[q.scopeType] ?? "기타"}</td>
                      <td className="nowrap">{q.scopeType === "role" ? roleLabel(q.scopeValue) : q.scopeValue}</td>
                      <td className="num">{q.dailyTokenLimit.toLocaleString("ko-KR")}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )
        )}
        <form onSubmit={saveQuota} aria-label="한도 설정" className="form-row">
          <label>
            범위
            <select
              value={scopeType}
              onChange={(e) => {
                setScopeType(e.target.value);
                setScopeValue("");
              }}
            >
              {Object.entries(SCOPE_LABEL).map(([k, v]) => (
                <option key={k} value={k}>
                  {v}
                </option>
              ))}
            </select>
          </label>
          <label>
            대상
            {scopeType === "role" ? (
              <select value={scopeValue} onChange={(e) => setScopeValue(e.target.value)}>
                <option value="">역할을 고르세요</option>
                {roleOptions.map((r) => (
                  <option key={r.key} value={r.key}>
                    {r.name}
                  </option>
                ))}
              </select>
            ) : (
              <input inputMode="numeric" value={scopeValue} onChange={(e) => setScopeValue(e.target.value)} />
            )}
          </label>
          <label>
            하루 한도(토큰)
            <input inputMode="numeric" value={limit} onChange={(e) => setLimit(e.target.value)} />
          </label>
          <button type="submit">한도 저장</button>
        </form>
        <p className="hint">대상: 역할은 목록에서 고르고, 부서·사용자는 번호를 입력합니다. 하루 한도는 숫자로 입력합니다(쉼표 가능).</p>
      </div>

      {removing && (
        <ConfirmDialog title="모델 규칙 삭제" confirmLabel="삭제" onConfirm={() => void removePolicy()} onCancel={() => setRemoving(null)}>
          <p>
            {roleLabel(removing.role)} · {removing.modelPattern}({removing.allowed ? "허용" : "금지"}) 규칙을 삭제합니다. 삭제하면 이 규칙이 없을 때의 판정으로 바로 바뀝니다.
            규칙이 하나도 없으면 모든 AI 호출이 거부됩니다. 삭제는 감사 로그에 남습니다.
          </p>
        </ConfirmDialog>
      )}
      {zeroLimit && (
        <ConfirmDialog
          title="하루 한도를 0 으로 저장"
          confirmLabel="0 으로 저장"
          onConfirm={() => {
            const z = zeroLimit;
            setZeroLimit(null);
            void putQuota(z.scopeType, z.scopeValue, 0);
          }}
          onCancel={() => setZeroLimit(null)}
        >
          <p>하루 한도를 0 으로 저장하면 이 대상의 AI 호출이 모두 막힙니다. 한도를 없애려는 것이라면 취소하고 충분히 큰 수를 넣으세요.</p>
        </ConfirmDialog>
      )}
    </>
  );
}
