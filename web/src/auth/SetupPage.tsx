// 제한 세션(비밀번호 변경·2단계 인증 등록 전)에서 App 이 대신 그리는 화면.
// 제한 세션은 이 화면의 API 와 내 정보·로그아웃 외에는 서버가 모두 403(X-Auth-Setup)으로 막는다.
import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { parseAuthError } from "./authApi";
import { useAuth } from "./AuthProvider";
import { TotpPanel, type AuthSummary } from "./TotpPanel";

function PasswordChangeForm() {
  const { refresh } = useAuth();
  const [cur, setCur] = useState("");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (pw !== pw2) {
      setError("새 비밀번호 두 칸이 서로 다릅니다.");
      return;
    }
    try {
      await api.post("/api/auth/password/change", { current: cur, new: pw });
      await refresh(); // setup 이 풀리면 App 이 업무 화면을 그린다
    } catch (err) {
      setError(parseAuthError(err).message);
    }
  }

  return (
    <form onSubmit={submit} className="card" aria-label="비밀번호 변경">
      <p>임시 비밀번호로 로그인했거나 비밀번호를 바꿔야 하는 계정입니다. 새 비밀번호를 정하면 업무 화면으로 이동합니다.</p>
      <p className="hint">10자 이상, 영문 대·소문자·숫자·기호 중 3종류 이상. 최근에 쓴 비밀번호는 다시 쓸 수 없습니다.</p>
      <div className="field">
        <label htmlFor="setup-cur">현재 비밀번호</label>
        <input id="setup-cur" type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} required />
      </div>
      <div className="field">
        <label htmlFor="setup-pw">새 비밀번호</label>
        <input id="setup-pw" type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} required />
      </div>
      <div className="field">
        <label htmlFor="setup-pw2">새 비밀번호 확인</label>
        <input id="setup-pw2" type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} required />
      </div>
      {error && <Notice tone="error">{error}</Notice>}
      <button type="submit">비밀번호 바꾸기</button>
    </form>
  );
}

function TotpEnrollStep() {
  const { refresh } = useAuth();
  const [summary, setSummary] = useState<AuthSummary | null>(null);
  useEffect(() => {
    api
      .get<{ auth: AuthSummary }>("/api/me")
      .then((me) => setSummary(me.auth))
      .catch(() => setSummary(null));
  }, []);
  return (
    <div className="card">
      <p>이 계정의 역할은 2단계 인증이 필수입니다. 인증 앱을 등록하면 업무 화면으로 이동합니다.</p>
      {summary && <TotpPanel summary={{ ...summary, totpRequired: true }} onChanged={refresh} />}
    </div>
  );
}

export function SetupPage({ setup }: { setup: string }) {
  const { refresh } = useAuth();
  async function logout() {
    try {
      await api.post("/api/auth/logout");
    } finally {
      await refresh();
    }
  }
  return (
    <main className="auth-box">
      <h1>{setup === "password_change" ? "비밀번호를 바꿔 주세요" : "2단계 인증을 등록해 주세요"}</h1>
      {setup === "password_change" ? <PasswordChangeForm /> : setup === "totp_enroll" ? <TotpEnrollStep /> : <Notice tone="warn">필요한 설정을 마칠 때까지 다른 화면은 열 수 없습니다.</Notice>}
      <p>
        <button type="button" className="btn-secondary" onClick={logout}>
          로그아웃
        </button>
      </p>
    </main>
  );
}
