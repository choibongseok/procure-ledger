// 역할 키 → 이름. 기본 5개는 고정 이름, 사용자 정의 역할(r_번호)은 역할 목록 API 로 받는다(B33).
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { ROLE_LABELS } from "../auth/permissions";

export interface RoleOption {
  key: string;
  name: string;
}

const DEFAULTS: RoleOption[] = Object.entries(ROLE_LABELS).map(([key, name]) => ({ key, name }));

// 마지막으로 받은 역할 이름표. 하위 컴포넌트의 순수 함수(roleName)가 읽는다. 갱신은 useRoleOptions 를 쓴 화면이 다시 그려지며 반영된다.
let known: Record<string, string> = { ...ROLE_LABELS };

/** 역할 키 → 표시 이름. 빈 값은 "전체", 모르는 키는 "(삭제된 역할)". */
export function roleName(key: string): string {
  return key === "" ? "전체" : (known[key] ?? "(삭제된 역할)");
}

/** 역할 선택지와 이름 조회. 목록을 못 받으면(권한 없음 등) 기본 역할 5개로 대신한다. */
export function useRoleOptions(): { options: RoleOption[]; nameOf: (key: string) => string } {
  const [options, setOptions] = useState<RoleOption[]>(DEFAULTS);
  useEffect(() => {
    let alive = true;
    Promise.resolve(api.get<{ roles?: { key: string; name: string }[] }>("/api/role-names"))
      .then((r) => {
        if (alive && r?.roles && r.roles.length > 0) {
          known = { ...ROLE_LABELS, ...Object.fromEntries(r.roles.map((x) => [x.key, x.name])) };
          setOptions(r.roles.map((x) => ({ key: x.key, name: x.name })));
        }
      })
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);
  return { options, nameOf: roleName };
}
