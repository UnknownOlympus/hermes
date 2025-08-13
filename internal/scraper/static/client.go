package static

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/UnknownOlympus/hermes/internal/config"
	"github.com/UnknownOlympus/hermes/internal/monitoring"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
)

var (
	ErrLogin      = errors.New("login failed")
	ErrScrapeTask = errors.New("failed to scrape tasks")
)

// Scraper combines client and parsing logic for static pages.
type Scraper struct {
	client  *http.Client
	cfg     *config.Config
	log     *slog.Logger
	metrics *monitoring.Metrics
}

type ScraperIface interface {
	GetEmployees(ctx context.Context) ([]*pb.Employee, string, error)
	GetTaskTypes(ctx context.Context) ([]string, string, error)
	GetDailyTasks(ctx context.Context, date time.Time) ([]*pb.Task, string, error)
}

// NewScraper creates a new client, configures it, and performs login.
func NewScraper(cfg *config.Config, log *slog.Logger, metrics *monitoring.Metrics) (*Scraper, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	client := &http.Client{
		Jar: jar,
	}

	scraper := &Scraper{
		client:  client,
		cfg:     cfg,
		log:     log,
		metrics: metrics,
	}

	err = scraper.retryLogin(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to login after multiple retries: %w", err)
	}

	scraper.log.Info("Static Scraper initialized and logged in successfully.")
	return scraper, nil
}

// --- Login logic ---

func (s *Scraper) login(ctx context.Context) error {
	data := url.Values{
		"action":   {"login"},
		"username": {s.cfg.Username},
		"password": {s.cfg.Password},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.LoginURL, strings.NewReader(data.Encode()))
	if err != nil {
		s.metrics.ScrapeErrors.WithLabelValues("login", "create_request_failed").Inc()
		return fmt.Errorf("failed to create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(
		"User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36",
	)

	resp, err := s.client.Do(req)
	if err != nil {
		s.metrics.ScrapeErrors.WithLabelValues("login", "request_failed").Inc()
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.metrics.ScrapeErrors.WithLabelValues("login", "bad_status_code").Inc()
		return fmt.Errorf("%w, status code: %d", ErrLogin, resp.StatusCode)
	}
	if strings.Contains(resp.Request.URL.String(), "login") {
		s.metrics.ScrapeErrors.WithLabelValues("login", "invalid_credentials").Inc()
		return ErrLogin
	}
	return nil
}

func (s *Scraper) retryLogin(ctx context.Context) error {
	const retries = 3
	const retryTimeout = 5 * time.Second
	var lastErr error

	for idx := range retries {
		err := s.login(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		s.log.InfoContext(
			ctx,
			"Login attempt failed",
			"attempt", idx+1,
			"max_attempt", retries,
			"error", err,
			"timeput", retryTimeout,
		)
		time.Sleep(retryTimeout)
	}
	return fmt.Errorf("login failed after %d retries: %w", retries, lastErr)
}

// --- Helper functions.
func (s *Scraper) getHTMLResponse(ctx context.Context, data *url.Values) (*http.Response, error) {
	reqURL, _ := url.Parse(s.cfg.TargetURL)
	reqURL.RawQuery = data.Encode()
	target := data.Get("core_section")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.metrics.ScrapeErrors.WithLabelValues(target, "request_failed").Inc()
		return nil, fmt.Errorf("failed to perform request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		s.metrics.ScrapeErrors.WithLabelValues(target, "bad_status_code").Inc()
		defer resp.Body.Close()
		return nil, fmt.Errorf("%w, received status code: %d", ErrScrapeTask, resp.StatusCode)
	}
	return resp, nil
}

func calculateSortedHash[T any](data []T, less func(i, j int) bool) (string, error) {
	if len(data) == 0 {
		return fmt.Sprintf("%x", sha256.Sum256([]byte("[]"))), nil
	}

	sort.Slice(data, less)

	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to encode hash to json: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(jsonData)), nil
}

func parseIDFromHref(href string) (int, error) {
	for part := range strings.SplitSeq(href, "&") {
		if strings.HasPrefix(part, "id=") {
			var identifier int

			_, err := fmt.Sscanf(part, "id=%d", &identifier)
			if err != nil {
				return 0, fmt.Errorf("failed to scan the string '%s':%w", part, err)
			}

			return identifier, nil
		}
	}

	return 0, nil
}
