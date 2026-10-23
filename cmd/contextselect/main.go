// contextselect exercises the pure selector against trusted offline fixtures.
// It does not authenticate Workers or grant access to a database/CAS.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	inputcontext "github.com/Loz888/-MGAR-v2-/internal/context"
)

type request struct {
	Snapshot  inputcontext.Snapshot `json:"snapshot"`
	Goal      string                `json:"goal"`
	Mode      string                `json:"mode"`
	MaxBytes  int                   `json:"max_bytes"`
	MaxTokens int                   `json:"max_tokens"`
	Content   map[string]string     `json:"content"`
}

func main() {
	if len(os.Args) != 1 {
		os.Exit(2)
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 2*1048576))
	decoder.DisallowUnknownFields()
	var input request
	if e := decoder.Decode(&input); e != nil {
		fmt.Fprintln(os.Stderr, "invalid selection fixture")
		os.Exit(2)
	}
	var extra any
	if e := decoder.Decode(&extra); e != io.EOF {
		fmt.Fprintln(os.Stderr, "trailing selection input")
		os.Exit(2)
	}
	content := map[string][]byte{}
	for id, text := range input.Content {
		content[id] = []byte(text)
	}
	result, e := inputcontext.Select(input.Snapshot, input.Goal, input.Mode, input.MaxBytes, input.MaxTokens, content)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if e = json.NewEncoder(os.Stdout).Encode(result); e != nil {
		os.Exit(1)
	}
}
