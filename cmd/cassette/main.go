package main

import (
	"context"
	"flag"
	"os"
	"os/signal"

	"github.com/ipfs/go-log/v2"
	"github.com/ipni/cassette"
	"github.com/ipni/cassette/cmd/cassette/internal"
)

var logger = log.Logger("cassette/cmd")

func main() {
	config := flag.String("config", "", "Path to config YAML file. If unspecified, default config will be used.")
	logLevel := flag.String("logLevel", "info", "The logging level. Only applied if GOLOG_LOG_LEVEL environment variable is unset.")
	flag.Parse()

	if _, set := os.LookupEnv("GOLOG_LOG_LEVEL"); !set {
		_ = log.SetLogLevel("*", *logLevel)
		_ = log.SetLogLevel("net/identify", "error")
	}
	*config = "config.yaml"
	var opts []cassette.Option
	if *config != "" {
		cfg, err := internal.NewConfig(*config)
		if err != nil {
			logger.Fatalw("Failed to instantiate config from path", "path", *config, "err", err)
		}
		opts, err = cfg.ToOptions()
		if err != nil {
			logger.Fatalw("Failed parse config to options", "err", err)
		}
	}

	c, err := cassette.New(opts...)
	if err != nil {
		logger.Fatalw("Failed to instantiate cassette", "err", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		logger.Fatalw("Failed to start cassette", "err", err)
	}
	sch := make(chan os.Signal, 1)
	signal.Notify(sch, os.Interrupt)

	<-sch
	cancel()
	logger.Info("Terminating...")
	if err := c.Shutdown(ctx); err != nil {
		logger.Warnw("Failure occurred while shutting down server.", "err", err)
	} else {
		logger.Info("Shut down server successfully.")
	}
}
