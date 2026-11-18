// 내 알림 설정(B36 단계 3, 명세 4.3·4.4): 알림 종류별 메일 켜짐/꺼짐/기본값 따름, 업무 시간 외 묶음 받기. 앱 안 알림은 끌 수 없다.
// 본인 것만 보고 바꾼다(관리자도 남의 설정은 못 본다). 메일 주소는 화면에 나오지 않는다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge, Notice, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

type Mode = "on" | "off" | "default";

interface PrefKind {
  kind: string;
  name: string;
  defaultEnabled: boolean;
  locked: boolean;
  mode: Mode;
  effective: boolean;
}

interface Prefs {
  mailAvailable: boolean;
  kinds: PrefKind[];
  digest: { mode: Mode; defaultOn: boolean; effective: boolean };
  workHours: string;
  holdMaxHours: number;
}

const MODES: { v: Mode; label: string }[] = [
  { v: "default", label: "기본값 따름" },
  { v: "on", label: "켜짐" },
  { v: "off", label: "꺼짐" },
];

export function NotifyPrefsPage() {
  const load = useLoad(() => api.get<Prefs>("/api/notifications/prefs"), []);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const p = load.data && Array.isArray(load.data.kinds) ? load.data : null;

  async function save(fn: () => Promise<unknown>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      setNote({ tone: "ok", text: ok });
      await load.reload();
    } catch (e) {
      setNote({ tone: "error", text: errorText(e) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>내 알림 설정</h1>
      <p>
        앱 안 알림은 항상 받습니다(끌 수 없습니다). 아래에서 같은 알림을 <strong>메일로도</strong> 받을지 종류별로 정합니다. 메일에는 알림 종류·건수·앱 링크만 담기고 문서 제목·이름·금액은 담기지 않습니다.{" "}
        <Link to="/notifications">알림 목록으로</Link>
      </p>
      {load.error && <Notice tone="error">{load.error}</Notice>}
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {load.loading && !p && <p className="hint">불러오는 중…</p>}
      {p && (
        <>
          {!p.mailAvailable && <Notice tone="warn">메일 서버가 설정되어 있지 않아 지금은 메일이 나가지 않습니다. 설정은 미리 정해 둘 수 있습니다.</Notice>}
          <section className="card" aria-labelledby="np-digest">
            <h2 id="np-digest">업무 시간 외 묶음 받기</h2>
            <p>
              켜면 업무 시간({p.workHours}, 영업일) 밖에 생긴 메일 알림을 바로 보내지 않고 다음 영업일 업무 시작 때 종류별 건수만 담은 메일 한 통으로 보냅니다. 결재 독촉은 묶지 않고 바로 받습니다. 보류가 {p.holdMaxHours}시간을 넘기면 업무 시간이 아니어도 보냅니다.
            </p>
            <div className="form-row" role="radiogroup" aria-label="업무 시간 외 묶음 받기">
              {MODES.map((m) => (
                <label key={m.v} className="chip">
                  <input
                    type="radio"
                    name="np-digest-mode"
                    checked={p.digest.mode === m.v}
                    disabled={busy}
                    onChange={() => void save(() => api.put("/api/notifications/digest", { mode: m.v }), "묶음 받기 설정을 저장했습니다.")}
                  />{" "}
                  {m.label}
                  {m.v === "default" ? `(지금 ${p.digest.defaultOn ? "켜짐" : "꺼짐"})` : ""}
                </label>
              ))}
            </div>
            <p className="hint">지금 적용: {p.digest.effective ? "묶어서 받음" : "바로 받음"}</p>
          </section>
          <section className="card" aria-labelledby="np-kinds">
            <h2 id="np-kinds">메일로 받을 알림</h2>
            {p.kinds.length === 0 ? (
              <p className="hint">메일로 보내는 알림 종류가 없습니다.</p>
            ) : (
              <TableWrap>
                <table>
                  <thead>
                    <tr>
                      <th>알림</th>
                      <th>기본값</th>
                      <th>내 설정</th>
                      <th>지금</th>
                    </tr>
                  </thead>
                  <tbody>
                    {p.kinds.map((k) => (
                      <tr key={k.kind}>
                        <td>{k.name}</td>
                        <td>
                          {k.defaultEnabled ? "켜짐" : "꺼짐"}
                          {k.locked && <span className="hint"> (필수)</span>}
                        </td>
                        <td>
                          <select
                            aria-label={`${k.name} 메일`}
                            value={k.mode}
                            disabled={busy}
                            onChange={(e) => void save(() => api.put(`/api/notifications/prefs/${k.kind}`, { mode: e.target.value }), `${k.name} 설정을 저장했습니다.`)}
                          >
                            {MODES.filter((m) => !(k.locked && m.v === "off")).map((m) => (
                              <option key={m.v} value={m.v}>
                                {m.label}
                              </option>
                            ))}
                          </select>
                        </td>
                        <td>{k.effective ? <Badge tone="ok">메일 받음</Badge> : <Badge tone="muted">앱 안에서만</Badge>}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            )}
          </section>
        </>
      )}
    </section>
  );
}
