package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lampac-go/internal/browser"
	"lampac-go/internal/config"
	"lampac-go/internal/httpapi"
	"lampac-go/internal/logbuf"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// version, commit and buildDate are set at build time via -ldflags
// "-X main.version=... -X main.commit=... -X main.buildDate=...".
var (
	version   = "0.5"
	commit    = "dev"
	buildDate = ""
)

func main() {
	// -version flag (used by install.sh to verify deployment)
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}

	zerolog.TimeFieldFormat = time.RFC3339

	// Determine log level: env var LOG_LEVEL takes priority, then config.toml [observability] log_level.
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		// Quick config load just to read log_level before full server init.
		if preloadCfg, err := config.Load(); err == nil && preloadCfg.Observability.LogLevel != "" {
			logLevel = preloadCfg.Observability.LogLevel
		}
	}
	switch logLevel {
	case "debug", "DEBUG":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "warn", "WARN":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error", "ERROR":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	logRing := logbuf.New(10000)
	logRing.StartCleanup()
	log.Logger = zerolog.New(io.MultiWriter(os.Stderr, logRing)).
		With().Timestamp().Str("service", "lampac-go").Logger()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := httpapi.NewServer(httpapi.Options{
		LogBuffer: logRing,
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize server")
	}

	// SIGHUP → hot-reload config without restart.
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	go func() {
		for range sighupCh {
			log.Info().Msg("SIGHUP received, reloading config...")
			if err := app.Reload(); err != nil {
				log.Error().Err(err).Msg("config reload failed")
			}
		}
	}()

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := app.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("graceful shutdown error")
		}
	}()

	app.StartBackground(ctx)

	// Sweep /tmp/com.google.Chrome.* scratch dirs left behind when
	// chromedp / Chrome exits ungracefully. One-shot at boot + periodic
	// thereafter; see internal/browser/tmp_janitor.go.
	browser.StartChromeTmpJanitor(ctx, 0, 0)

	log.Info().Str("addr", app.Addr()).Str("version", version).Msg("lampac-go started")
	if err := app.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("server stopped with error")
	}

	log.Info().Msg("lampac-go stopped")
}
