// 내 계정(/account): 비밀번호 변경, 2단계 인증. 로그인한 모두에게 열린다(authenticated).
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { parseAuthError } from "./authApi";
import { PERMISSION_LABELS } from "../admin/auditLabels";
import { TotpPanel, type AuthSummary } from "./TotpPanel";

interface MyAccess {
  roles: string[];
  permissions: string[];
  sources: { permission: string; roleName: string; via: string }[];
}

const METHOD_LABELS: Record<string, string> = {
  password: "이메일·비밀번호",
  oidc: "사내 계정(OIDC)",
  dev: "개발용",
  google: "구글",
  microsoft: "마이크로소프트",
  saml: "SAML",
};

export function AccountPage() {
  const [summary, setSummary] = useState<AuthSummary | null>(null);
  const [cur, setCur] = useState("");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [error, setError] = useState("");
  const [info, setInfo] = useState("");
  const [access, setAccess] = useState<MyAccess | null>(null);

  useEffect(() => {
    api
      .get<MyAccess>("/api/me/access")
      .then((a) => setAccess(Array.isArray(a?.roles) ? { roles: a.roles, permissions: a.permissions ?? [], sources: a.sources ?? [] } : null))
      .catch(() => setAccess(null));
  }, []);

  const load = useCallback(async () => {
    try {
      const me = await api.get<{ auth: AuthSummary }>("/api/me");
      setSummary(me.auth);
    } catch {
      setError("내 계정 정보를 불러오지 못했습니다.");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function change(e: FormEvent) {
    e.preventDefault();
    setError("");
    setInfo("");
    if (pw !== pw2) {
      setError("새 비밀번호 두 칸이 서로 다릅니다.");
      return;
    }
    try {
      await api.post("/api/auth/password/change", { current: cur, new: pw });
      setInfo("비밀번호를 바꿨습니다. 다른 기기의 로그인은 모두 끊어졌습니다.");
      setCur("");
      setPw("");
      setPw2("");
    } catch (err) {
      setError(parseAuthError(err).message);
    }
  }

  return (
    <section>
      <h1>내 계정</h1>
      {summary && <p>이 세션의 로그인 방식: {METHOD_LABELS[summary.method] ?? summary.method}</p>}
      <section className="card" aria-labelledby="acct-access">
        <h2 id="acct-access">내 권한</h2>
        {access ? (
          <>
            <p>
              역할: {access.roles.length ? access.roles.join(", ") : "없음"} · 권한 {access.permissions.length}개. 권한이 어디서 왔는지는 아래에서 봅니다.
            </p>
            <details>
              <summary>권한과 출처 보기</summary>
              <ul>
                {[...new Set(access.sources.map((s) => s.permission))].map((p) => (
                  <li key={p}>
                    {PERMISSION_LABELS[p] ?? p} — {[...new Set(access.sources.filter((s) => s.permission === p).map((s) => `${s.roleName} ← ${s.via}`))].join(" · ")}
                  </li>
                ))}
              </ul>
            </details>
          </>
        ) : (
          <p>권한 정보를 불러오지 못했습니다.</p>
        )}
      </section>
      <section className="card" aria-labelledby="acct-pw">
        <h2 id="acct-pw">비밀번호</h2>
        {summary && !summary.hasPassword ? (
          <p>이메일·비밀번호 로그인이 시작되지 않은 계정입니다. 쓰려면 관리자에게 &quot;로그인 시작&quot;을 요청하세요.</p>
        ) : (
          <form onSubmit={change} aria-label="비밀번호 변경">
            <div className="field">
              <label htmlFor="acct-cur">현재 비밀번호</label>
              <input id="acct-cur" type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} required />
            </div>
            <div className="field">
              <label htmlFor="acct-new">새 비밀번호</label>
              <input id="acct-new" type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} required />
              <span className="hint">10자 이상, 영문 대·소문자·숫자·기호 중 3종류 이상. 최근에 쓴 비밀번호는 다시 쓸 수 없습니다.</span>
            </div>
            <div className="field">
              <label htmlFor="acct-new2">새 비밀번호 확인</label>
              <input id="acct-new2" type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} required />
            </div>
            {error && <Notice tone="error">{error}</Notice>}
            {info && <Notice tone="ok">{info}</Notice>}
            <button type="submit">비밀번호 바꾸기</button>
          </form>
        )}
      </section>
      <section className="card" aria-labelledby="acct-totp">
        <h2 id="acct-totp">2단계 인증</h2>
        {summary && summary.hasPassword ? (
          <TotpPanel summary={summary} onChanged={load} />
        ) : (
          <p>이메일·비밀번호로 로그인하는 계정에만 적용됩니다. 구글·MS·SAML 로그인은 해당 서비스의 2단계 인증을 쓰세요.</p>
        )}
      </section>
    </section>
  );
}
