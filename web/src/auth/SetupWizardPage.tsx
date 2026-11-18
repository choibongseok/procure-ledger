// 설치 마법사 화면(B38 명세 4절). 빈 DB 첫 기동 때만 서버가 연다(그 밖에는 /api/setup/status 가 404).
// 설치 코드는 서버 로그에만 나온다. 화면에 입력한 값은 이 화면의 메모리에만 있다가 마지막 "설치 완료"에서 한 번에 서버로 간다
// (도중에 닫아도 서버에 반쪽 상태가 없다). 이 화면은 로그인 전 공개 경로라 api 클라이언트(CSRF·401 이동) 대신 fetch 를 직접 쓴다.
import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Notice } from "../layout/ui";

const CODE_HEADER = "X-Setup-Code";

interface Fields {
  companyName: string;
  businessNo: string;
  representative: string;
  address: string;
  phone: string;
  fiscalStartMonth: number;
  adminName: string;
  adminEmail: string;
  adminPassword: string;
  adminPassword2: string;
  department: string;
  demo: boolean;
  demoConfirm: string;
}

const EMPTY: Fields = {
  companyName: "",
  businessNo: "",
  representative: "",
  address: "",
  phone: "",
  fiscalStartMonth: 1,
  adminName: "",
  adminEmail: "",
  adminPassword: "",
  adminPassword2: "",
  department: "",
  demo: false,
  demoConfirm: "",
};

/** 서버 필드 이름 → 단계(어느 단계에서 고쳐야 하는지). */
const FIELD_STEP: Record<string, number> = { company: 1, fiscalStartMonth: 1, name: 2, email: 2, password: 2, department: 2, demoConfirm: 4, admin: 2 };
const STEPS = ["설치 코드", "회사 정보", "첫 관리자", "로그인 방식", "데모 데이터", "확인"];

interface Resp {
  ok: boolean;
  status: number;
  body: { error?: string; errors?: Record<string, string>; totpKeyConfigured?: boolean; ok?: boolean; demoFailed?: boolean };
}

