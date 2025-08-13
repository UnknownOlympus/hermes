package monitoring

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const serverReadTimeout = 5
const serverWriteTimeout = 10

// Metrics contains all Prometheus custom metrics.
type Metrics struct {
	ScrapeDuration *prometheus.HistogramVec
	ScrapeErrors   *prometheus.CounterVec
	CacheOps       *prometheus.CounterVec
}

func NewMetrics(reg *prometheus.Registry) *Metrics {
	return &Metrics{
		ScrapeDuration: promauto.With(reg).NewHistogramVec(prometheus.HistogramOpts{
			Name: "hermes_scrape_duration_seconds",
			Help: "Duration of a scrape operation.",
		}, []string{"target"}),
		ScrapeErrors: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "hermes_scrape_errors_total",
			Help: "Total number of scrape errors.",
		}, []string{"target", "reason"}),
		CacheOps: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "hermes_cache_operations_total",
			Help: "Total number of cache operations.",
		}, []string{"operation", "status"}),
	}
}

// Serve runs HTTP-server for metrics on other port.
func Serve(ctx context.Context, log *slog.Logger, addr string, reg *prometheus.Registry) {
	log.InfoContext(ctx, "Prometheus metrics server starting", "address", addr)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  time.Duration(serverReadTimeout) * time.Second,
		WriteTimeout: time.Duration(serverWriteTimeout) * time.Second,
	}

	var err error
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		sdCtx, cancel := context.WithTimeout(context.Background(), time.Duration(serverReadTimeout)*time.Second)
		defer cancel()
		log.InfoContext(ctx, "Monitoring server shutting down.")
		err = server.Shutdown(sdCtx)
		if err != nil {
			log.ErrorContext(ctx, "Monitoring server failed to shutdown", "error", err)
			return
		}
	case err = <-serverErr:
		log.ErrorContext(ctx, "Monitoring server failed", "error", err)
	}
}
