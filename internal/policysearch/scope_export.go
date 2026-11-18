package policysearch

import "fmt"

// B47: 업로드 매뉴얼·규정의 열람 범위는 규정 머리말과 같은 의미(역할 AND 부서)다. 같은 파서·같은 판정을 쓰도록 여기서 내보낸다.

// ParseScope 는 머리말 원문 형식의 열람 범위("all" 또는 쉼표 목록)를 문서 범위로 바꾼다. roles 의 항목은 기본 역할 이름 또는 권한 이름이어야 하고
// 비어 있거나 알 수 없는 항목은 오류다(fail-closed: 범위를 안 적은 문서를 전체 공개로 두지 않는다). 부서 이름의 존재 확인은 호출한 쪽이 한다.
func ParseScope(roles, departments string) (Doc, error) {
	d := Doc{RawRoles: roles, RawDepts: departments}
	var err error
	if d.AllRoles, d.Roles, err = parseScope(roles, true); err != nil {
		return Doc{}, fmt.Errorf("roles: %w", err)
	}
	if d.AllDepts, d.Depts, err = parseScope(departments, false); err != nil {
		return Doc{}, fmt.Errorf("departments: %w", err)
	}
	if err := checkRoles(d); err != nil {
		return Doc{}, err
	}
	return d, nil
}

// AllowsRaw 는 열람 범위를 원문(RawRoles·RawDepts)에서 독립으로 다시 계산한 판정이다(색인 쪽 Visible 과 다른 구현, 두 번째 방어선).
func (d Doc) AllowsRaw(v Viewer) bool { return rawAllows(d, v) }
