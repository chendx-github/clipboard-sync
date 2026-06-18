package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"clipboard-sync/cache"
	"clipboard-sync/clipboard"
	"clipboard-sync/device"
	"clipboard-sync/internal/app"
	"clipboard-sync/internal/config"
	"clipboard-sync/internal/logx"
	"clipboard-sync/mq"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return runAgent(nil)
	}
	switch args[0] {
	case "run":
		return runAgent(args[1:])
	case "paste":
		return runPaste(args[1:])
	default:
		return runAgent(args)
	}
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", "configs/config.yaml", "path to config yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	agent, cleanup, err := bootstrap(*configPath)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := agent.Start(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

func runPaste(args []string) error {
	fs := flag.NewFlagSet("paste", flag.ContinueOnError)
	configPath := fs.String("config", "configs/config.yaml", "path to config yaml")
	timeout := fs.Duration("timeout", 10*time.Minute, "paste transfer timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	agent, cleanup, err := bootstrap(*configPath)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	return agent.RequestRemotePaste(ctx, *timeout)
}

func bootstrap(configPath string) (*app.Agent, func(), error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	deviceID, err := device.ResolveID(cfg.DeviceID)
	if err != nil {
		return nil, nil, err
	}
	logger := logx.New(cfg.LogLevel)
	clip, err := clipboard.New(cfg.PollInterval())
	if err != nil {
		return nil, nil, err
	}
	store, err := cache.New(cfg.CacheDir)
	if err != nil {
		return nil, nil, err
	}
	mqClient, err := mq.New(cfg.NATSURL, logger)
	if err != nil {
		return nil, nil, err
	}
	agent := app.NewAgent(deviceID, cfg, clip, mqClient, store, logger)
	cleanup := func() {
		agent.Close()
	}
	return agent, cleanup, nil
}

func usage() error {
	return fmt.Errorf("usage: agent [run] [-config configs/config.yaml] | agent paste [-config configs/config.yaml]")
}
