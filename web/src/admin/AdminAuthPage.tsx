// 로그인 설정(/admin/auth, 권한 auth:manage): 사용자별 로그인 자격 상태와 "로그인 시작"·"잠금 해제".
// 방식 설정(구글·MS·SAML)·정책·비상 로그인은 이 화면에 단계별로 더해진다. 비밀값 입력칸은 없다.
import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import { parseAuthError } from "../auth/authApi";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { ProvidersSection } from "./AuthProvidersSection";

export interface CredStatus {
  userId: number;
  email: string;
  name: string;
  role: string;
  active: boolean;
  hasPassword: boolean;
  mustChange: boolean;
  totp: boolean;
  lockedUntil?: string;
  identities: string[];
  canAuthManage?: boolean;
}

interface StartResult {
  via: "mail" | "temp";
  tempPassword?: string;
  expiresAt?: string;
}

const METHOD_LABELS: Record<string, string> = {
  oidc: "사내 계정(OIDC)",
  dev: "개발용",
  google: "구글",
  microsoft: "마이크로소프트",
  saml: "SAML",
};

function fmt(s?: string): string {
  return s ? new Date(s).toLocaleString("ko-KR") : "";
}

/** 사용자별 로그인 자격 상태와 시작·잠금 해제·2단계 초기화·연결 해제. */
export function UsersSection({ onChanged, onLoaded }: { onChanged?: () => void; onLoaded?: (users: CredStatus[]) => void }) {
  const [users, setUsers] = useState<CredStatus[]>([]);
  const [mailEnabled, setMailEnabled] = useState(false);
  const [error, setError] = useState("");
  const [info, setInfo] = useState("");
  const [fresh, setFresh] = useState<{ name: string; password: string; expiresAt?: string } | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ users: CredStatus[]; mailEnabled: boolean }>("/api/admin/auth/users");
      setUsers(r.users ?? []);
      onLoaded?.(r.users ?? []);
      setMailEnabled(!!r.mailEnabled);
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }, [onLoaded]);

  useEffect(() => {
    void load();
  }, [load]);

  async function start(u: CredStatus, via: "mail" | "temp") {
    setError("");
    setInfo("");
    setFresh(null);
    try {
      const r = await api.post<StartResult>(`/api/admin/users/${u.userId}/auth/start`, { via });
      if (r.via === "temp" && r.tempPassword) setFresh({ name: u.name, password: r.tempPassword, expiresAt: r.expiresAt });
      else setInfo(`${u.name}님에게 비밀번호 설정 링크를 보냈습니다. 링크는 72시간 동안, 한 번만 쓸 수 있습니다.`);
      await load();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  async function resetTotp(u: CredStatus) {
    setError("");
    setInfo("");
    try {
      await api.post(`/api/admin/users/${u.userId}/auth/totp-reset`);
      setInfo(`${u.name}님의 2단계 인증을 초기화했습니다. 로그인 중인 세션은 모두 끊어졌습니다.`);
      await load();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  async function unlink(u: CredStatus, provider: string) {
    setError("");
    setInfo("");
    try {
      await api.post(`/api/admin/users/${u.userId}/auth/unlink`, { provider });
      setInfo(`${u.name}님의 ${METHOD_LABELS[provider] ?? provider} 연결을 해제했습니다. 그 방식으로 로그인 중인 세션은 끊어졌습니다.`);
      await load();
      onChanged?.();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  async function unlock(u: CredStatus) {
    setError("");
    setInfo("");
    try {
      await api.post(`/api/admin/users/${u.userId}/auth/unlock`);
      setInfo(`${u.name}님의 계정 잠금을 풀었습니다.`);
      await load();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  return (
    <section aria-labelledby="auth-users-title">
      <h2 id="auth-users-title">사용자별 로그인 상태</h2>
      <p>사용자별 로그인 상태입니다. 계정은 입사 발령으로 만들어지며, 비밀번호는 여기서 &quot;로그인 시작&quot;으로 시작합니다. 관리자는 비밀번호를 직접 정할 수 없습니다.</p>
      {!mailEnabled && (
        <Notice tone="warn">
          메일 발송이 설정되지 않았습니다(PL_SMTP_HOST). 설정 링크 대신 임시 비밀번호로 시작하세요. 본인 계정의 임시 비밀번호는 다른 관리자가 발급합니다.
        </Notice>
      )}
      {error && <Notice tone="error">{error}</Notice>}
      {info && <Notice tone="ok">{info}</Notice>}
      {fresh && (
        <div role="status" className="notice notice-warn">
          <p>
            <strong>{fresh.name}님의 임시 비밀번호는 지금만 볼 수 있습니다.</strong> 창을 닫으면 다시 확인할 수 없습니다. {fmt(fresh.expiresAt)} 까지 유효하며, 첫 로그인에서 새 비밀번호로 바꿔야 합니다.
          </p>
          <code data-testid="temp-password">{fresh.password}</code>{" "}
          <button type="button" className="btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(fresh.password)}>
            복사
          </button>{" "}
          <button type="button" className="btn-sm" onClick={() => setFresh(null)}>
            전달했습니다
          </button>
        </div>
      )}
      {users.length === 0 ? (
        <EmptyState>사용자가 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>이름</th>
                <th>이메일</th>
                <th>비밀번호</th>
                <th>2단계 인증</th>
                <th>상태</th>
                <th>연결된 방식</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.userId}>
                  <td>{u.name}</td>
                  <td>{u.email}</td>
                  <td>{u.hasPassword ? <Badge tone="ok">설정됨</Badge> : <Badge tone="muted">없음</Badge>}</td>
                  <td>{u.totp ? <Badge tone="ok">등록됨</Badge> : <Badge tone="muted">없음</Badge>}</td>
                  <td>
                    {!u.active && <Badge tone="muted">비활성</Badge>}{" "}
                    {u.lockedUntil && <Badge tone="bad">잠김 ({fmt(u.lockedUntil)} 까지)</Badge>}{" "}
                    {u.mustChange && <Badge tone="warn">변경 필요</Badge>}
                  </td>
                  <td>
                    {u.identities.length
                      ? u.identities.map((m) => (
                          <span key={m} className="nowrap">
                            {METHOD_LABELS[m] ?? m}{" "}
                            <button type="button" className="btn-secondary btn-sm" aria-label={`${u.name} ${METHOD_LABELS[m] ?? m} 연결 해제`} onClick={() => void unlink(u, m)}>
                              해제
                            </button>{" "}
                          </span>
                        ))
                      : "-"}
                  </td>
                  <td className="nowrap">
                    {u.active && (
                      <>
                        {mailEnabled && (
                          <button type="button" className="btn-secondary btn-sm" onClick={() => void start(u, "mail")}>
                            설정 링크 보내기
                          </button>
                        )}{" "}
                        <button type="button" className="btn-secondary btn-sm" onClick={() => void start(u, "temp")}>
                          임시 비밀번호 발급
                        </button>{" "}
                      </>
                    )}
                    {u.totp && (
                      <>
                        <button type="button" className="btn-secondary btn-sm" onClick={() => void resetTotp(u)}>
                          2단계 초기화
                        </button>{" "}
                      </>
                    )}
                    {u.lockedUntil && (
                      <button type="button" className="btn-sm" onClick={() => void unlock(u)}>
                        잠금 해제
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

/** 로그인 설정: 방식 카드·정책·사용자별 상태. */
export function AdminAuthPage() {
  const [users, setUsers] = useState<CredStatus[]>([]);
  const [key, setKey] = useState(0);
  const onLoaded = useCallback((u: CredStatus[]) => setUsers(u), []);
  const admins = users.filter((u) => u.canAuthManage && u.active).map((u) => ({ id: u.userId, name: u.name }));
  return (
    <div>
      <h1>로그인 설정</h1>
      <p>로그인 방식, 비밀번호·2단계 인증 정책, 비상 관리자, 사용자별 로그인 상태를 관리합니다. 비밀값(클라이언트 비밀·SMTP 비밀번호·키)은 서버 환경변수로만 설정하며 이 화면에는 입력칸이 없습니다.</p>
      <ProvidersSection adminUsers={admins} refreshKey={key} />
      <UsersSection onLoaded={onLoaded} onChanged={() => setKey((k) => k + 1)} />
    </div>
  );
}
