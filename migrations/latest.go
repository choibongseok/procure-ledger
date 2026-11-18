package migrations

import (
	"sort"
	"strings"
)

// Latest 는 이 바이너리가 아는 마지막 마이그레이션 번호(4자리, 예 "0055")다. 백업 세트의 번호와 비교해 더 새로운 세트의 복구를 거부할 때 쓴다.
func Latest() string {
	ents, err := files.ReadDir(".")
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".sql") && len(e.Name()) >= 4 {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[len(names)-1][:4]
}
