// 회사 설정 화면(B34 단계 1): 회사 정보, 로고·직인 이미지. 관리자만 바꾼다(company:manage). 출력 문서(발주서·결재 문서)가 이 값을 쓴다.
import { useRef, useState, type FormEvent } from "react";
import { api, apiFetch } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Notice } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { AccountingSection, CalendarSection } from "./AccountingSections";

interface Company {
  name: string;
  businessNo: string;
  representative: string;
  address: string;
  phone: string;
  hasLogo: boolean;
  hasSeal: boolean;
  configured: boolean;
  version: number;
}

/** 브라우저 기본 영어 문구 대신 한국어 버튼을 쓴다. 입력은 숨기고 버튼이 연다. */
function ImagePick({ label, disabled, onPick }: { label: string; disabled: boolean; onPick: (f: File) => void }) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <div>
      <input ref={input} type="file" hidden tabIndex={-1} aria-label={`${label} 올리기`} accept="image/png,image/jpeg" disabled={disabled} onChange={(e) => e.target.files?.[0] && onPick(e.target.files[0])} />
      <button type="button" className="btn-secondary btn-sm" disabled={disabled} onClick={() => input.current?.click()}>
        {label} 파일 선택
      </button>
    </div>
  );
}

export function CompanyPage() {
  const { can } = useAuth();
  const manage = can("company:manage");
  const load = useLoad(() => api.get<Company>("/api/company"), []);
  // 입력 중인 값(null 이면 서버에 저장된 값을 보인다). 서버 값을 effect 로 복사하면 늦게 도는 effect 가 사용자의 입력을 덮어쓸 수 있어 파생값으로 둔다.
  type Form = { name: string; businessNo: string; representative: string; address: string; phone: string };
  const [draft, setDraft] = useState<Form | null>(null);
  const [msg, setMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [stamp, setStamp] = useState(0); // 이미지 교체 뒤 미리보기를 다시 받는다
  const c = load.data;

  const f: Form = draft ?? { name: c?.name ?? "", businessNo: c?.businessNo ?? "", representative: c?.representative ?? "", address: c?.address ?? "", phone: c?.phone ?? "" };
  const setF = (next: Form) => setDraft(next);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      await api.put("/api/company", { ...f, version: c?.version ?? 0 });
      setMsg({ tone: "ok", text: "회사 정보를 저장했습니다. 이후 열어 보는 발주서·결재 문서 출력에 반영됩니다." });
      await load.reload();
      setDraft(null);
    } catch (er) {
      setMsg({ tone: "error", text: errorText(er) });
    } finally {
      setBusy(false);
    }
  }

  async function upload(kind: "logo" | "seal", file: File) {
    setBusy(true);
    setMsg(null);
    try {
      const fd = new FormData();
      fd.append("file", file);
      await apiFetch(`/api/company/${kind}`, { method: "PUT", body: fd });
      setMsg({ tone: "ok", text: kind === "logo" ? "로고를 교체했습니다. 이전 이미지는 이력으로 남습니다." : "직인을 교체했습니다. 이전 이미지는 이력으로 남습니다." });
      setStamp((s) => s + 1);
      await load.reload();
    } catch (er) {
      setMsg({ tone: "error", text: errorText(er) });
    } finally {
      setBusy(false);
    }
  }

  if (load.error) return <Notice tone="error">{load.error}</Notice>;
  if (!c) return <p>불러오는 중…</p>;
  const field = (id: string, label: string, key: keyof typeof f, hint?: string) => (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <input id={id} value={f[key]} disabled={!manage} onChange={(e) => setF({ ...f, [key]: e.target.value })} />
      {hint && <span className="hint">{hint}</span>}
    </div>
  );

  return (
    <section>
      <h1>회사 설정</h1>
      {msg && <Notice tone={msg.tone === "ok" ? "info" : "error"}>{msg.text}</Notice>}
      {!c.configured && <Notice tone="info">아직 회사 정보를 저장하지 않았습니다. 저장하기 전에는 조직도의 최상위 조직 이름을 발주처로 씁니다.</Notice>}
      <h2>회사 정보</h2>
      <form onSubmit={(e) => void save(e)}>
        {field("co-name", "회사명", "name", "필수입니다. 발주서·결재 문서 출력의 머리글과 발주처에 쓰입니다.")}
        {field("co-biz", "사업자등록번호", "businessNo", "000-00-00000 형식")}
        {field("co-rep", "대표자", "representative")}
        {field("co-addr", "주소", "address")}
        {field("co-phone", "대표 전화", "phone")}
        {manage && (
          <button type="submit" disabled={busy || f.name.trim() === ""}>
            저장
          </button>
        )}
        <p className="hint">출력은 열 때의 현재 회사 정보로 그려집니다. 이미 인쇄한 문서는 바뀌지 않습니다.</p>
      </form>

      <h2>로고·직인</h2>
      <p className="hint">PNG 또는 JPG 이미지, 512KB 이하, 가로·세로 2,000픽셀 이하. 직인은 출력 문서에만 쓰이고 앱 안 다른 화면에는 나오지 않습니다.</p>
      <div style={{ display: "flex", gap: 32, flexWrap: "wrap" }}>
        {(
          [
            ["logo", "로고", c.hasLogo],
            ["seal", "직인", c.hasSeal],
          ] as const
        ).map(([kind, label, has]) => (
          <div key={kind} className="field">
            <strong>{label}</strong>
            <div>{has ? <img src={`/api/company/${kind}?v=${stamp}`} alt={`현재 ${label}`} style={{ maxHeight: 96, maxWidth: 160 }} /> : <span className="hint">등록된 {label}이 없습니다.</span>}</div>
            {manage && (
              <>
                <ImagePick label={label} disabled={busy} onPick={(file) => void upload(kind, file)} />
              </>
            )}
          </div>
        ))}
      </div>
      {!manage && <Notice tone="info">회사 설정은 관리자만 바꿀 수 있습니다.</Notice>}
      <AccountingSection />
      <CalendarSection />
    </section>
  );
}
