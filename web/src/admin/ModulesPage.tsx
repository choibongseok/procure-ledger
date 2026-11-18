// 업무 모듈 켜기·끄기(B50 4절). 권한 attendance:manage_module(관리자, 위험 권한·세션 전용).
// 켜기 전 점검표의 필수 항목이 모두 통과해야 켤 수 있고, 끄면 메뉴·화면·AI 도구가 없는 것과 같아진다(데이터는 그대로 남는다).
import { useState } from "react";
import { api } from "../api/client";
import { Badge, ConfirmDialog, Notice, useToast } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface ModuleRow {
  key: string;
  name: string;
  description: string;
  enabled: boolean;
}
interface CheckItem {
  label: string;
  required: boolean;
  ok: boolean;
  detail: string;
}
interface HRTemplate {
  name: string;
  description: string;
  permissions: string[];
}

function ModuleCard({ m, onChanged }: { m: ModuleRow; onChanged: () => void }) {
  const toast = useToast();
  const check = useLoad(() => api.get<{ items: CheckItem[]; ready: boolean }>(`/api/admin/modules/${m.key}/checklist`), [m.key, m.enabled]);
  const tpl = useLoad(() => (m.key === "attendance" ? api.get<HRTemplate>("/api/admin/modules/attendance/hr-template") : Promise.resolve(null)), [m.key]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirmOff, setConfirmOff] = useState(false);
  const items = check.data?.items ?? [];
  const ready = check.data?.ready === true;

  async function set(enabled: boolean) {
    setBusy(true);
    setError("");
    try {
      await api.put(`/api/admin/modules/${m.key}`, { enabled });
      toast(enabled ? `「${m.name}」 모듈을 켰습니다.` : `「${m.name}」 모듈을 껐습니다. 데이터는 그대로 남아 있습니다.`);
      setConfirmOff(false);
      onChanged();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function createHRRole() {
    if (!tpl.data) return;
    setBusy(true);
    setError("");
    try {
      await api.post("/api/admin/roles", { name: tpl.data.name, description: tpl.data.description, permissions: tpl.data.permissions });
      toast(`「${tpl.data.name}」 역할을 만들었습니다. 역할 관리 화면에서 담당자에게 부여하세요.`);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-label={m.name}>
      <h2>
        {m.name} <Badge tone={m.enabled ? "ok" : "muted"}>{m.enabled ? "켜짐" : "꺼짐"}</Badge>
      </h2>
      <p>{m.description}</p>
      {error && <Notice tone="error">{error}</Notice>}
      <h3>켜기 전 점검표</h3>
      {check.error ? (
        <Notice tone="error">{check.error}</Notice>
      ) : (
        <ul className="plain-list">
          {items.map((c) => (
            <li key={c.label}>
              <Badge tone={c.ok ? "ok" : c.required ? "bad" : "warn"}>{c.ok ? "통과" : c.required ? "필수 미비" : "권고"}</Badge> {c.label}
              <span className="hint"> · {c.detail}</span>
            </li>
          ))}
        </ul>
      )}
      {m.key === "attendance" && (
        <Notice tone="warn">
          연차 발생·소멸 같은 법정 기준은 사업장 규모·취업규칙에 따라 달라, <strong>운영 전에 노무 검토가 필요합니다</strong>. 근태·휴직 사유는 민감한 개인정보일 수 있으니 취급 담당자를 최소로 지정하세요.
        </Notice>
      )}
      <div className="actions">
        {m.enabled ? (
          <button type="button" className="btn-danger" disabled={busy} onClick={() => setConfirmOff(true)}>
            끄기
          </button>
        ) : (
          <button type="button" disabled={busy || !ready} onClick={() => void set(true)}>
            켜기
          </button>
        )}
        {m.key === "attendance" && tpl.data && (
          <button type="button" className="btn-secondary" disabled={busy} onClick={() => void createHRRole()}>
            「{tpl.data.name}」 역할 만들기
          </button>
        )}
      </div>
      {!m.enabled && !ready && !check.error && <p className="hint">필수 점검 항목이 모두 통과해야 켤 수 있습니다.</p>}
      {confirmOff && (
        <ConfirmDialog title="모듈 끄기" confirmLabel="끄기" busy={busy} onConfirm={() => void set(false)} onCancel={() => setConfirmOff(false)}>
          <p>
            「{m.name}」 모듈을 끄면 메뉴·화면·AI 도구·알림이 모두 사라지고 진행 중이던 결재 문서의 잔여 일수 반영도 멈춥니다. 기록된 데이터는 지워지지 않으며 다시 켜면 이어서 쓸 수 있습니다.
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}

export function ModulesPage() {
  const res = useLoad(() => api.get<{ modules: ModuleRow[] }>("/api/admin/modules"), []);
  if (res.error) return <Notice tone="error">{res.error}</Notice>;
  if (!res.data) return <p>불러오는 중…</p>;
  return (
    <section>
      <h1>업무 모듈</h1>
      <p className="hint">회사에 필요한 업무 기능을 켜고 끕니다. 조직·결재·권한·감사는 모든 모듈이 같이 씁니다.</p>
      {res.data.modules.map((m) => (
        <ModuleCard key={m.key} m={m} onChanged={() => void res.reload()} />
      ))}
    </section>
  );
}
