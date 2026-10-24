// schedulingbench executes a fixed, bounded local timer workload. No model calls.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Loz888/-MGAR-v2-/internal/dag"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"math"
	"os"
	"runtime"
	"sort"
	"time"
)

type sample struct {
	Key                        string `json:"key"`
	Ready, Start, End, Service float64
	Parents                    []string `json:"parents"`
}
type trial struct {
	Policy                                        string   `json:"policy"`
	Samples                                       []sample `json:"samples"`
	Makespan, Throughput, P50, P95, MaxWait, Jain float64
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func quantile(x []float64, p float64) float64 {
	sort.Float64s(x)
	return x[int(math.Ceil(p*float64(len(x))))-1]
}
func run(policy string, estimates map[string]time.Duration) (trial, error) {
	start := time.Now()
	nodes := []store.ScheduleNode{}
	edges := []dag.Edge{}
	service := map[string]time.Duration{}
	parents := map[string][]string{}
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("A%02d", i)
		nodes = append(nodes, store.ScheduleNode{ID: key, Key: key, Role: "research", State: "ready", ReadyAt: start, Estimate: estimates["leaf"]})
		service[key] = 15 * time.Millisecond
	}
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("C%02d", i)
		state := "pending"
		if i == 0 {
			state = "ready"
		}
		nodes = append(nodes, store.ScheduleNode{ID: key, Key: key, Role: "research", State: state, ReadyAt: start, Estimate: estimates["chain"]})
		service[key] = 20 * time.Millisecond
		if i > 0 {
			p := fmt.Sprintf("C%02d", i-1)
			parents[key] = []string{p}
			edges = append(edges, dag.Edge{From: p, To: key})
		}
	}
	type done struct {
		key string
		at  time.Time
	}
	finished := make(chan done, 2)
	running, completed := 0, 0
	records := map[string]*sample{}
	order := []string{}
	for completed < len(nodes) {
		ready, e := scheduler.Rank(nodes, edges, "research", policy, time.Now())
		if e != nil {
			return trial{}, e
		}
		for _, rank := range ready {
			if running >= 2 {
				break
			}
			for i := range nodes {
				if nodes[i].Key == rank.Key {
					n := &nodes[i]
					n.State = "executing"
					records[n.Key] = &sample{Key: n.Key, Ready: ms(n.ReadyAt.Sub(start)), Start: ms(time.Since(start)), Service: ms(service[n.Key]), Parents: parents[n.Key]}
					order = append(order, n.Key)
					running++
					go func(key string) { timer := time.NewTimer(service[key]); <-timer.C; finished <- done{key, time.Now()} }(n.Key)
					break
				}
			}
		}
		if running == 0 {
			return trial{}, fmt.Errorf("no runnable work")
		}
		d := <-finished
		running--
		completed++
		records[d.key].End = ms(d.at.Sub(start))
		for i := range nodes {
			if nodes[i].Key == d.key {
				nodes[i].State = "succeeded"
			}
		}
		for i := range nodes {
			n := &nodes[i]
			if n.State != "pending" {
				continue
			}
			ok := true
			for _, p := range parents[n.Key] {
				if records[p] == nil || records[p].End == 0 {
					ok = false
				}
			}
			if ok {
				n.State = "ready"
				n.ReadyAt = d.at
			}
		}
	}
	result := trial{Policy: policy, Samples: []sample{}}
	latency := []float64{}
	sum, squares := 0., 0.
	for _, key := range order {
		s := *records[key]
		result.Samples = append(result.Samples, s)
		result.Makespan = max(result.Makespan, s.End)
		latency = append(latency, s.End)
		wait := s.Start - s.Ready
		result.MaxWait = max(result.MaxWait, wait)
		fraction := s.Service / (s.Service + wait)
		sum += fraction
		squares += fraction * fraction
	}
	result.P50 = quantile(latency, .5)
	result.P95 = quantile(latency, .95)
	result.Throughput = float64(len(nodes)) * 1000 / result.Makespan
	result.Jain = sum * sum / (float64(len(nodes)) * squares)
	return result, nil
}
func main() {
	samples := flag.Int("samples", 5, "paired repetitions, 3..30")
	flag.Parse()
	if *samples < 3 || *samples > 30 || flag.NArg() != 0 {
		os.Exit(2)
	}
	estimates := map[string]time.Duration{}
	for k, d := range map[string]time.Duration{"leaf": 15 * time.Millisecond, "chain": 20 * time.Millisecond} {
		values := []float64{}
		for range 3 {
			s := time.Now()
			<-time.NewTimer(d).C
			values = append(values, float64(time.Since(s)))
		}
		estimates[k] = time.Duration(quantile(values, .5))
	}
	trials := []trial{}
	for i := 0; i < *samples; i++ {
		modes := []string{"fifo", "critical_path"}
		if i%2 == 1 {
			modes = []string{"critical_path", "fifo"}
		}
		for _, policy := range modes {
			r, e := run(policy, estimates)
			if e != nil {
				fmt.Fprintln(os.Stderr, "BENCHMARK_FAILED")
				os.Exit(1)
			}
			trials = append(trials, r)
		}
	}
	report := map[string]any{"schema_version": "scheduling_experiment.v1", "workload": "fixed_timer_dag_8_leaf_5_chain.v1", "workload_kind": "actual_local_timers_not_LLM", "concurrency": 2, "samples_per_policy": *samples, "units": map[string]string{"time": "milliseconds", "throughput": "tasks_per_second", "fairness": "Jain_of_service_over_service_plus_ready_wait"}, "host": map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "cpu_count": runtime.NumCPU(), "gomaxprocs": runtime.GOMAXPROCS(0), "go": runtime.Version()}, "warmup_median_ns": estimates, "trials": trials}
	if e := json.NewEncoder(os.Stdout).Encode(report); e != nil {
		os.Exit(1)
	}
}
