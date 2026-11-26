package placement

import (
	"errors"
	"gostorage/internal/chunk"
	"gostorage/internal/domain"
	"sort"
	"time"
)

var ErrInsufficientNodes = errors.New("insufficient healthy nodes or capacity")

func Select(nodes []domain.StorageNode, fileSize int64, maxNodes int, now time.Time, ttl time.Duration) ([]domain.StorageNode, []chunk.Plan, error) {
	if maxNodes < 2 {
		maxNodes = 2
	}
	eligible := make([]domain.StorageNode, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		if n.ID == "" || n.Status != domain.NodeActive || n.CapacityBytes <= n.UsedBytes || n.LastHeartbeatAt.IsZero() || now.Sub(n.LastHeartbeatAt) > ttl {
			continue
		}
		if _, ok := seen[n.ID]; ok {
			continue
		}
		seen[n.ID] = struct{}{}
		eligible = append(eligible, n)
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	if len(eligible) > maxNodes {
		eligible = eligible[:maxNodes]
	}
	for count := len(eligible); count >= 2 && int64(count) <= fileSize; count-- {
		plans, e := chunk.PlanFile(fileSize, count)
		if e != nil {
			continue
		}
		chosen := make([]domain.StorageNode, 0, count)
		for _, n := range eligible {
			if n.CapacityBytes-n.UsedBytes >= plans[len(chosen)].Size {
				chosen = append(chosen, n)
				if len(chosen) == count {
					return chosen, plans, nil
				}
			}
		}
	}
	return nil, nil, ErrInsufficientNodes
}
