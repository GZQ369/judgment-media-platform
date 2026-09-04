package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"vertical-intel-video-agent/internal/api"
	"vertical-intel-video-agent/internal/engine"
	"vertical-intel-video-agent/internal/store"
)

func main() {
	addr := getenv("ADDR", ":8088")
	s := store.NewMemoryStore()
	e := engine.NewEngine(s)
	h := api.NewHandler(e)

	srv := &http.Server{
		Addr:              addr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("vertical-intel-video-agent listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
