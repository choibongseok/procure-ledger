// 알림 채널 관리의 기본값·설정 영역(B36 단계 3, 명세 4.3·4.8): 알림 종류별 메일 기본값과 필수 여부, 업무 시간·묶음·주기 알림 등 설정값.
// 사람마다 따로 정한 수신 설정은 보이지 않는다(본인만 본다). 기본값을 바꾸면 이후 이벤트부터 "기본값 따름" 사람에게 적용된다.
import { useState } from "react";
import { api } from "../api/client";
import { Notice, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface DefaultKind {
  kind: string;
  name: string;
  enabled: boolean;
  locked: boolean;
}

interface Setting {
  key: string;
  label: string;
  value: string;
  unit: string;
  type: "int" | "clock" | "bool";
  min?: number;
  max?: number;
  hint?: string;
}

interface Defaults {
  kinds: DefaultKind[];
  settings: Setting[];
  mailConfigured: boolean;
}

export function NotifyDefaults() {
  const load = useLoad(() => api.get<Defaults>("/api/notify/defaults"), []);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [edit, setEdit] = useState<Record<string, string>>({});
  const d = load.data && Array.isArray(load.data.kinds) ? load.data : null;

  async function run(fn: () => Promise<unknown>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      setNote({ tone: "ok", text: ok });
      await load.reload();
      return true;
    } catch (e) {
      setNote({ tone: "error", text: errorText(e) });
      return false;
    } finally {
      setBusy(false);
    }
  }

  const changed = Object.keys(edit).filter((k) => d?.settings.find((s) => s.key === k)?.value !== edit[k]);

  return (
    <section className="card" aria-labelledby="nt-defaults">
      <h2 id="nt-defaults">수신 기본값과 설정</h2>
      <p>
        사람마다 알림 종류별 메일을 켜고 끌 수 있고(내 알림 설정), 정하지 않은 사람에게는 아래 기본값이 적용됩니다. <strong>필수</strong>로 정한 알림은 끌 수 없습니다. 앱 안 알림은 항상 켜져 있습니다.
      </p>
      {load.error && <Notice tone="error">{load.error}</Notice>}
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {d && (
        <>
          <TableWrap>
            <table>
              <thead>
                <tr>
                  <th>알림</th>
                  <th>메일 기본값</th>
                  <th>필수</th>
                </tr>
              </thead>
              <tbody>
                {d.kinds.map((k) => (
                  <tr key={k.kind}>
                    <td>{k.name}</td>
                    <td>
                      <label>
                        <input
                          type="checkbox"
                          aria-label={`${k.name} 메일 기본값`}
                          checked={k.enabled}
                          disabled={busy}
                          onChange={(e) => void run(() => api.put(`/api/notify/defaults/${k.kind}`, { enabled: e.target.checked, locked: e.target.checked ? k.locked : false }), `${k.name} 기본값을 저장했습니다.`)}
                        />{" "}
                        {k.enabled ? "켜짐" : "꺼짐"}
                      </label>
                    </td>
                    <td>
                      <label>
                        <input
                          type="checkbox"
                          aria-label={`${k.name} 필수`}
                          checked={k.locked}
                          disabled={busy || !k.enabled}
                          onChange={(e) => void run(() => api.put(`/api/notify/defaults/${k.kind}`, { enabled: k.enabled, locked: e.target.checked }), `${k.name} 필수 설정을 저장했습니다.`)}
                        />{" "}
                        {k.locked ? "필수" : "선택"}
                      </label>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <h3>설정값</h3>
          <p className="hint">업무 시간 외 묶음 발송·재시도·속도 제한·지급 예정과 월 마감 점검 알림의 기준입니다. 범위를 벗어나면 저장되지 않습니다.</p>
          {d.settings.map((s) => {
            const v = edit[s.key] ?? s.value;
            const id = `ns-${s.key.replace(/\./g, "-")}`;
            return (
              <div className="field" key={s.key}>
                <label htmlFor={id}>
                  {s.label}
                  {s.unit ? ` (${s.unit})` : ""}
                </label>
                {s.type === "bool" ? (
                  <select id={id} value={v} disabled={busy} onChange={(e) => setEdit({ ...edit, [s.key]: e.target.value })}>
                    <option value="0">꺼짐</option>
                    <option value="1">켜짐</option>
                  </select>
                ) : (
                  <input id={id} value={v} disabled={busy} inputMode={s.type === "int" ? "numeric" : "text"} placeholder={s.type === "clock" ? "09:00" : ""} onChange={(e) => setEdit({ ...edit, [s.key]: e.target.value })} />
                )}
                {s.hint && <p className="hint">{s.hint}</p>}
              </div>
            );
          })}
          <p>
            <button
              type="button"
              disabled={busy || changed.length === 0}
              onClick={() =>
                void run(() => api.put("/api/notify/settings", { values: Object.fromEntries(changed.map((k) => [k, edit[k]])) }), "설정을 저장했습니다.").then((ok) => {
                  if (ok) setEdit({});
                })
              }
            >
              설정 저장
            </button>
          </p>
        </>
      )}
    </section>
  );
}
