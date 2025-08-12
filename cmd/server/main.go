package main

import (
	"context"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/UnknownOlympus/hermes/internal/config"
	"github.com/UnknownOlympus/hermes/internal/scraper/static"
	"github.com/UnknownOlympus/hermes/internal/server"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

const (
	envLocal = "local"
	envDev   = "development"
	envProd  = "production"
)

func main() {
	// This is the entry point for the Hermes server.

	// It sets up the context for graceful shutdown and loads the configuration.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	// The server will be started with the configuration loaded from the environment variables.
	cfg := config.MustLoad()

	// Set up the logger based on the environment specified in the configuration.
	logger := setupLogger(cfg.Env)

	logger.InfoContext(ctx, "Hermes server is starting with configuration loaded successfully.")

	lis, err := net.Listen("tcp", cfg.GrpcPort)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	serv := grpc.NewServer()

	// scraperCLient, err := scraper.NewClient(cfg, logger)
	// if err != nil {
	// 	log.Fatalf("Failed to create scraper client: %v", err)
	// }
	// defer scraperCLient.Close()

	staticScraper, err := static.NewScraper(cfg, logger)
	if err != nil {
		log.Fatalf("Failed to create static client (http): %v", err)
	}

	pb.RegisterScraperServiceServer(serv, server.NewGRPCServer(logger, staticScraper))

	logger.InfoContext(ctx, "Server listening", "port", lis.Addr())

	reflection.Register(serv)
	go func() {
		err = serv.Serve(lis)
		if err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	defer serv.GracefulStop()

	<-ctx.Done()

	defer stop()
}

// setupLogger initializes and returns a logger based on the environment provided.
func setupLogger(env string) *slog.Logger {
	var log *slog.Logger

	switch env {
	case envLocal:
		log = slog.New(
			slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
				Level:     slog.LevelDebug,
				AddSource: false,
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					return a
				},
			}),
		)
	case envDev:
		log = slog.New(
			slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
				Level:     slog.LevelInfo,
				AddSource: false,
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					return a
				},
			}),
		)
	case envProd:
		log = slog.New(
			slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
				Level:     slog.LevelWarn,
				AddSource: false,
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == slog.TimeKey {
						return slog.Attr{Key: "", Value: slog.Value{}}
					}
					return a
				},
			}),
		)
	default:
		log = slog.New(
			slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
				Level:     slog.LevelError,
				AddSource: false,
				ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
					if a.Key == slog.TimeKey {
						return slog.Attr{Key: "", Value: slog.Value{}}
					}
					return a
				},
			}),
		)

		log.Error(
			"The env parameter was not specified, or was invalid. Logging will be minimal, by default." +
				" Please specify the value of `env`: local, development, production")
	}

	return log
}
