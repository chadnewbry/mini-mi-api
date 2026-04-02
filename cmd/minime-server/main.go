package main

import (
	"log"
	"net/http"
	"os"

	"github.com/chadnewbry/mini-mi-api/internal/minime"
)

func main() {
	config := minime.LoadConfig()
	server, err := minime.NewServer(config)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	addr := ":" + config.Port
	log.Printf("mini me server listening on %s (data root: %s)", addr, config.DataRoot)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: server.Handler(),
	}

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
