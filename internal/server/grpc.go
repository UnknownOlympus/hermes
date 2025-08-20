package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/UnknownOlympus/hermes/internal/monitoring"
	"github.com/UnknownOlympus/hermes/internal/scraper/static"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const employeesCacheTTL = 10 * time.Minute
const agreementsCacheTTL = 6 * time.Hour

type Server struct {
	pb.UnimplementedScraperServiceServer

	scraper static.ScraperIface
	redis   *redis.Client
	log     *slog.Logger
	metrics *monitoring.Metrics
}

func NewGRPCServer(
	logger *slog.Logger,
	redis *redis.Client,
	scraper static.ScraperIface,
	metrics *monitoring.Metrics,
) *Server {
	return &Server{log: logger, redis: redis, scraper: scraper, metrics: metrics}
}

func (s *Server) GetEmployees(ctx context.Context, req *pb.GetEmployeesRequest) (*pb.GetEmployeesResponse, error) {
	log := s.log.With("op", "GetEmployees")
	log.InfoContext(ctx, "Received request", "known_hash", req.GetKnownHash())

	fetchFn := func() (*pb.GetEmployeesResponse, error) {
		employees, currentHash, err := s.scraper.GetEmployees(ctx)
		if err != nil {
			log.ErrorContext(ctx, "Error getting employees from scraper", "error", err)
			s.metrics.ScrapeErrors.WithLabelValues("employees", "scraper_error").Inc()
			return nil, status.Errorf(codes.Internal, "failed to get employee list")
		}
		return &pb.GetEmployeesResponse{
			NewHash:   currentHash,
			Employees: employees,
		}, nil
	}

	newFn := func() *pb.GetEmployeesResponse { return &pb.GetEmployeesResponse{} }

	response, err := getCachedOrFetch(ctx, s, "GetEmployees", "hermes:employees:all", employeesCacheTTL, fetchFn, newFn)
	if err != nil {
		return nil, fmt.Errorf("failed to work with cache data: %w", err)
	}

	if req.GetKnownHash() == response.GetNewHash() {
		log.InfoContext(ctx, "Hashes match. Returning empty employee list.")
		return &pb.GetEmployeesResponse{NewHash: response.GetNewHash()}, nil
	}

	log.InfoContext(ctx, "Hashes do not match. Returning full employee list.")
	return response, nil
}

func (s *Server) GetDailyTasks(ctx context.Context, req *pb.GetDailyTasksRequest) (*pb.GetDailyTasksResponse, error) {
	log := s.log.With("op", "GetDailyTask")
	log.InfoContext(ctx, "Received request", "known_hash", req.GetKnownHash())

	var targetDate time.Time
	var err error

	if req.GetDate() != nil && req.GetDate().GetValue() != "" {
		log.InfoContext(ctx, "Received GetDailyTasks request for date", "value", req.GetDate().GetValue())
		targetDate, err = time.Parse("2006-01-02", req.GetDate().GetValue())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid date format, please use YYYY-MM-DD")
		}
	} else {
		log.InfoContext(ctx, "Received request for today's date")
		targetDate = time.Now()
	}

	tasks, currentHash, err := s.scraper.GetDailyTasks(ctx, targetDate)
	if err != nil {
		log.ErrorContext(ctx, "Error getting tasks from scraper", "error", err)
		return nil, status.Errorf(codes.Internal, "failed to get tasks")
	}

	if req.GetKnownHash() == currentHash {
		log.InfoContext(ctx, "Hashes match. Returning empty task list.")
		return &pb.GetDailyTasksResponse{NewHash: currentHash}, nil
	}

	log.InfoContext(ctx, "Hashes do not match. Returning full task list.")
	return &pb.GetDailyTasksResponse{
		NewHash: currentHash,
		Tasks:   tasks,
	}, nil
}

func (s *Server) GetTaskTypes(ctx context.Context, req *pb.GetTaskTypesRequest) (*pb.GetTaskTypesResponse, error) {
	log := s.log.With("op", "GetTaskTypes")
	log.InfoContext(ctx, "Received request", "known_hash", req.GetKnownHash())

	fetchFn := func() (*pb.GetTaskTypesResponse, error) {
		taskTypes, currentHash, err := s.scraper.GetTaskTypes(ctx)
		if err != nil {
			s.metrics.ScrapeErrors.WithLabelValues("task_types", "scraper_error").Inc()
			return nil, status.Errorf(codes.Internal, "failed to get task types")
		}
		return &pb.GetTaskTypesResponse{
			NewHash: currentHash,
			Types:   taskTypes,
		}, nil
	}

	newFn := func() *pb.GetTaskTypesResponse { return &pb.GetTaskTypesResponse{} }

	response, err := getCachedOrFetch(ctx, s, "GetTaskTypes", "hermes:task_types:all", 1*time.Hour, fetchFn, newFn)
	if err != nil {
		return nil, fmt.Errorf("failed to work with cache data: %w", err)
	}

	if req.GetKnownHash() == response.GetNewHash() {
		s.log.InfoContext(ctx, "Hashes match. Returning empty task list.")
		return &pb.GetTaskTypesResponse{NewHash: response.GetNewHash()}, nil
	}

	return response, nil
}

func (s *Server) GetAgreements(ctx context.Context, req *pb.GetAgreementsRequest) (*pb.GetAgreementsResponse, error) {
	log := s.log.With("op", "GetAgreements")
	newFn := func() *pb.GetAgreementsResponse { return &pb.GetAgreementsResponse{} }

	var cacheKey string
	var fetchFn func() (*pb.GetAgreementsResponse, error)

	switch {
	case req.GetCustomerId() != 0:
		log.InfoContext(ctx, "Received request with id", "identifier", req.GetCustomerId())
		cacheKey = fmt.Sprintf("hermes:agreements:id:%d", req.GetCustomerId())
		fetchFn = func() (*pb.GetAgreementsResponse, error) {
			agreements, err := s.scraper.GetAgreementsByID(ctx, req.GetCustomerId())
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to get agreements by ID")
			}
			return &pb.GetAgreementsResponse{Agreements: agreements}, nil
		}
	case req.GetCustomerName() != "":
		log.InfoContext(ctx, "Received request with name", "fullname", req.GetCustomerName())
		cacheKey = fmt.Sprintf("hermes:agreements:name:%s", req.GetCustomerName())
		fetchFn = func() (*pb.GetAgreementsResponse, error) {
			agreements, err := s.scraper.GetAgreementsByName(ctx, req.GetCustomerName())
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to get agreements by name")
			}
			return &pb.GetAgreementsResponse{Agreements: agreements}, nil
		}
	default:
		return nil, status.Errorf(
			codes.InvalidArgument,
			"received invalid argument, id must be integer type, name must be not empty string",
		)
	}

	response, err := getCachedOrFetch(ctx, s, "GetAgreements", cacheKey, agreementsCacheTTL, fetchFn, newFn)
	if err != nil {
		return nil, fmt.Errorf("failed to work with cache data: %w", err)
	}

	return response, nil
}
