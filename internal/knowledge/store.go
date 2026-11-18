package knowledge

import (
	"context"
	"sync"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/policysearch"
	"github.com/choibongseok/procure-ledger/internal/repository"
	"github.com/choibongseok/procure-ledger/internal/textindex"
)

// Store 는 프로세스 메모리의 조각 색인이다(명세 D1). DB 의 조각 본문이 진실이고, 세대 번호(knowledge_state.generation)가 바뀌면 다시 만든다.
// 서버와 erp-mcp 가 각자 한 개씩 갖는다. 색인이 낡아도 검색 서비스가 문서마다 열람을 다시 판정하므로 권한 밖 결과는 나오지 않는다.
type Store struct {
	DB   contract.Tx
	repo repository.SQLKnowledge

	mu   sync.RWMutex
	snap *snapshot
}

type snapshot struct {
	gen  int64
	ix   *textindex.Index
	rows map[int64]repository.ChunkRow
	// scopes 는 업로드 문서(출처 번호)의 열람 범위다. 범위 원문을 읽지 못하면(깨진 값) 항목이 없어 아무도 못 본다(fail-closed).
	scopes map[int64]policysearch.Doc
}

// NewStore 는 빈 저장소를 만든다. 첫 Refresh 가 적재한다.
func NewStore(db contract.Tx) *Store { return &Store{DB: db} }

// Refresh 는 세대 번호를 읽어 달라졌으면(또는 처음이면) 색인을 다시 만든다. 현재 스냅샷을 돌려준다.
func (s *Store) Refresh(ctx context.Context) (*snapshot, error) {
	gen, _, err := s.repo.Generation(ctx, s.DB)
	if err != nil {
		// 세대를 못 읽으면 마지막으로 알던 색인으로 응답한다(재확인이 항상 걸려 새지 않는다). 처음이면 오류.
		s.mu.RLock()
		cur := s.snap
		s.mu.RUnlock()
		if cur != nil {
			return cur, nil
		}
		return nil, err
	}
	s.mu.RLock()
	cur := s.snap
	s.mu.RUnlock()
	if cur != nil && cur.gen == gen {
		return cur, nil
	}
	rows, err := s.repo.LoadChunks(ctx, s.DB)
	if err != nil {
		if cur != nil {
			return cur, nil
		}
		return nil, err
	}
	entries := make([]textindex.Entry, len(rows))
	byID := make(map[int64]repository.ChunkRow, len(rows))
	scopes := map[int64]policysearch.Doc{}
	for i, r := range rows {
		entries[i] = textindex.Entry{ID: r.ChunkID, Heading: r.Heading, Text: r.Text}
		byID[r.ChunkID] = r
		if r.Kind == repository.KSrcUpload {
			if _, seen := scopes[r.SourceID]; !seen {
				if d, err := policysearch.ParseScope(r.Roles, r.Depts); err == nil {
					scopes[r.SourceID] = d
				}
			}
		}
	}
	next := &snapshot{gen: gen, ix: textindex.Build(entries), rows: byID, scopes: scopes}
	s.mu.Lock()
	s.snap = next
	s.mu.Unlock()
	return next, nil
}

// Len 은 적재된 조각 수다(시험·관리용, 권한 거름을 거치지 않는다).
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap == nil {
		return 0
	}
	return s.snap.ix.Len()
}
