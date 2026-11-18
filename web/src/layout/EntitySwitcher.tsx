// 법인 선택기(B44 명세 15.1 3항, GNB 오른쪽): 고를 수 있는 법인이 둘 이상인 사람에게만 보인다. 법인이 하나뿐이면 아무것도 그리지 않아 화면이 이전과 같다.
// 고르면 세션의 현재 법인이 바뀌고(PUT /api/me/entity) 화면은 이동 없이 현재 데이터를 새로 읽는다(Layout 이 본문을 현재 법인으로 다시 마운트한다).
import { useState } from "react";
import { useAuth } from "../auth/AuthProvider";
import { errorText } from "../purchase/useLoad";

/** 현재 법인을 가리키는 선택 값("*" 는 전체 법인). */
export function entityKey(cur: { all: boolean; code?: string } | null): string {
  if (!cur) return "";
  return cur.all ? "*" : (cur.code ?? "");
}

export function EntitySwitcher() {
  const { entities = [], currentEntity = null, groupScope = false, switchEntity } = useAuth();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  if (entities.length < 2) return null;
  const value = entityKey(currentEntity);
  return (
    <div className="gnb-entity">
      <label htmlFor="gnb-entity-select" className="sr-only">
        현재 법인
      </label>
      <select
        id="gnb-entity-select"
        value={value}
        disabled={busy}
        aria-describedby={error ? "gnb-entity-error" : undefined}
        onChange={async (e) => {
          setBusy(true);
          setError("");
          try {
            await switchEntity(e.target.value);
          } catch (err) {
            setError(errorText(err));
          } finally {
            setBusy(false);
          }
        }}
      >
        {groupScope && <option value="*">전체 법인</option>}
        {entities.map((x) => (
          <option key={x.id} value={x.code}>
            {x.name}
          </option>
        ))}
      </select>
      {error && (
        <span id="gnb-entity-error" role="alert" className="hint">
          {error}
        </span>
      )}
    </div>
  );
}

/** 전체 법인(그룹 범위) 모드 배너. 읽기 전용임을 알린다. */
export function GroupModeBanner() {
  const { currentEntity = null } = useAuth();
  if (!currentEntity?.all) return null;
  return (
    <p className="notice notice-info" role="status">
      전체 법인 조회 중(읽기 전용). 쓰기는 법인을 하나 고른 뒤에 할 수 있습니다.
    </p>
  );
}
