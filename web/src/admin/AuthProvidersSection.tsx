// 로그인 방식 카드(구글·MS·SAML)와 로그인 정책(비밀번호 정책·2단계 필수 역할·이메일 로그인·비상 관리자). 비밀값 입력칸은 없다.
import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import { parseAuthError } from "../auth/authApi";
import { Badge, Notice } from "../layout/ui";
import { PERMISSION_LABELS } from "./auditLabels";

export interface ProviderView {
  provider: "google" | "microsoft" | "saml";
  enabled: boolean;
  envReady: boolean;
  envProblem?: string;
  configProblem: string;
  testedAt: string | null;
  allowedDomains: string[];
  tenantId: string;
  displayName: string;
  redirectUri?: string;
  acsUrl?: string;
  entityId?: string;
  idpEntityId?: string;
  idpCertFingerprints?: string[];
  idpCertNotAfter?: string;
  linkedUsers: number;
}

export interface PolicyView {
  password: { minLength: number; maxLength: number; requireClasses: number; historyCount: number; expireDays: number; banCommon: boolean };
  emailLoginEnabled: boolean;
  totpRequiredPerms: string[];
  emergencyAdmins: number[];
}

interface ProvidersResponse {
  providers: ProviderView[];
  policy: PolicyView;
  mailEnabled: boolean;
  totpAvailable: boolean;
  serverTime?: string;
  recentEmergencyLogin?: string;
}

interface TestResult {
  ok: boolean;
  summary: Record<string, string>;
  createdAt: string;
}

const TITLES: Record<string, string> = { google: "구글 워크스페이스", microsoft: "마이크로소프트 Entra ID", saml: "SAML 2.0" };
/** 2단계 인증 필수 대상으로 고를 수 있는 권한(위험 권한). 이 권한을 실효 권한으로 가진 사람이 필수 대상이다. */
const TOTP_PERMS = ["role:manage", "auth:manage", "audit:verify", "user:manage", "mask:bypass", "retention:dispose", "llm:policy_manage", "ledger:write", "payment:write"];
const EXPIRY_WARN_DAYS = 30;

function fmt(s?: string | null): string {
  return s ? new Date(s).toLocaleString("ko-KR") : "";
}

function CopyRow({ label, value }: { label: string; value: string }) {
  return (
    <p>
      {label}: <code>{value}</code>{" "}
      <button type="button" className="btn-secondary btn-sm" aria-label={`${label} 복사`} onClick={() => void navigator.clipboard?.writeText(value)}>
        복사
      </button>
    </p>
  );
}

