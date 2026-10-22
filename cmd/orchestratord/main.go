package main

import (
	"github.com/Loz888/-MGAR-v2-/internal/config"
	"github.com/Loz888/-MGAR-v2-/internal/control"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	c, err := config.Load(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	s := http.Server{Addr: c.Listen, Handler: control.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(s.ListenAndServe())
}
