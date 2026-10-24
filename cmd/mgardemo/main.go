// mgardemo runs owned synthetic offline business fixtures; it never enables paid calls.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Loz888/-MGAR-v2-/internal/demo"
	"os"
	"time"
)

func main() {
	provider := flag.String("provider", "mock", "only mock is authorized")
	caseID := flag.String("case", "offline-001", "frozen offline/development case")
	fault := flag.String("fault", "none", "bounded negative-test fault")
	flag.Parse()
	if *provider != "mock" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "only bounded mock fixtures are authorized")
		os.Exit(2)
	}
	root, e := os.Getwd()
	if e != nil {
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	report, e := demo.Run(ctx, demo.Options{Root: root, Case: *caseID, Fault: *fault})
	if e != nil {
		fmt.Fprintln(os.Stderr, "synthetic demo failed; no success claim")
		os.Exit(1)
	}
	if e = json.NewEncoder(os.Stdout).Encode(report); e != nil {
		os.Exit(1)
	}
	if report.State == "interrupted" || (*fault == "none" && report.State == "failed") {
		os.Exit(1)
	}
}
