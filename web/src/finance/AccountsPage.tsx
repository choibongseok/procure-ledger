// 계정과목 표와 자동 전표 규칙(B31 3.1). 조회는 재무·관리자·감사, 변경은 재무·관리자(account:manage).
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import type { Account, AccountRule } from "./types";

export function AccountsPage() {
  const { can } = useAuth();
  const manage = can("account:manage");
  const accounts = useLoad(() => api.get<Account[]>("/api/finance/accounts?all=1"), []);
  const rules = useLoad(() => api.get<AccountRule[]>("/api/finance/account-rules"), []);
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [editing, setEditing] = useState<{ code: string; name: string } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function run(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      await Promise.all([accounts.reload(), rules.reload()]);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  const list = accounts.data ?? [];
  const activeAccounts = list.filter((a) => a.active);
  return (
    <section>
      <h1>계정과목</h1>
      <p className="hint">전표의 줄이 가리키는 분류입니다. 코드·구분은 바꿀 수 없고, 쓰인 계정은 지울 수 없어 중지만 할 수 있습니다. 시스템 계정은 자동 전표 규칙이 쓰므로 중지할 수 없습니다.</p>
      {error && <Notice tone="error">{error}</Notice>}
      {accounts.error && <Notice tone="error">{accounts.error}</Notice>}
      {manage && (
        <div className="form-row">
          <div className="field">
            <label htmlFor="ac-code">코드(4자리)</label>
            <input id="ac-code" value={code} maxLength={4} inputMode="numeric" onChange={(e) => setCode(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="ac-name">이름</label>
            <input id="ac-name" value={name} maxLength={40} onChange={(e) => setName(e.target.value)} />
          </div>
          <button
            type="button"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                await api.post("/api/finance/accounts", { code, name });
                setCode("");
                setName("");
              })
            }
          >
            계정 추가
          </button>
        </div>
      )}
      {list.length === 0 && !accounts.loading && <EmptyState>등록된 계정과목이 없습니다.</EmptyState>}
      {list.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th className="nowrap">코드</th>
                <th>이름</th>
                <th className="nowrap">구분</th>
                <th className="nowrap">상태</th>
                {manage && <th />}
              </tr>
            </thead>
            <tbody>
              {list.map((a) => (
                <tr key={a.code}>
                  <td data-label="코드" className="nowrap">{a.code}</td>
                  <td data-label="이름">
                    {editing?.code === a.code ? (
                      <input aria-label={`${a.code} 이름`} value={editing.name} maxLength={40} onChange={(e) => setEditing({ code: a.code, name: e.target.value })} />
                    ) : (
                      a.name
                    )}
                    {a.system && <> <Badge tone="info">시스템</Badge></>}
                  </td>
                  <td data-label="구분" className="nowrap">{a.kindLabel}</td>
                  <td data-label="상태" className="nowrap">{a.active ? <Badge tone="ok">사용 중</Badge> : <Badge tone="muted">중지</Badge>}</td>
                  {manage && (
                    <td className="nowrap">
                      {editing?.code === a.code ? (
                        <>
                          <button type="button" className="btn-sm" disabled={busy} onClick={() => void run(async () => { await api.put(`/api/finance/accounts/${a.code}`, { name: editing.name }); setEditing(null); })}>
                            저장
                          </button>{" "}
                          <button type="button" className="btn-secondary btn-sm" onClick={() => setEditing(null)}>
                            취소
                          </button>
                        </>
                      ) : (
                        <>
                          <button type="button" className="btn-secondary btn-sm" onClick={() => setEditing({ code: a.code, name: a.name })}>
                            이름 변경
                          </button>{" "}
                          {!a.system && (
                            <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void run(() => api.post(`/api/finance/accounts/${a.code}/active`, { active: !a.active }))}>
                              {a.active ? "중지" : "다시 사용"}
                            </button>
                          )}
                        </>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      <h2>자동 전표 규칙</h2>
      <p className="hint">증빙 판정 확정·지출 결의서 승인 때 자동 전표가 쓰는 계정입니다. 규칙이 가리키는 계정이 중지되면 자동 전표는 전표 대기로 갑니다.</p>
      {rules.error && <Notice tone="error">{rules.error}</Notice>}
      <TableWrap>
        <table className="stack-table">
          <thead>
            <tr>
              <th>규칙</th>
              <th>계정</th>
            </tr>
          </thead>
          <tbody>
            {(rules.data ?? []).map((r) => (
              <tr key={r.key}>
                <td data-label="규칙">{r.label || r.key}</td>
                <td data-label="계정">
                  {manage ? (
                    <select aria-label={`${r.label} 계정`} value={r.accountCode} disabled={busy} onChange={(e) => void run(() => api.put(`/api/finance/account-rules/${r.key}`, { accountCode: e.target.value }))}>
                      {[...activeAccounts, ...list.filter((a) => !a.active && a.code === r.accountCode)].map((a) => (
                        <option key={a.code} value={a.code}>
                          {a.code} {a.name}
                          {a.active ? "" : "(중지)"}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <>
                      {r.accountCode} {r.accountName}
                      {!r.active && " (중지)"}
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
    </section>
  );
}
