// 개인 토큰 발급·폐기 화면. 원문은 발급 직후 한 번만 보여 준다.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, ApiError } from "../api/client";
import { businessMessage } from "../purchase/useLoad";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";

export interface TokenInfo {
  id: number;
  name: string;
  prefix: string;
  createdAt: string;
  expiresAt: string;
  revokedAt: string | null;
  lastUsedAt: string | null;
}

function fmt(s: string | null): string {
  return s ? new Date(s).toLocaleString("ko-KR") : "-";
}

const STATUS_TONE: Record<string, Tone> = { "사용 중": "ok", 폐기됨: "muted", 만료됨: "muted" };

function statusOf(t: TokenInfo, now = Date.now()): string {
  if (t.revokedAt) return "폐기됨";
  if (new Date(t.expiresAt).getTime() <= now) return "만료됨";
  return "사용 중";
}

/** AI 도구 연결 안내 카드(docs/mcp-connect.md 요약). 서버 주소는 지금 열린 주소를 쓴다. */
function ConnectGuide() {
  const cmd = `claude mcp add --transport http procure-ledger ${window.location.origin}/mcp --header "Authorization: Bearer <토큰>"`;
  return (
    <section className="card" aria-labelledby="mcp-guide-title">
      <h2 id="mcp-guide-title">AI 도구 연결 안내</h2>
      <ol>
        <li>위 양식에서 이름과 유효 기간(1~365일, 기본 30일)을 정해 &quot;발급&quot;을 누릅니다.</li>
        <li>화면에 나온 토큰(plp_ 로 시작)을 복사합니다. 창을 닫으면 다시 볼 수 없으니 잃어버리면 폐기하고 새로 발급하세요.</li>
        <li>AI 도구(Claude Code 등)에 아래 주소와 토큰을 넣습니다. AI 는 이 토큰을 발급받은 사람의 권한으로만 움직입니다.</li>
      </ol>
      <pre data-testid="mcp-command">{cmd}</pre>
      <p className="hint">
        토큰은 비밀번호처럼 보관하세요. 설정 파일을 저장소에 올리지 마세요. 토큰으로는 토큰을 발급하거나 폐기할 수 없고, 로그인한 화면에서만 됩니다.
        Claude Desktop 연결 등 자세한 방법은 저장소의 <code>docs/mcp-connect.md</code> 를 보세요.
      </p>
    </section>
  );
}

export function TokensPage() {
  const [tokens, setTokens] = useState<TokenInfo[]>([]);
  const [name, setName] = useState("");
  const [days, setDays] = useState(30);
  const [fresh, setFresh] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [revoking, setRevoking] = useState<TokenInfo | null>(null);

  const load = useCallback(async () => {
    try {
      setTokens(await api.get<TokenInfo[]>("/api/tokens"));
    } catch {
      setError("토큰 목록을 불러오지 못했습니다.");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function issue(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      const res = await api.post<{ token: string }>("/api/tokens", { name, ttlDays: days });
      setFresh(res.token);
      setName("");
      await load();
    } catch (e) {
      // 409 는 사용 중인 토큰이 상한에 닿은 경우로, 서버의 업무 문구를 그대로 보인다.
      if (e instanceof ApiError && e.status === 409) setError(businessMessage(e.message));
      else setError("토큰을 발급하지 못했습니다. 이름과 유효 기간(1~365일)을 확인하세요.");
    }
  }

  async function revoke() {
    if (!revoking) return;
    const id = revoking.id;
    setRevoking(null);
    setError("");
    try {
      await api.del(`/api/tokens/${id}`);
      await load();
    } catch {
      setError("토큰을 폐기하지 못했습니다. 잠시 뒤 다시 시도하세요.");
    }
  }

  return (
    <section>
      <h1>개인 토큰</h1>
      <p>AI 도구나 외부 프로그램에서 본인 권한으로 접속할 때 쓰는 토큰입니다. 비밀번호처럼 보관하세요.</p>

      <form onSubmit={issue} className="form-row">
        <label>
          이름 <input value={name} onChange={(e) => setName(e.target.value)} maxLength={80} required />
        </label>
        <label>
          유효 기간(일) <input type="number" min={1} max={365} value={days} onChange={(e) => setDays(Number(e.target.value))} />
        </label>
        <button type="submit">발급</button>
      </form>

      {fresh && (
        <div role="status" className="notice notice-warn">
          <p>
            <strong>이 토큰은 지금만 볼 수 있습니다.</strong> 창을 닫으면 다시 확인할 수 없습니다.
          </p>
          <code data-testid="fresh-token">{fresh}</code>{" "}
          <button type="button" className="btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(fresh)}>
            복사
          </button>{" "}
          <button type="button" className="btn-sm" onClick={() => setFresh(null)}>
            저장했습니다
          </button>
        </div>
      )}
      {error && <Notice tone="error">{error}</Notice>}

      {tokens.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>이름</th>
                <th>토큰 앞부분</th>
                <th>발급</th>
                <th>만료</th>
                <th>마지막 사용</th>
                <th>상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => (
                <tr key={t.id}>
                  <td>{t.name}</td>
                  <td>{t.prefix}…</td>
                  <td className="nowrap">{fmt(t.createdAt)}</td>
                  <td className="nowrap">{fmt(t.expiresAt)}</td>
                  <td className="nowrap">{fmt(t.lastUsedAt)}</td>
                  <td>
                    <Badge tone={STATUS_TONE[statusOf(t)]}>{statusOf(t)}</Badge>
                  </td>
                  <td>
                    {statusOf(t) === "사용 중" && (
                      <button type="button" className="btn-danger btn-sm" onClick={() => setRevoking(t)}>
                        폐기
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {tokens.length === 0 && <EmptyState hint="위 양식에서 이름을 정해 발급하세요.">발급한 토큰이 없습니다.</EmptyState>}
      <ConnectGuide />
      {revoking && (
        <ConfirmDialog title="토큰 폐기" confirmLabel="폐기" onConfirm={() => void revoke()} onCancel={() => setRevoking(null)}>
          <p>
            「{revoking.name}」({revoking.prefix}…) 토큰을 폐기합니다. 이 토큰으로 연결한 AI 도구는 바로 접속하지 못하게 되고, 폐기한 토큰은 되살릴 수 없습니다.
            다시 연결하려면 새 토큰을 발급하세요.
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}
