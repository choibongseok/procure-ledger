// 로그인 화면. 켜진 방식만 그린다(GET /api/auth/config).
// 이메일+비밀번호는 POST /api/auth/login/password, 외부 로그인(OIDC)은 서버의 /api/auth/login 흐름이 한다.
import { useEffect, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { parseAuthError } from "./authApi";
import { ROLE_LABELS, type Role } from "./permissions";

interface DevAccount {
  email: string;
  name: string;
  role: Role;
  department: string;
}

interface LoginProvider {
  id: string;
  label: string;
  kind: string;
}

interface AuthConfig {
  login: boolean;
  /** 켜진 외부 로그인 방식(서버가 켜지고 설정이 끝난 것만 준다). 없으면 login 으로 범용 버튼 하나를 그린다. */
  providers?: LoginProvider[];
  devIdp: boolean;
  /** 이메일+비밀번호 로그인이 켜져 있는지. 서버가 알려 주지 않으면 켜진 것으로 본다. */
  emailLogin?: boolean;
  passwordResetByMail?: boolean;
  devAccounts?: DevAccount[];
  /** 설치 마법사가 열려 있는지(빈 DB 첫 기동). */
  setupPending?: boolean;
}

export const LOGIN_ERRORS: Record<string, string> = {
  forbidden: "등록되지 않았거나 사용할 수 없는 계정입니다. 관리자에게 문의하세요.",
  state: "로그인 요청이 만료되었습니다. 다시 시도해 주세요.",
  idp: "사내 인증 서비스에서 로그인하지 못했습니다. 잠시 후 다시 시도해 주세요.",
  server: "로그인 처리 중 서버 오류가 발생했습니다. 잠시 뒤 다시 시도하고, 계속되면 관리자에게 알려 주세요.",
};

export function LoginPage() {
  const [params] = useSearchParams();
  const [cfg, setCfg] = useState<AuthConfig | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [formError, setFormError] = useState("");
  const [busy, setBusy] = useState(false);
  const [ticket, setTicket] = useState("");
  const [code, setCode] = useState("");
  const error = params.get("error");

  useEffect(() => {
    let alive = true;
    api
      .get<AuthConfig>("/api/auth/config")
      .then((c) => alive && setCfg(c))
      .catch(() => alive && setCfg(null));
    return () => {
      alive = false;
    };
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFormError("");
    setBusy(true);
    try {
      const r = await api.post<{ totp?: boolean; ticket?: string }>("/api/auth/login/password", { email, password });
      if (r?.totp && r.ticket) {
        setTicket(r.ticket); // 비밀번호는 통과, 2단계 코드가 남았다
        setPassword("");
        return;
      }
      // 새 세션으로 처음부터 다시 읽는다. 제한 세션이면 /api/me 의 setup 이 App 을 설정 화면으로 보낸다.
      window.location.assign(params.get("returnTo")?.startsWith("/") ? (params.get("returnTo") as string) : "/");
    } catch (err) {
      setFormError(parseAuthError(err).message);
      setPassword(""); // 입력한 이메일은 남기고 비밀번호는 비운다
    } finally {
      setBusy(false);
    }
  }

  async function submitCode(e: FormEvent) {
    e.preventDefault();
    setFormError("");
    setBusy(true);
    try {
      await api.post("/api/auth/login/totp", { ticket, code });
      window.location.assign(params.get("returnTo")?.startsWith("/") ? (params.get("returnTo") as string) : "/");
    } catch (err) {
      const b = parseAuthError(err);
      setFormError(b.message);
      setCode("");
      if (b.code === "bad_ticket" || b.code === "locked") setTicket(""); // 처음부터 다시
    } finally {
      setBusy(false);
    }
  }

  const emailLogin = cfg ? cfg.emailLogin !== false : false;
  const providers: LoginProvider[] = cfg ? (cfg.providers ?? (cfg.login ? [{ id: "oidc", label: "사내 계정으로 로그인", kind: "oidc" }] : [])) : [];
  const noLogin = cfg !== null && providers.length === 0 && !emailLogin;

  return (
    <main className="auth-box">
      <h1>로그인</h1>
      <p>사내 계정으로 로그인합니다.</p>
      {cfg?.setupPending && (
        <Notice tone="warn">
          초기 설정이 필요합니다. <Link to="/setup">초기 설정 시작</Link> (서버 로그의 설치 코드가 필요합니다)
        </Notice>
      )}
      {error && <Notice tone="error">{LOGIN_ERRORS[error] ?? "로그인하지 못했습니다."}</Notice>}
      {noLogin && <Notice tone="error">로그인이 아직 설정되지 않았습니다. 관리자에게 문의하세요.</Notice>}
      {emailLogin && ticket && (
        <form onSubmit={submitCode} aria-label="2단계 인증" className="card">
          <p>인증 앱의 6자리 코드를 입력하세요. 앱을 쓸 수 없으면 복구 코드를 입력합니다.</p>
          <div className="field">
            <label htmlFor="login-code">인증 코드</label>
            <input id="login-code" inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} required />
          </div>
          {formError && <Notice tone="error">{formError}</Notice>}
          <button type="submit" disabled={busy}>
            확인
          </button>
        </form>
      )}
      {emailLogin && !ticket && (
        <form onSubmit={submit} aria-label="이메일 로그인" className="card">
          <div className="field">
            <label htmlFor="login-email">이메일</label>
            <input id="login-email" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="login-password">비밀번호</label>
            <input
              id="login-password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </div>
          {formError && <Notice tone="error">{formError}</Notice>}
          <button type="submit" disabled={busy}>
            로그인
          </button>
          <p className="hint">
            {cfg?.passwordResetByMail ? (
              <Link to="/login/forgot">비밀번호를 잊으셨나요?</Link>
            ) : (
              "비밀번호를 잊으셨다면 관리자에게 임시 비밀번호를 요청하세요. 메일 발송이 설정되지 않았습니다."
            )}
          </p>
        </form>
      )}
      {providers.length > 0 && (
        <div className="login-providers" aria-label="외부 계정으로 로그인">
          {providers.map((p) => (
            <p key={p.id}>
              <a
                className={emailLogin ? "btn btn-secondary" : "btn"}
                href={p.id === "oidc" ? "/api/auth/login" : p.id === "saml" ? "/api/auth/saml/login" : `/api/auth/login?provider=${encodeURIComponent(p.id)}`}
              >
                {p.label}
              </a>
            </p>
          ))}
        </div>
      )}
      {cfg?.devIdp && cfg.devAccounts && (
        <section aria-label="개발용 계정" className="card">
          <h2>개발용 계정</h2>
          <p>로컬 검증용입니다. 운영 환경에서는 보이지 않습니다.</p>
          <ul>
            {cfg.devAccounts.map((a) => (
              <li key={a.email}>
                <a href={`/api/auth/login?login_hint=${encodeURIComponent(a.email)}`}>
                  {a.name} ({ROLE_LABELS[a.role] ?? a.role}, {a.department})
                </a>
              </li>
            ))}
          </ul>
        </section>
      )}
    </main>
  );
}
