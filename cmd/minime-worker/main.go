package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/chadnewbry/mini-mi-api/internal/minime"
)

func main() {
	config := minime.LoadConfig()
	config.RunWorkers = true

	server, err := minime.NewServer(config)
	if err != nil {
		log.Fatalf("create worker: %v", err)
	}

	_ = server
	log.Printf("mini me worker running (data root: %s, workers: %d)", config.DataRoot, config.WorkerCount)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	log.Printf("mini me worker stopping")
}
