package placement

import (
	"gostorage/internal/domain"
	"testing"
	"time"
)

func TestSelectFiltersUnhealthyAndDuplicates(t *testing.T) {
	now := time.Now()
	active := domain.StorageNode{ID: "a", Status: domain.NodeActive, CapacityBytes: 100, LastHeartbeatAt: now}
	stale := active
	stale.ID = "b"
	stale.LastHeartbeatAt = now.Add(-time.Hour)
	disabled := active
	disabled.ID = "c"
	disabled.Status = domain.NodeDisabled
	nodes := []domain.StorageNode{active, active, stale, disabled}
	if _, _, e := Select(nodes, 10, 10, now, time.Minute); e == nil {
		t.Fatal("expected stale, disabled and duplicate nodes to leave insufficient placement")
	}
}
func TestSelectChoosesCapacityAndStablePlacement(t *testing.T) {
	now := time.Now()
	nodes := []domain.StorageNode{{ID: "c", Status: domain.NodeActive, CapacityBytes: 20, UsedBytes: 15, LastHeartbeatAt: now}, {ID: "a", Status: domain.NodeActive, CapacityBytes: 20, LastHeartbeatAt: now}, {ID: "b", Status: domain.NodeActive, CapacityBytes: 20, LastHeartbeatAt: now}}
	got, plans, e := Select(nodes, 10, 2, now, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" || len(plans) != 2 {
		t.Fatalf("unexpected placement: nodes=%v plans=%v", got, plans)
	}
}
func TestSelectRejectsSmallFileAndInsufficientCapacity(t *testing.T) {
	now := time.Now()
	nodes := []domain.StorageNode{{ID: "a", Status: domain.NodeActive, CapacityBytes: 2, LastHeartbeatAt: now}, {ID: "b", Status: domain.NodeActive, CapacityBytes: 2, LastHeartbeatAt: now}}
	if _, _, e := Select(nodes, 1, 4, now, time.Minute); e == nil {
		t.Fatal("expected too-small file rejection")
	}
	if _, _, e := Select(nodes, 10, 4, now, time.Minute); e == nil {
		t.Fatal("expected capacity rejection")
	}
}
