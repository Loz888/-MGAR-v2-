package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Loz888/-MGAR-v2-/internal/config"
	"github.com/Loz888/-MGAR-v2-/internal/control"
	"github.com/Loz888/-MGAR-v2-/internal/scheduler"
	"github.com/Loz888/-MGAR-v2-/internal/store"
)

func main() {
	once := flag.Bool("once", false, "perform one recovery pass without starting HTTP")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("usage: orchestratord [--once]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, *once); err != nil {
		log.Fatal(err)
	}
}

func serve(ctx context.Context, once bool) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	s, err := store.Open(ctx, os.Getenv("MGAR_TEST_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer s.Pool.Close()
	token := make([]byte, 16)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	owner := "orchestratord-" + hex.EncodeToString(token)
	sched := scheduler.Scheduler{Store: s}
	if err = sched.RecoverAll(ctx, owner); err != nil {
		return err
	}
	if once {
		fmt.Println("recovery scan completed; no Agent, model or tool executed")
		return nil
	}
	server := &http.Server{Addr: cfg.Listen, Handler: control.HandlerWithReadiness(s.Pool.Ping), ReadHeaderTimeout: 5 * time.Second}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-scanCtx.Done():
				return
			case <-ticker.C:
				if err := sched.RecoverAll(scanCtx, owner); err != nil && scanCtx.Err() == nil {
					log.Printf("recovery scan failed: %v", err)
				}
			}
		}
	}()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-scanCtx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.ListenAndServe()
	cancel()
	<-done
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
