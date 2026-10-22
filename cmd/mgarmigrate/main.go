package main

import (
	"context"
	"fmt"
	"github.com/Loz888/-MGAR-v2-/internal/store"
	"os"
)

func main() {
	s, err := store.Open(context.Background(), os.Getenv("MGAR_TEST_DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer s.Pool.Close()
	if err = store.Migrate(context.Background(), s.Pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("migration 001 applied/verified")
}