async function call(path: string, code: string, body: unknown): Promise<Resp> {
  try {
    const res = await fetch(path, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", [CODE_HEADER]: code }, body: JSON.stringify(body ?? {}) });
    let b: Resp["body"] = {};
    try {
      b = await res.json();
    } catch {
      /* 본문 없음 */
    }
    return { ok: res.ok, status: res.status, body: b };
  } catch {
    return { ok: false, status: 0, body: { error: "서버에 연결하지 못했습니다. 네트워크를 확인하고 다시 시도해 주세요." } };
  }
}

export function SetupWizardPage() {
  const [state, setState] = useState<"loading" | "pending" | "closed">("loading");
  const [step, setStep] = useState(0);
  const [code, setCode] = useState("");
  const [f, setF] = useState<Fields>(EMPTY);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [totpKey, setTotpKey] = useState(false);
  const [done, setDone] = useState(false);
  const [demoFailed, setDemoFailed] = useState(false);
  const [providers, setProviders] = useState<{ id: string; label: string }[]>([]);

  useEffect(() => {
    let alive = true;
    fetch("/api/setup/status", { credentials: "same-origin" })
      .then((r) => alive && setState(r.ok ? "pending" : "closed"))
      .catch(() => alive && setState("closed"));
    fetch("/api/auth/config", { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((c) => alive && c && setProviders(c.providers ?? []))
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);

  const set = <K extends keyof Fields>(k: K, v: Fields[K]) => {
    setF((p) => ({ ...p, [k]: v }));
    setErrors({}); // 고치는 중에는 이전 검사 문구를 지운다(다음 단계로 넘어갈 때 다시 검사한다)
  };
  const payload = () => ({ ...f, adminPassword2: undefined });

  if (state === "loading") return <p className="auth-box">불러오는 중…</p>;
  if (state === "closed") {
    return (
      <main className="auth-box">
        <h1>초기 설정</h1>
        <p>초기 설정이 이미 끝났거나 이 서버에서는 필요하지 않습니다.</p>
        <p>
          <Link to="/login">로그인 화면으로</Link>
        </p>
      </main>
    );
  }
  if (done) {
    return (
      <main className="auth-box">
        <h1>설치가 끝났습니다</h1>
        <Notice tone="ok">방금 정한 이메일과 비밀번호로 로그인하세요. 설치 코드는 폐기되었고 이 설정 화면은 다시 열리지 않습니다.</Notice>
        {demoFailed && (
          <Notice tone="warn">
            설치는 끝났지만 데모 데이터를 넣지 못했습니다. 서버 로그에서 원인을 확인하세요. 데모 데이터가 일부만 들어갔다면 새 DB 로 다시 설치해야 하고, 아무것도 들어가지 않았다면 서버 호스트에서 erp-admin demo-seed 로 다시 넣을 수 있습니다.
          </Notice>
        )}
        <p>
          <a className="btn" href="/login">
            로그인
          </a>
        </p>
      </main>
    );
  }

  async function checkCode(e: FormEvent) {
    e.preventDefault();
    setNotice("");
    setBusy(true);
    const r = await call("/api/setup/check", code, {});
    setBusy(false);
    if (r.status === 404) return setState("closed");
    if (!r.ok) return setNotice(r.status === 429 ? "설치 코드를 여러 번 틀려 잠시 입력할 수 없습니다. 15분쯤 뒤에 다시 시도하세요." : (r.body.error ?? "설치 코드를 확인하지 못했습니다."));
    setTotpKey(!!r.body.totpKeyConfigured);
    setStep(1);
  }

  async function validate(next: number) {
    setNotice("");
    if (f.adminPassword !== f.adminPassword2 && step === 2) {
      setErrors({ password2: "비밀번호 두 번이 서로 다릅니다." });
      return;
    }
    setBusy(true);
    const r = await call("/api/setup/validate", code, payload());
    setBusy(false);
    if (r.status === 404) return setState("closed");
    if (!r.ok) return setNotice(r.body.error ?? "입력을 확인하지 못했습니다.");
    const errs = r.body.errors ?? {};
    // 지금 단계 이하의 오류만 막고, 뒤 단계 오류는 그 단계에 가서 고치게 한다.
    const here = Object.entries(errs).filter(([k]) => (FIELD_STEP[k] ?? 5) <= step);
    setErrors(Object.fromEntries(here)); // 지금 단계의 오류만 보인다(뒤 단계의 아직 비어 있는 칸을 미리 나무라지 않는다)
    if (here.length > 0) return;
    setStep(next);
  }

  async function complete() {
    setNotice("");
    setBusy(true);
    const r = await call("/api/setup/complete", code, payload());
    setBusy(false);
    if (r.status === 404) return setState("closed");
    if (r.status === 409) return setNotice("이미 초기 설정이 끝났습니다. 로그인 화면으로 이동하세요.");
    if (r.status === 422) {
      const errs = r.body.errors ?? {};
      setErrors(errs);
      const first = Math.min(...Object.keys(errs).map((k) => FIELD_STEP[k] ?? 5), 5);
      setStep(first);
      return setNotice("입력을 확인하세요.");
    }
    if (r.status === 429 || r.status === 401) return setNotice(r.body.error ?? "설치 코드를 확인하세요.");
    if (!r.ok) return setNotice(r.body.error ?? "처리 중 오류가 발생했습니다. 잠시 후 다시 시도해 주세요.");
    setDemoFailed(r.body.demoFailed === true);
    setDone(true);
  }

  const err = (k: string) => (errors[k] ? <span className="hint" role="alert">{errors[k]}</span> : null);
  const nav = (back: number, next: number | "complete") => (
    <p className="form-actions">
      {back >= 0 && (
        <button type="button" className="btn-secondary" disabled={busy} onClick={() => setStep(back)}>
          이전
        </button>
      )}{" "}
      {next === "complete" ? (
        <button type="button" disabled={busy} onClick={complete}>
          설치 완료
        </button>
      ) : (
        <button type="button" disabled={busy} onClick={() => validate(next)}>
          다음
        </button>
      )}
    </p>
  );

  return (
    <main className="auth-box">
      <h1>초기 설정</h1>
      <p className="hint" aria-label="진행 단계">
        {step + 1} / {STEPS.length} 단계: {STEPS[step]}
      </p>
      {notice && <Notice tone="error">{notice}</Notice>}

      {step === 0 && (
        <form onSubmit={checkCode} aria-label="설치 코드 확인" className="card">
          <p>서버를 시작할 때 서버 로그(콘솔)에 출력된 설치 코드를 입력하세요. 서버를 운영하는 사람만 첫 관리자를 정할 수 있습니다.</p>
          <div className="field">
            <label htmlFor="setup-code">설치 코드</label>
            <input id="setup-code" autoComplete="off" spellCheck={false} value={code} onChange={(e) => setCode(e.target.value)} required />
          </div>
          <button type="submit" disabled={busy}>
            확인
          </button>
        </form>
      )}

      {step === 1 && (
        <section aria-label="회사 정보" className="card">
          <h2>회사 정보</h2>
          {errors.company && <Notice tone="error">{errors.company}</Notice>}
          <div className="field">
            <label htmlFor="s-company">회사명</label>
            <input id="s-company" value={f.companyName} onChange={(e) => set("companyName", e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="s-bno">사업자등록번호 (선택)</label>
            <input id="s-bno" placeholder="000-00-00000" value={f.businessNo} onChange={(e) => set("businessNo", e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="s-rep">대표자 (선택)</label>
            <input id="s-rep" value={f.representative} onChange={(e) => set("representative", e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="s-addr">주소 (선택)</label>
            <input id="s-addr" value={f.address} onChange={(e) => set("address", e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="s-phone">대표 전화 (선택)</label>
            <input id="s-phone" value={f.phone} onChange={(e) => set("phone", e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="s-fy">회계연도 시작월</label>
            <select id="s-fy" value={f.fiscalStartMonth} onChange={(e) => set("fiscalStartMonth", Number(e.target.value))}>
              {Array.from({ length: 12 }, (_, i) => i + 1).map((m) => (
                <option key={m} value={m}>
                  {m}월
                </option>
              ))}
            </select>
            {err("fiscalStartMonth")}
            <span className="hint">시간대는 한국 표준시(KST)로 표시됩니다. 로고·직인은 설치 뒤 회사 설정에서 올립니다.</span>
          </div>
          {nav(0, 2)}
        </section>
      )}

      {step === 2 && (
        <section aria-label="첫 관리자" className="card">
          <h2>첫 관리자</h2>
          <p className="hint">이 사람이 관리자 역할과 비상 관리자로 지정됩니다. 이메일과 비밀번호로 로그인합니다.</p>
          <div className="field">
            <label htmlFor="s-name">이름</label>
            <input id="s-name" autoComplete="name" value={f.adminName} onChange={(e) => set("adminName", e.target.value)} required />
            {err("name")}
          </div>
          <div className="field">
            <label htmlFor="s-email">이메일</label>
            <input id="s-email" type="email" autoComplete="username" value={f.adminEmail} onChange={(e) => set("adminEmail", e.target.value)} required />
            {err("email")}
          </div>
          <div className="field">
            <label htmlFor="s-pw">비밀번호</label>
            <input id="s-pw" type="password" autoComplete="new-password" value={f.adminPassword} onChange={(e) => set("adminPassword", e.target.value)} required />
            {err("password")}
            <span className="hint">10자 이상, 영문 대·소문자·숫자·기호 중 3종류 이상.</span>
          </div>
          <div className="field">
            <label htmlFor="s-pw2">비밀번호 확인</label>
            <input id="s-pw2" type="password" autoComplete="new-password" value={f.adminPassword2} onChange={(e) => set("adminPassword2", e.target.value)} required />
            {err("password2")}
          </div>
          <div className="field">
            <label htmlFor="s-dept">부서 이름 (선택)</label>
            <input id="s-dept" value={f.department} onChange={(e) => set("department", e.target.value)} />
            {err("department")}
            <span className="hint">비워 두면 "본사"로 만듭니다. 조직 구성은 설치 뒤 조직 관리와 일괄 가져오기에서 합니다.</span>
          </div>
          {nav(1, 3)}
        </section>
      )}

      {step === 3 && (
        <section aria-label="로그인 방식" className="card">
          <h2>로그인 방식</h2>
          <p>이메일 로그인은 항상 켜집니다(이 단계에서는 끌 수 없습니다).</p>
          <p>
            외부 로그인(구글·마이크로소프트·SAML·사내 IdP): {providers.length > 0 ? providers.map((p) => p.label).join(", ") + " 설정됨" : "아직 설정되지 않았습니다"}. 설치 뒤 로그인 설정 화면에서 시험한 다음 켜세요.
          </p>
          {totpKey ? (
            <Notice tone="ok">2단계 인증 키가 설정되어 있습니다.</Notice>
          ) : (
            <Notice tone="warn">관리자 2단계 인증을 쓰려면 서버에 PL_AUTH_SECRET_KEY 를 먼저 설정하세요. 설정하지 않아도 설치는 계속할 수 있습니다.</Notice>
          )}
          {nav(2, 4)}
        </section>
      )}

      {step === 4 && (
        <section aria-label="데모 데이터" className="card">
          <h2>데모 데이터</h2>
          <p>시연·학습용 샘플을 넣을 수 있습니다. 한 번 고르면 되돌릴 수 없고, 모든 화면 위에 "시연 환경" 표시가 계속 나옵니다. 실제 업무에는 넣지 않는 것을 권합니다. 데모를 고르면 첫 관리자의 부서는 "데모 주식회사"로 정해집니다(앞에서 입력한 부서 이름은 쓰이지 않습니다).</p>
          <div className="field">
            <label>
              <input type="checkbox" checked={f.demo} onChange={(e) => set("demo", e.target.checked)} /> 이 환경은 시연용으로만 쓰며 실제 업무 데이터와 섞을 수 없습니다
            </label>
          </div>
          {f.demo && (
            <div className="field">
              <label htmlFor="s-demo">확인을 위해 회사명을 똑같이 입력하세요</label>
              <input id="s-demo" value={f.demoConfirm} onChange={(e) => set("demoConfirm", e.target.value)} />
              {err("demoConfirm")}
            </div>
          )}
          {nav(3, 5)}
        </section>
      )}

      {step === 5 && (
        <section aria-label="설치 확인" className="card">
          <h2>확인</h2>
          <dl>
            <dt>회사명</dt>
            <dd>{f.companyName}</dd>
            <dt>회계연도 시작월</dt>
            <dd>{f.fiscalStartMonth}월</dd>
            <dt>첫 관리자</dt>
            <dd>
              {f.adminName} ({f.adminEmail})
            </dd>
            <dt>부서</dt>
            <dd>{f.department || "본사"}</dd>
            <dt>데모 데이터</dt>
            <dd>{f.demo ? "넣음" : "넣지 않음"}</dd>
          </dl>
          <p className="hint">설치를 완료하면 이 설정 화면은 다시 열리지 않습니다. 완료 뒤 방금 정한 이메일과 비밀번호로 로그인하세요.</p>
          {nav(4, "complete")}
        </section>
      )}
    </main>
  );
}
