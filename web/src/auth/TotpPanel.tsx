// 2단계 인증(TOTP) 등록·해제 패널. 내 계정 화면과 제한 세션(필수 역할의 첫 등록) 화면이 같이 쓴다.
// 비밀키는 텍스트·otpauth 링크로 보여 준다(QR 이미지는 후속). 복구 코드는 확인 응답에서 한 번만 보인다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { parseAuthError } from "./authApi";

export interface AuthSummary {
  method: string;
  hasPassword: boolean;
  totpAvailable: boolean;
  totpEnrolled: boolean;
  totpRequired: boolean;
}

export function TotpPanel({ summary, onChanged }: { summary: AuthSummary; onChanged: () => void | Promise<void> }) {
  const [enroll, setEnroll] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [recovery, setRecovery] = useState<string[] | null>(null);
  const [error, setError] = useState("");
  const [info, setInfo] = useState("");

  async function begin(e?: FormEvent) {
    e?.preventDefault();
    setError("");
    try {
      const body = summary.totpEnrolled ? { password, code } : {};
      setEnroll(await api.post<{ secret: string; uri: string }>("/api/auth/totp/enroll", body));
      setCode("");
    } catch (err) {
      setError(parseAuthError(err).message);
    }
  }

  async function confirm(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      const r = await api.post<{ recoveryCodes: string[] }>("/api/auth/totp/confirm", { code });
      setRecovery(r.recoveryCodes);
      setEnroll(null);
      setCode("");
    } catch (err) {
      setError(parseAuthError(err).message);
    }
  }

  async function disable(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await api.post("/api/auth/totp/disable", { password, code });
      setInfo("2단계 인증을 해제했습니다.");
      setPassword("");
      setCode("");
      await onChanged();
    } catch (err) {
      setError(parseAuthError(err).message);
    }
  }

  if (!summary.totpAvailable) {
    return <Notice tone="warn">이 서버는 2단계 인증이 설정되지 않았습니다(관리자: PL_AUTH_SECRET_KEY).</Notice>;
  }

  return (
    <div>
      {error && <Notice tone="error">{error}</Notice>}
      {info && <Notice tone="ok">{info}</Notice>}
      {recovery && (
        <div role="status" className="notice notice-warn">
          <p>
            <strong>복구 코드 8개는 지금만 볼 수 있습니다.</strong> 인증 앱을 잃어버렸을 때 한 개씩 한 번만 쓸 수 있습니다. 안전한 곳에 보관하세요.
          </p>
          <ul data-testid="recovery-codes">
            {recovery.map((c) => (
              <li key={c}>
                <code>{c}</code>
              </li>
            ))}
          </ul>
          <button
            type="button"
            onClick={async () => {
              setRecovery(null);
              await onChanged();
            }}
          >
            보관했습니다
          </button>
        </div>
      )}
      {!recovery && enroll && (
        <form onSubmit={confirm} aria-label="2단계 인증 등록">
          <p>인증 앱(Google Authenticator, Microsoft Authenticator 등)에 아래 키를 직접 입력하거나 링크를 열고, 앱이 보여 주는 6자리 코드를 입력하세요.</p>
          <p>
            키: <code data-testid="totp-secret">{enroll.secret}</code>
          </p>
          <p className="hint">
            <a href={enroll.uri}>인증 앱으로 열기(otpauth 링크)</a>
          </p>
          <div className="field">
            <label htmlFor="totp-code">인증 앱의 6자리 코드</label>
            <input id="totp-code" inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} required />
          </div>
          <button type="submit">확인하고 켜기</button>
        </form>
      )}
      {!recovery && !enroll && !summary.totpEnrolled && (
        <p>
          <button type="button" onClick={() => void begin()}>
            2단계 인증 시작
          </button>
        </p>
      )}
      {!recovery && !enroll && summary.totpEnrolled && (
        <form onSubmit={disable} aria-label="2단계 인증 변경">
          <p>
            <strong>2단계 인증을 쓰고 있습니다.</strong> 기기를 바꾸거나 해제하려면 현재 비밀번호와 인증 앱의 코드가 필요합니다.
          </p>
          <div className="field">
            <label htmlFor="totp-pw">현재 비밀번호</label>
            <input id="totp-pw" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="totp-cur-code">인증 앱의 6자리 코드</label>
            <input id="totp-cur-code" inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} required />
          </div>
          <button type="button" className="btn-secondary" onClick={() => void begin()}>
            새 기기로 다시 등록
          </button>{" "}
          {!summary.totpRequired && (
            <button type="submit" className="btn-danger">
              해제
            </button>
          )}
          {summary.totpRequired && <p className="hint">이 역할은 2단계 인증이 필수라 해제할 수 없습니다.</p>}
        </form>
      )}
    </div>
  );
}
