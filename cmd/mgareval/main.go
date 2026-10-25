// mgareval executes exactly one owned synthetic experiment; no live provider.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/demo"
)

func main() {
	arm := flag.String("arm", "B4", "closed experiment arm")
	caseID := flag.String("case", "offline-001", "frozen offline case")
	provider := flag.String("provider", "mock", "mock only")
	flag.Parse()
	if *provider != "mock" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "experiment_configuration_denied")
		os.Exit(2)
	}
	root, e := os.Getwd()
	if e != nil {
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	result, e := demo.RunExperiment(ctx, demo.Options{Root: root, Case: *caseID, Fault: "none"}, *arm)
	if encode := json.NewEncoder(os.Stdout).Encode(result); encode != nil {
		os.Exit(1)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "experiment_control_error")
		os.Exit(1)
	}
}
