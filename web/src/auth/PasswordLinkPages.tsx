// 비밀번호 재설정 요청(/login/forgot)과 링크로 새 비밀번호 정하기(/login/reset?token=...).
import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { parseAuthError } from "./authApi";

export function ForgotPasswordPage() {
  const [email, setEmail] = useState("");
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await api.post("/api/auth/password/forgot", { email });
      setDone(true);
    } catch (err) {
      setError(parseAuthError(err).message);
    }
  }

  return (
    <main className="auth-box">
      <h1>비밀번호 재설정</h1>
      {done ? (
        <Notice tone="ok">등록된 이메일이면 재설정 링크를 보냈습니다. 링크는 30분 동안, 한 번만 쓸 수 있습니다.</Notice>
      ) : (
        <form onSubmit={submit} className="card">
          <div className="field">
            <label htmlFor="forgot-email">이메일</label>
            <input id="forgot-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </div>
          {error && <Notice tone="error">{error}</Notice>}
          <button type="submit">재설정 링크 받기</button>
        </form>
      )}
      <p>
        <Link to="/login">로그인으로 돌아가기</Link>
      </p>
    </main>
  );
}

export function ResetPasswordPage() {
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [done, setDone] = useState(false);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (pw !== pw2) {
      setError("새 비밀번호 두 칸이 서로 다릅니다.");
      return;
    }
    try {
      await api.post("/api/auth/password/reset", { token, password: pw });
      setDone(true);
    } catch (err) {
      setError(parseAuthError(err).message);
    }
  }

  return (
    <main className="auth-box">
      <h1>새 비밀번호 정하기</h1>
      {!token ? (
        <Notice tone="error">링크가 올바르지 않습니다. 메일의 링크를 다시 여세요.</Notice>
      ) : done ? (
        <Notice tone="ok">비밀번호를 바꿨습니다. 새 비밀번호로 로그인하세요.</Notice>
      ) : (
        <form onSubmit={submit} className="card">
          <p className="hint">10자 이상, 영문 대·소문자·숫자·기호 중 3종류 이상. 최근에 쓴 비밀번호는 다시 쓸 수 없습니다.</p>
          <div className="field">
            <label htmlFor="reset-pw">새 비밀번호</label>
            <input id="reset-pw" type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="reset-pw2">새 비밀번호 확인</label>
            <input id="reset-pw2" type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} required />
          </div>
          {error && <Notice tone="error">{error}</Notice>}
          <button type="submit">비밀번호 바꾸기</button>
        </form>
      )}
      <p>
        <Link to="/login">로그인으로 가기</Link>
      </p>
    </main>
  );
}
