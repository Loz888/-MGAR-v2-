package main

import (
	"encoding/json"
	"fmt"
	"github.com/Loz888/-MGAR-v2-/internal/config"
	"os"
)

func main() {
	c, e := config.Load(os.Getenv)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if len(os.Args) != 2 || os.Args[1] != "health" {
		fmt.Fprintln(os.Stderr, "usage: mgarctl health (scaffold only)")
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "ok", "provider": c.Provider})
}
