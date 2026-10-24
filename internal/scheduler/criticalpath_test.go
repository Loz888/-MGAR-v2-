package scheduler

import (
	"github.com/Loz888/-MGAR-v2-/internal/dag"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"testing"
	"time"
)

func TestCriticalPathReadyOnlyAndAging(t *testing.T) {
	now := time.Now()
	nodes := []store.ScheduleNode{
		{ID: "a", Key: "A", Role: "research", State: "ready", ReadyAt: now, Estimate: time.Second},
		{ID: "b", Key: "B", Role: "research", State: "ready", ReadyAt: now, Estimate: time.Second},
		{ID: "c", Key: "C", Role: "research", State: "pending", ReadyAt: now, Estimate: 10 * time.Second},
	}
	edges := []dag.Edge{{From: "B", To: "C"}}
	ranked, e := Rank(nodes, edges, "research", "critical_path", now)
	if e != nil || len(ranked) != 2 || ranked[0].ID != "b" {
		t.Fatal(ranked, e)
	}
	fifo, e := Rank(nodes, edges, "research", "fifo", now)
	if e != nil || fifo[0].ID != "a" {
		t.Fatal(fifo, e)
	}
	nodes[0].ReadyAt = now.Add(-20 * time.Second)
	ranked, e = Rank(nodes, edges, "research", "critical_path", now)
	if e != nil || ranked[0].ID != "a" {
		t.Fatal("aging", ranked, e)
	}
	// Even an erroneously marked Ready child cannot outrank unfinished parents.
	nodes[2].State = "ready"
	ranked, e = Rank(nodes, edges, "research", "critical_path", now)
	if e != nil || len(ranked) != 2 {
		t.Fatal("dependency escaped", ranked, e)
	}
	if _, e = Rank(nodes, append(edges, dag.Edge{From: "C", To: "B"}), "research", "critical_path", now); e == nil {
		t.Fatal("cycle")
	}
	if _, e = Rank(nodes, edges, "research", "unknown", now); e == nil {
		t.Fatal("unknown policy")
	}
}
