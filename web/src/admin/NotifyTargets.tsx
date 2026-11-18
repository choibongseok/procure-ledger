// 알림 채널 관리의 웹훅 대상 영역(B36 명세 4.6): 슬랙·팀즈·카카오워크·termspace 채널(방) 등록·수정·시험 발송·중지, 추가 허용 호스트.
// 주소(와 앱 키)는 입력만 받고 화면에 다시 나오지 않는다. 목록에는 호스트만 보인다.
import { useState } from "react";
import { api } from "../api/client";
import { Badge, EmptyState, LoadingRows, Notice, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface Target {
  id: number;
  name: string;
  type: "slack" | "teams" | "kakaowork" | "termspace";
  typeName: string;
  hint: string;
  kinds: string[];
  kindNames: string[];
  active: boolean;
  autoPaused: boolean;
  state: string;
  failures: number;
  lastOkAt?: string;
  lastError?: string;
  lastErrorAt?: string;
}

interface TargetList {
  targets: Target[];
  kinds: { kind: string; name: string }[];
  sealReady: boolean;
  baseUrlSet: boolean;
  extraHosts: string[];
  builtinHosts: Record<string, string[]>;
}

const TYPES: { v: Target["type"]; label: string }[] = [
  { v: "slack", label: "슬랙" },
  { v: "teams", label: "마이크로소프트 팀즈" },
  { v: "kakaowork", label: "카카오워크" },
  { v: "termspace", label: "termspace" },
];

interface Form {
  id: number | null;
  name: string;
  type: Target["type"];
  url: string;
  token: string;
  conversation: string;
  kinds: string[];
}

const emptyForm: Form = { id: null, name: "", type: "slack", url: "", token: "", conversation: "", kinds: [] };

function when(iso?: string) {
  return iso ? iso.replace("T", " ").replace("Z", "") : "";
}

export function NotifyTargets() {
  const list = useLoad(() => api.get<TargetList>("/api/notify/targets"), []);
  const [form, setForm] = useState<Form | null>(null);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [hostsText, setHostsText] = useState<string | null>(null);
  // 서버가 목록 모양이 아닌 응답을 주면(다른 화면의 시험 대역 등) 아무것도 그리지 않는다.
  const d = list.data && Array.isArray(list.data.targets) ? list.data : null;
  const targets = d ? d.targets : [];

  async function run(fn: () => Promise<unknown>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      setNote({ tone: "ok", text: ok });
      await list.reload();
      return true;
    } catch (e) {
      setNote({ tone: "error", text: errorText(e) });
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    if (!form) return;
    const body = { name: form.name, type: form.type, url: form.url, token: form.token, conversation: form.conversation, kinds: form.kinds };
    const okSave = await run(
      () => (form.id === null ? api.post("/api/notify/targets", body) : api.put(`/api/notify/targets/${form.id}`, body)),
      form.id === null ? "웹훅 대상을 등록했습니다. 시험 보내기로 연결을 확인하세요." : "웹훅 대상을 저장했습니다.",
    );
    if (okSave) setForm(null);
  }

  function edit(t: Target) {
    setForm({ id: t.id, name: t.name, type: t.type, url: "", token: "", conversation: "", kinds: t.kinds });
    setNote(null);
  }

  function toggleKind(k: string) {
    setForm((f) => (f ? { ...f, kinds: f.kinds.includes(k) ? f.kinds.filter((x) => x !== k) : [...f.kinds, k] } : f));
  }

  return (
    <section className="card" aria-labelledby="nt-hooks">
      <h2 id="nt-hooks">슬랙·팀즈·카카오워크</h2>
      <p>
        채널(방) 하나로 알림을 보냅니다. 방에는 알림 종류·건수·앱 링크만 나가고 사람 이름·문서 제목·금액은 나가지 않습니다. 같은 종류의 알림이 짧은 시간에 여러 건 생기면 건수로 합쳐 한 줄만 보냅니다.
      </p>
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {d && !d.sealReady && (
        <Notice tone="warn">웹훅 주소를 안전하게 저장할 키가 서버에 없어 대상을 등록할 수 없습니다. 서버 환경변수로 계좌번호 암호화 키를 설정한 뒤 다시 기동하세요.</Notice>
      )}
      {d && d.sealReady && !d.baseUrlSet && <Notice tone="warn">앱 주소가 설정되지 않아 링크를 만들 수 없어 웹훅을 보내지 않습니다. 서버 환경변수로 앱 주소를 설정하세요.</Notice>}
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {d && targets.length === 0 && <EmptyState hint="아래 단추로 슬랙·팀즈·카카오워크 채널을 등록하세요.">등록된 웹훅 대상이 없습니다.</EmptyState>}
      {(targets.length > 0 || (list.loading && !list.data)) && (
        <TableWrap busy={list.loading && !list.data}>
          <table>
            <thead>
              <tr>
                <th>이름</th>
                <th>종류</th>
                <th>주소</th>
                <th>받을 알림</th>
                <th>상태</th>
                <th>마지막 결과</th>
                <th>작업</th>
              </tr>
            </thead>
            <tbody>
              {list.loading && !list.data && <LoadingRows cols={7} />}
              {targets.map((t) => (
                <tr key={t.id}>
                  <td>{t.name}</td>
                  <td>{t.typeName}</td>
                  <td className="nowrap">{t.hint}</td>
                  <td>{t.kindNames.length ? t.kindNames.join(", ") : <span className="hint">고른 알림이 없어 아무것도 보내지 않습니다</span>}</td>
                  <td>
                    <Badge tone={t.state === "사용 중" ? "ok" : t.state === "자동 중지" ? "bad" : "muted"}>{t.state}</Badge>
                    {t.failures > 0 && <span className="hint"> 연속 실패 {t.failures}번</span>}
                  </td>
                  <td>
                    {t.lastError ? (
                      <span>
                        {t.lastError} <span className="hint">{when(t.lastErrorAt)}</span>
                      </span>
                    ) : t.lastOkAt ? (
                      <span className="hint">{when(t.lastOkAt)} 성공</span>
                    ) : (
                      <span className="hint">아직 보낸 적 없음</span>
                    )}
                  </td>
                  <td className="nowrap">
                    <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => edit(t)}>
                      수정
                    </button>{" "}
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={busy}
                      onClick={() => void run(() => api.post(`/api/notify/targets/${t.id}/test`, {}), `${t.name} 에 시험 메시지를 보냈습니다.`)}
                    >
                      시험 보내기
                    </button>{" "}
                    {t.active ? (
                      <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void run(() => api.post(`/api/notify/targets/${t.id}/pause`, {}), `${t.name} 을(를) 사용 중지했습니다.`)}>
                        사용 중지
                      </button>
                    ) : (
                      <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void run(() => api.post(`/api/notify/targets/${t.id}/resume`, {}), `${t.name} 을(를) 다시 켰습니다.`)}>
                        다시 켜기
                      </button>
                    )}
                    {t.active && t.autoPaused && (
                      <>
                        {" "}
                        <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void run(() => api.post(`/api/notify/targets/${t.id}/resume`, {}), `${t.name} 을(를) 다시 켰습니다.`)}>
                          다시 켜기
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {d?.sealReady && !form && (
        <p>
          <button type="button" disabled={busy} onClick={() => { setForm(emptyForm); setNote(null); }}>
            웹훅 대상 추가
          </button>
        </p>
      )}
      {form && d && (
        <form
          className="card"
          aria-label={form.id === null ? "웹훅 대상 등록" : "웹훅 대상 수정"}
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <h3>{form.id === null ? "웹훅 대상 등록" : "웹훅 대상 수정"}</h3>
          <div className="field">
            <label htmlFor="nt-name">이름(40자 이하)</label>
            <input id="nt-name" value={form.name} maxLength={40} required onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </div>
          <div className="field">
            <label htmlFor="nt-type">종류</label>
            <select id="nt-type" value={form.type} disabled={form.id !== null} onChange={(e) => setForm({ ...form, type: e.target.value as Target["type"] })}>
              {TYPES.map((t) => (
                <option key={t.v} value={t.v}>
                  {t.label}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="nt-url">{form.type === "kakaowork" ? "봇 API 주소" : "웹훅 주소"}</label>
            <input
              id="nt-url"
              type="password"
              autoComplete="off"
              value={form.url}
              required={form.id === null}
              onChange={(e) => setForm({ ...form, url: e.target.value })}
              placeholder={form.id === null ? (form.type === "kakaowork" ? "https://api.kakaowork.com/v1/messages.send" : form.type === "termspace" ? "https://<호스트>/hooks/tsh_…" : "https://…") : "바꿀 때만 새 주소를 입력"}
            />
            <p className="hint">
              허용 호스트: {(d.builtinHosts[form.type] ?? []).join(", ")}
              {d.extraHosts.length > 0 ? `, ${(d.extraHosts ?? []).join(", ")}` : ""}. 저장하면 다시 볼 수 없습니다.
            </p>
          </div>
          {form.type === "kakaowork" && (
            <>
              <div className="field">
                <label htmlFor="nt-token">앱 키</label>
                <input id="nt-token" type="password" autoComplete="off" value={form.token} required={form.id === null} onChange={(e) => setForm({ ...form, token: e.target.value })} placeholder={form.id === null ? "" : "바꿀 때만 입력"} />
              </div>
              <div className="field">
                <label htmlFor="nt-conv">대화방 ID</label>
                <input id="nt-conv" value={form.conversation} required={form.id === null} onChange={(e) => setForm({ ...form, conversation: e.target.value })} placeholder={form.id === null ? "" : "바꿀 때만 입력"} />
                <p className="hint">카카오워크는 주소 하나로 받는 수신 웹훅이 아니라 봇 API(앱 키와 대화방 ID)로 보냅니다.</p>
              </div>
            </>
          )}
          <fieldset>
            <legend>받을 알림 종류</legend>
            <div className="chip-row">
              {d.kinds.map((k) => (
                <label key={k.kind} className="chip">
                  <input type="checkbox" checked={form.kinds.includes(k.kind)} onChange={() => toggleKind(k.kind)} /> {k.name}
                </label>
              ))}
            </div>
          </fieldset>
          <p>
            <button type="submit" disabled={busy}>
              저장
            </button>{" "}
            <button type="button" className="btn-secondary" disabled={busy} onClick={() => setForm(null)}>
              취소
            </button>
          </p>
        </form>
      )}
      {d && (
        <details>
          <summary>추가 허용 호스트</summary>
          <p className="hint">사내 메신저처럼 기본 목록에 없는 웹훅 호스트를 쉼표로 구분해 적습니다(예: hooks.corp.example, *.chat.corp.example). IP 주소는 쓸 수 없고 내부 주소로 연결되는 호스트는 발송 때 거부됩니다.</p>
          <div className="field">
            <label htmlFor="nt-hosts">추가 허용 호스트</label>
            <input id="nt-hosts" value={hostsText ?? (d.extraHosts ?? []).join(", ")} onChange={(e) => setHostsText(e.target.value)} />
          </div>
          <button
            type="button"
            className="btn-secondary"
            disabled={busy || hostsText === null}
            onClick={() =>
              void run(
                () => api.put("/api/notify/hosts", { hosts: (hostsText ?? "").split(",").map((h) => h.trim()).filter(Boolean) }),
                "허용 호스트를 저장했습니다.",
              ).then(() => setHostsText(null))
            }
          >
            허용 호스트 저장
          </button>
        </details>
      )}
    </section>
  );
}
