package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/UnknownOlympus/hermes/internal/scraper/static"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedScraperServiceServer
	scraper static.ScraperIface
	log     *slog.Logger
}

func NewGRPCServer(logger *slog.Logger, scraper static.ScraperIface) *Server {
	return &Server{log: logger, scraper: scraper}
}

func (s *Server) GetEmployees(ctx context.Context, req *pb.GetEmployeesRequest) (*pb.GetEmployeesResponse, error) {
	log := s.log.With("op", "GetEmployees")
	log.InfoContext(ctx, "Received request", "known_hash", req.GetKnownHash())

	employees, currentHash, err := s.scraper.GetEmployees(ctx)
	if err != nil {
		log.ErrorContext(ctx, "Error getting employees from scraper", "error", err)
		return nil, status.Errorf(codes.Internal, "failed to get employee list")
	}

	if req.GetKnownHash() == currentHash {
		log.InfoContext(ctx, "Hashes match. Returning empty employee list.")
		return &pb.GetEmployeesResponse{NewHash: currentHash}, nil
	}

	log.InfoContext(ctx, "Hashes do not match. Returning full employee list.")
	return &pb.GetEmployeesResponse{
		NewHash:   currentHash,
		Employees: employees,
	}, nil
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

	taskTypes, currentHash, err := s.scraper.GetTaskTypes(ctx)
	if err != nil {
		log.ErrorContext(ctx, "Error getting task types from scraper", "error", err)
		return nil, status.Errorf(codes.Internal, "failed to get task types")
	}

	if req.GetKnownHash() == currentHash {
		s.log.InfoContext(ctx, "Hashes match. Returning empty task list.")
		return &pb.GetTaskTypesResponse{NewHash: currentHash}, nil
	}

	return &pb.GetTaskTypesResponse{
		Types:   taskTypes,
		NewHash: currentHash,
	}, nil
}
