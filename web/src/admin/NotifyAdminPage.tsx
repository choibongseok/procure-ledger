// 알림 채널 관리(B36 명세 4.7): 메일 상태 카드·시험 메일·발송 큐 요약. notification:manage(관리자).
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge, Notice } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { NotifyDefaults } from "./NotifyDefaults";
import { NotifyTargets } from "./NotifyTargets";

interface QueueStatus {
  mailConfigured: boolean;
  baseUrlSet: boolean;
  queued: number;
  sending: number;
  failed: number;
  queuedWarn: boolean;
}

export function NotifyAdminPage() {
  const status = useLoad(() => api.get<QueueStatus>("/api/notify/status"), []);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const s = status.data;

  async function mailTest() {
    setBusy(true);
    setNote(null);
    try {
      await api.post("/api/notify/mail-test", {});
      setNote({ tone: "ok", text: "시험 메일을 보냈습니다. 내 메일함을 확인하세요." });
    } catch (e) {
      setNote({ tone: "error", text: errorText(e) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>알림 채널</h1>
      <p>앱 안 알림을 메일과 슬랙·팀즈·카카오워크·termspace 채널로도 보냅니다. 메일과 채널에는 알림 종류·건수·앱 링크만 담기고 문서 제목·이름·금액은 담기지 않습니다. 발송은 업무 처리와 따로 진행되어 업무가 느려지지 않습니다.</p>
      {status.error && <Notice tone="error">{status.error}</Notice>}
      {status.loading && !s && <p className="hint">불러오는 중…</p>}
      {s && (
        <>
          <section className="card" aria-labelledby="nt-mail">
            <h2 id="nt-mail">메일</h2>
            <p>
              메일 서버 {s.mailConfigured ? <Badge tone="ok">설정됨</Badge> : <Badge tone="warn">설정 안 됨</Badge>} 앱 주소 {s.baseUrlSet ? <Badge tone="ok">설정됨</Badge> : <Badge tone="warn">설정 안 됨</Badge>}
            </p>
            {!s.mailConfigured && <Notice tone="warn">메일 서버가 설정되지 않아 메일은 보내지 않고 앱 안 알림만 만들어집니다. 서버 환경변수로 메일 서버를 설정한 뒤 다시 기동하세요.</Notice>}
            {s.mailConfigured && !s.baseUrlSet && <Notice tone="warn">앱 주소가 설정되지 않아 링크를 만들 수 없어 메일을 보내지 않습니다. 서버 환경변수로 앱 주소를 설정하세요.</Notice>}
            <p>
              <button type="button" className="btn-secondary" disabled={busy || !s.mailConfigured} onClick={() => void mailTest()}>
                시험 메일 보내기
              </button>{" "}
              <span className="hint">내 이메일로 고정 문구 한 통을 바로 보냅니다.</span>
            </p>
            {note && <Notice tone={note.tone}>{note.text}</Notice>}
          </section>
          <NotifyDefaults />
          <NotifyTargets />
          <section className="card" aria-labelledby="nt-queue">
            <h2 id="nt-queue">발송 큐</h2>
            <p className="chip-row">
              <Badge tone={s.queued > 0 ? "info" : "muted"}>대기 {s.queued.toLocaleString("ko-KR")}건</Badge>
              <Badge tone={s.sending > 0 ? "info" : "muted"}>발송 중 {s.sending.toLocaleString("ko-KR")}건</Badge>
              <Badge tone={s.failed > 0 ? "bad" : "muted"}>실패 {s.failed.toLocaleString("ko-KR")}건</Badge>
            </p>
            {s.queuedWarn && <Notice tone="warn">대기 중인 발송 건이 1만 건을 넘었습니다. 메일 서버 상태를 확인하세요.</Notice>}
            <p>
              <Link to="/admin/notifications/deliveries">발송 기록 보기</Link>
            </p>
          </section>
        </>
      )}
    </section>
  );
}
