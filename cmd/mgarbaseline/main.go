// mgarbaseline is an isolated experiment entry; it never enables production success.
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
	id := flag.String("case", "offline-002", "frozen offline or development case")
	fault := flag.String("fault", "none", "bounded negative-test fault")
	flag.Parse()
	if *provider != "mock" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "only bounded mock experiments are authorized")
		os.Exit(2)
	}
	root, e := os.Getwd()
	if e != nil {
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	report, e := demo.RunBaseline(ctx, demo.Options{Root: root, Case: *id, Fault: *fault})
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
		os.Exit(1)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "baseline execution error; structured failure retained")
		os.Exit(1)
	}
}
