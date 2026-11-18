// 관리자 설정 화면(llm:policy_manage): AI 모델 허용 목록, 일일 사용 한도, 이미지 증빙 AI 전송 허용.
import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { LLMPolicyEditor } from "./LLMPolicyEditor";

export function SettingsPage() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ enabled: boolean }>("/api/admin/settings/llm-image");
      setEnabled(r.enabled);
    } catch {
      setError("설정을 불러오지 못했습니다.");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function toggle(next: boolean) {
    setError("");
    try {
      const r = await api.put<{ enabled: boolean }>("/api/admin/settings/llm-image", { enabled: next });
      setEnabled(r.enabled);
    } catch {
      setError("설정을 바꾸지 못했습니다.");
    }
  }

  return (
    <section>
      <h1>관리자 설정</h1>
      <LLMPolicyEditor />
      <div className="card">
        <h2>이미지 증빙 AI 전송</h2>
        <p>켜면 재무 담당자가 올린 이미지 증빙이 마스킹 없이 AI 모델로 전송됩니다. 기본값은 꺼짐이며, 변경은 감사 로그에 남습니다.</p>
        {error && <Notice tone="error">{error}</Notice>}
        {enabled !== null && (
          <label>
            <input type="checkbox" checked={enabled} onChange={(e) => void toggle(e.target.checked)} /> 이미지 증빙의 AI 전송 허용
          </label>
        )}
      </div>
    </section>
  );
}
