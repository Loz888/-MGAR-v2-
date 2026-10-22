package dag

import (
	"errors"
	"sort"
)

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func Order(nodes []string, edges []Edge) ([]string, error) {
	if len(nodes) == 0 || len(nodes) > 24 || len(edges) > 96 {
		return nil, errors.New("DAG size bound")
	}
	degree := map[string]int{}
	depth := map[string]int{}
	out := map[string][]string{}
	seen := map[Edge]bool{}
	for _, n := range nodes {
		if _, ok := degree[n]; ok {
			return nil, errors.New("duplicate node")
		}
		degree[n] = 0
		depth[n] = 1
	}
	for _, e := range edges {
		_, a := degree[e.From]
		_, b := degree[e.To]
		if !a || !b || e.From == e.To || seen[e] {
			return nil, errors.New("invalid dependency")
		}
		seen[e] = true
		degree[e.To]++
		out[e.From] = append(out[e.From], e.To)
		if len(out[e.From]) > 8 {
			return nil, errors.New("fanout bound")
		}
	}
	ready := []string{}
	for n, d := range degree {
		if d == 0 {
			ready = append(ready, n)
		}
	}
	sort.Strings(ready)
	ordered := []string{}
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		ordered = append(ordered, n)
		for _, child := range out[n] {
			if depth[child] < depth[n]+1 {
				depth[child] = depth[n] + 1
			}
			if depth[child] > 6 {
				return nil, errors.New("depth bound")
			}
			degree[child]--
			if degree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(nodes) {
		return nil, errors.New("cycle")
	}
	return ordered, nil
}