function ProviderCard({ v, onChanged, setError }: { v: ProviderView; onChanged: () => Promise<void>; setError: (m: string) => void }) {
  const [domains, setDomains] = useState(v.allowedDomains.join(", "));
  const [tenant, setTenant] = useState(v.tenantId);
  const [display, setDisplay] = useState(v.displayName);
  const [meta, setMeta] = useState("");
  const [result, setResult] = useState<TestResult | null>(null);
  const [note, setNote] = useState("");

  async function save(body: Record<string, unknown>, done?: string) {
    setError("");
    setNote("");
    try {
      await api.put(`/api/admin/auth/providers/${v.provider}`, body);
      if (done) setNote(done);
      await onChanged();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  async function saveSettings() {
    const body: Record<string, unknown> = { displayName: display };
    if (v.provider === "google") body.allowedDomains = domains.split(/[\s,]+/).filter(Boolean);
    if (v.provider === "microsoft") body.tenantId = tenant;
    if (v.provider === "saml" && meta.trim()) body.idpMetadata = meta;
    await save(body, "설정을 저장했습니다. 켜기 전에 연결 시험을 하세요.");
    setMeta("");
  }

  async function startTest() {
    setError("");
    setNote("");
    try {
      const r = await api.post<{ url: string }>(`/api/admin/auth/providers/${v.provider}/test`);
      window.open(r.url, "_blank", "noopener");
      setNote("새 창에서 관리자 본인 계정으로 로그인하세요. 끝나면 '시험 결과 보기'를 누르세요.");
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  async function loadResult() {
    setError("");
    try {
      const r = await api.get<{ result: TestResult | null }>(`/api/admin/auth/providers/${v.provider}/test-result`);
      setResult(r.result);
      await onChanged();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }

  const certSoon =
    v.idpCertNotAfter !== undefined && new Date(v.idpCertNotAfter).getTime() - Date.now() < EXPIRY_WARN_DAYS * 86400000;
  const locked = v.enabled; // 켜져 있는 동안은 설정을 바꿀 수 없다

  return (
    <section className="card" aria-labelledby={`prov-${v.provider}`}>
      <h3 id={`prov-${v.provider}`}>{TITLES[v.provider]}</h3>
      <p>
        {v.enabled ? <Badge tone="ok">켜짐</Badge> : <Badge tone="muted">꺼짐</Badge>}{" "}
        {v.envReady ? <Badge tone="ok">환경변수 설정됨</Badge> : <Badge tone="warn">환경변수 없음</Badge>}{" "}
        {v.testedAt ? <Badge tone="ok">시험 성공 {fmt(v.testedAt)}</Badge> : <Badge tone="muted">시험 안 함</Badge>}{" "}
        <Badge tone="info">연결된 사용자 {v.linkedUsers}명</Badge>
      </p>
      {!v.envReady && (
        <Notice tone="warn">
          {v.provider === "saml"
            ? v.envProblem || "서버에 SAML SP 인증서·키 경로(PL_SAML_SP_CERT_PATH·PL_SAML_SP_KEY_PATH)가 설정되지 않았습니다."
            : `서버 환경변수(클라이언트 ID·비밀)가 설정되지 않았습니다. 비밀값은 이 화면에서 입력하지 않습니다.`}
        </Notice>
      )}
      {v.configProblem && <p className="hint">설정 필요: {v.configProblem}</p>}
      {certSoon && <Notice tone="warn">IdP 서명 인증서가 30일 안에 만료됩니다({fmt(v.idpCertNotAfter)}). 새 메타데이터로 교체하세요.</Notice>}

      {v.redirectUri && <CopyRow label="리디렉션 URI" value={v.redirectUri} />}
      {v.entityId && <CopyRow label="SP entityID" value={v.entityId} />}
      {v.acsUrl && <CopyRow label="ACS URL" value={v.acsUrl} />}
      {v.provider === "saml" && (
        <p>
          <a href="/api/auth/saml/metadata" download="procure-ledger-sp-metadata.xml">
            SP 메타데이터 내려받기
          </a>
        </p>
      )}

      <div className="field">
        <label htmlFor={`name-${v.provider}`}>로그인 화면 버튼 이름</label>
        <input id={`name-${v.provider}`} value={display} disabled={locked} maxLength={40} onChange={(e) => setDisplay(e.target.value)} />
      </div>
      {v.provider === "google" && (
        <div className="field">
          <label htmlFor="g-domains">허용 도메인(쉼표로 구분, 필수)</label>
          <input id="g-domains" value={domains} disabled={locked} onChange={(e) => setDomains(e.target.value)} placeholder="corp.example" />
        </div>
      )}
      {v.provider === "microsoft" && (
        <div className="field">
          <label htmlFor="ms-tenant">허용 테넌트 ID(GUID, 필수)</label>
          <input id="ms-tenant" value={tenant} disabled={locked} onChange={(e) => setTenant(e.target.value)} />
          <span className="hint">단일 테넌트만 허용합니다. common·organizations 는 쓸 수 없습니다.</span>
        </div>
      )}
      {v.provider === "saml" && (
        <div className="field">
          <label htmlFor="saml-meta">IdP 메타데이터(XML 붙여넣기, 최대 256KiB)</label>
          <textarea id="saml-meta" rows={5} value={meta} disabled={locked} onChange={(e) => setMeta(e.target.value)} placeholder="<EntityDescriptor …>" />
          {v.idpEntityId && (
            <span className="hint">
              등록됨: {v.idpEntityId} · 인증서 지문 {(v.idpCertFingerprints ?? []).map((f) => f.slice(0, 16)).join(", ")} · 만료 {fmt(v.idpCertNotAfter)}
            </span>
          )}
        </div>
      )}
      {locked && <p className="hint">켜져 있는 동안은 설정을 바꿀 수 없습니다. 먼저 끄세요.</p>}

      {note && <Notice tone="ok">{note}</Notice>}
      {result && (
        <div role="status" className={`notice notice-${result.ok ? "ok" : "warn"}`}>
          <p>
            <strong>시험 {result.ok ? "성공" : "실패"}</strong> ({fmt(result.createdAt)})
          </p>
          <ul>
            {Object.entries(result.summary).map(([k, val]) => (
              <li key={k}>
                {k}: {val}
              </li>
            ))}
          </ul>
        </div>
      )}
      <p>
        <button type="button" disabled={locked} onClick={() => void saveSettings()}>
          설정 저장
        </button>{" "}
        <button type="button" className="btn-secondary" disabled={!v.envReady || !!v.configProblem} onClick={() => void startTest()}>
          연결 시험
        </button>{" "}
        <button type="button" className="btn-secondary" onClick={() => void loadResult()}>
          시험 결과 보기
        </button>{" "}
        {v.enabled ? (
          <button type="button" className="btn-danger" onClick={() => void save({ enabled: false }, "방식을 껐습니다.")}>
            끄기
          </button>
        ) : (
          <button type="button" disabled={!v.testedAt} title={v.testedAt ? "" : "연결 시험에 성공해야 켤 수 있습니다"} onClick={() => void save({ enabled: true }, "방식을 켰습니다. 로그인 화면에 보입니다.")}>
            켜기
          </button>
        )}
      </p>
    </section>
  );
}

function PolicyCard({ data, adminUsers, onChanged, setError }: { data: ProvidersResponse; adminUsers: { id: number; name: string }[]; onChanged: () => Promise<void>; setError: (m: string) => void }) {
  const p = data.policy;
  const [minLength, setMinLength] = useState(p.password.minLength);
  const [classes, setClasses] = useState(p.password.requireClasses);
  const [history, setHistory] = useState(p.password.historyCount);
  const [expire, setExpire] = useState(p.password.expireDays);
  const [roles, setRoles] = useState<string[]>(p.totpRequiredPerms ?? []);
  const [emergency, setEmergency] = useState<number[]>(p.emergencyAdmins ?? []);
  const [note, setNote] = useState("");

  async function save(body: Record<string, unknown>, done: string) {
    setError("");
    setNote("");
    try {
      await api.put("/api/admin/auth/policy", body);
      setNote(done);
      await onChanged();
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }
  const toggle = <T,>(list: T[], v: T) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

  return (
    <section className="card" aria-labelledby="auth-policy-title">
      <h3 id="auth-policy-title">이메일 로그인과 정책</h3>
      <p>
        이메일 로그인 {p.emailLoginEnabled ? <Badge tone="ok">켜짐</Badge> : <Badge tone="warn">꺼짐(외부 로그인만)</Badge>}{" "}
        메일 발송 {data.mailEnabled ? <Badge tone="ok">설정됨</Badge> : <Badge tone="muted">설정 안 됨</Badge>}{" "}
        2단계 인증 {data.totpAvailable ? <Badge tone="ok">사용 가능</Badge> : <Badge tone="warn">PL_AUTH_SECRET_KEY 없음</Badge>}
        {data.serverTime && <span className="hint"> · 서버 시각 {fmt(data.serverTime)}</span>}
      </p>
      {data.recentEmergencyLogin && <Notice tone="warn">최근 비상 로그인이 사용되었습니다({fmt(data.recentEmergencyLogin)}). 감사 로그에서 확인하세요.</Notice>}

      <h4>비밀번호 정책</h4>
      <div className="form-row">
        <label>
          최소 길이
          <input type="number" min={8} max={128} value={minLength} onChange={(e) => setMinLength(Number(e.target.value))} />
        </label>
        <label>
          문자 종류 최소 개수(0=검사 안 함)
          <input type="number" min={0} max={4} value={classes} onChange={(e) => setClasses(Number(e.target.value))} />
        </label>
        <label>
          재사용 금지 개수
          <input type="number" min={0} max={10} value={history} onChange={(e) => setHistory(Number(e.target.value))} />
        </label>
        <label>
          만료
          <select value={expire} onChange={(e) => setExpire(Number(e.target.value))}>
            <option value={0}>끔(권장)</option>
            <option value={60}>60일</option>
            <option value={90}>90일</option>
            <option value={180}>180일</option>
          </select>
        </label>
        <button
          type="button"
          onClick={() =>
            void save(
              { password: { minLength, maxLength: Math.max(p.password.maxLength, minLength), requireClasses: classes, historyCount: history, expireDays: expire, banCommon: p.password.banCommon } },
              "비밀번호 정책을 저장했습니다. 이미 만든 비밀번호에는 소급하지 않습니다.",
            )
          }
        >
          정책 저장
        </button>
      </div>

      <h4>2단계 인증 필수 권한</h4>
      <p className="hint">아래 권한을 하나라도 가진 사람(역할 이름과 상관없이)은 2단계 인증을 등록해야 로그인할 수 있습니다.</p>
      <p className="chip-row">
        {TOTP_PERMS.map((r) => (
          <label key={r}>
            <input type="checkbox" checked={roles.includes(r)} onChange={() => setRoles(toggle(roles, r))} /> {PERMISSION_LABELS[r] ?? r}{" "}
          </label>
        ))}
        <button type="button" className="btn-secondary btn-sm" onClick={() => void save({ totpRequiredPerms: roles }, "필수 권한을 저장했습니다. 미등록 사용자는 다음 로그인에서 등록하게 됩니다.")}>
          저장
        </button>
      </p>

      <h4>비상 관리자(1~3명)</h4>
      <p className="hint">이메일 로그인을 꺼도 비상 로그인(/login/emergency)으로 들어올 수 있는 관리자입니다. 2단계 인증 등록이 필수이고 사용할 때마다 감사 로그에 경고로 남습니다.</p>
      <p>
        {adminUsers.map((a) => (
          <label key={a.id} className="nowrap">
            <input type="checkbox" checked={emergency.includes(a.id)} onChange={() => setEmergency(toggle(emergency, a.id))} /> {a.name}{" "}
          </label>
        ))}
        <button type="button" className="btn-secondary btn-sm" onClick={() => void save({ emergencyAdmins: emergency }, "비상 관리자를 저장했습니다.")}>
          저장
        </button>
      </p>

      <h4>이메일 로그인 켜기·끄기</h4>
      <p className="hint">
        끄려면 시험에 성공한 외부 로그인 방식이 켜져 있고, 그 방식으로 연결된 활성 관리자가 있고, 비상 관리자가 지정되어 2단계 인증 등록을 마쳐야 합니다. 모자란 것은 저장할 때 알려 드립니다.
      </p>
      {note && <Notice tone="ok">{note}</Notice>}
      {p.emailLoginEnabled ? (
        <button type="button" className="btn-danger" onClick={() => void save({ emailLoginEnabled: false }, "이메일 로그인을 껐습니다. 비상 로그인만 남습니다.")}>
          이메일 로그인 끄기
        </button>
      ) : (
        <button type="button" onClick={() => void save({ emailLoginEnabled: true }, "이메일 로그인을 켰습니다.")}>
          이메일 로그인 켜기
        </button>
      )}
    </section>
  );
}

export function ProvidersSection({ adminUsers, refreshKey }: { adminUsers: { id: number; name: string }[]; refreshKey: number }) {
  const [data, setData] = useState<ProvidersResponse | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await api.get<ProvidersResponse>("/api/admin/auth/providers");
      setData(r.providers ? r : null);
    } catch (e) {
      setError(parseAuthError(e).message);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  if (!data) return error ? <Notice tone="error">{error}</Notice> : null;
  // 설정이 바뀌면 입력 상태를 서버 값으로 다시 맞추려고 key 에 저장 시각 대신 값 요약을 쓴다.
  const sig = (v: ProviderView) => [v.provider, v.enabled, v.allowedDomains.join(","), v.tenantId, v.displayName, v.idpEntityId].join("|");
  return (
    <section aria-labelledby="auth-prov-title">
      <h2 id="auth-prov-title">로그인 방식</h2>
      {error && <Notice tone="error">{error}</Notice>}
      <p className="hint">
        기본 방식은 이메일+비밀번호입니다. 아래 외부 방식은 연결 시험에 성공한 뒤에만 켤 수 있고, 켜면 로그인 화면에 버튼이 생깁니다. 외부 로그인은 계정을 만들지 않습니다(등록된 재직자의 이메일에만 연결).
      </p>
      <div className="card-grid">
        {data.providers.map((v) => (
          <ProviderCard key={sig(v)} v={v} onChanged={load} setError={setError} />
        ))}
      </div>
      <PolicyCard key={JSON.stringify(data.policy)} data={data} adminUsers={adminUsers} onChanged={load} setError={setError} />
    </section>
  );
}
