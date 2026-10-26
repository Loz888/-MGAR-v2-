// mgareval defaults to one offline experiment; live runs require explicit gates.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Loz888/-MGAR-v2-/eval/live"
	"github.com/Loz888/-MGAR-v2-/internal/demo"
)

func main() {
	arm := flag.String("arm", "B4", "closed experiment arm")
	caseID := flag.String("case", "offline-001", "frozen offline case")
	provider := flag.String("provider", "mock", "mock only")
	isLive := flag.Bool("live", false, "the approved immutable 24x4 experiment")
	confirm := flag.Bool("confirm-live", false, "explicit paid experiment confirmation")
	preflight := flag.Bool("preflight", false, "print configuration and blockers without sending")
	flag.Parse()
	if flag.NArg() != 0 || (!*isLive && (*provider != "mock" || *confirm || *preflight)) || (*isLive && *provider != "deepseek") {
		fmt.Fprintln(os.Stderr, "experiment_configuration_denied")
		os.Exit(2)
	}
	root, e := os.Getwd()
	if e != nil {
		os.Exit(2)
	}
	if *isLive {
		forbidden := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "case" || f.Name == "arm" {
				forbidden = true
			}
		})
		if forbidden {
			fmt.Fprintln(os.Stderr, "live_assignments_are_immutable")
			os.Exit(2)
		}
		checked := live.Preflight(root, *confirm)
		if *preflight || !checked.Ready {
			if json.NewEncoder(os.Stdout).Encode(checked) != nil {
				os.Exit(1)
			}
			if !checked.Ready {
				os.Exit(2)
			}
			return
		}
		result, err := live.Run(context.Background(), root, *confirm)
		if json.NewEncoder(os.Stdout).Encode(result) != nil {
			os.Exit(1)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "live_execution_stopped")
			os.Exit(1)
		}
		return
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
