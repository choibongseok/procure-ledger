// 비상 로그인(/login/emergency): 이메일 로그인을 꺼도 비상 관리자만 비밀번호+2단계 코드로 들어온다. 로그인 화면에 링크가 없다.
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { parseAuthError } from "./authApi";

export function EmergencyLoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [ticket, setTicket] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function first(e: FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      const r = await api.post<{ ticket: string }>("/api/auth/login/emergency", { email, password });
      setTicket(r.ticket);
    } catch (err) {
      setError(parseAuthError(err).message);
    } finally {
      setPassword("");
      setBusy(false);
    }
  }

  async function second(e: FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      await api.post("/api/auth/login/totp", { ticket, code });
      window.location.assign("/");
    } catch (err) {
      const b = parseAuthError(err);
      setError(b.message);
      setCode("");
      if (b.code === "bad_ticket" || b.code === "locked") setTicket("");
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="auth-box">
      <h1>비상 로그인</h1>
      <Notice tone="warn">비상 관리자 전용입니다. 사용할 때마다 감사 로그에 경고로 남고 관리자 화면에 알림이 표시됩니다.</Notice>
      {error && <Notice tone="error">{error}</Notice>}
      {!ticket ? (
        <form onSubmit={first} aria-label="비상 로그인" className="card">
          <div className="field">
            <label htmlFor="em-email">이메일</label>
            <input id="em-email" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="em-pw">비밀번호</label>
            <input id="em-pw" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </div>
          <button type="submit" disabled={busy}>
            다음
          </button>
        </form>
      ) : (
        <form onSubmit={second} aria-label="2단계 인증" className="card">
          <div className="field">
            <label htmlFor="em-code">인증 앱의 6자리 코드(또는 복구 코드)</label>
            <input id="em-code" inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} required />
          </div>
          <button type="submit" disabled={busy}>
            로그인
          </button>
        </form>
      )}
      <p>
        <Link to="/login">일반 로그인으로</Link>
      </p>
    </main>
  );
}
