package server_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/UnknownOlympus/hermes/internal/server"
	"github.com/UnknownOlympus/hermes/tests/mocks"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestGetEmployees(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	testCases := []struct {
		name            string
		req             *pb.GetEmployeesRequest
		setupMock       func(m *mocks.ScraperIface) // Use the generated mock type
		expectedResp    *pb.GetEmployeesResponse
		expectedErrCode codes.Code
	}{
		{
			name: "Success - Hashes do not match, full list returned",
			req:  &pb.GetEmployeesRequest{KnownHash: "old-hash"},
			setupMock: func(m *mocks.ScraperIface) {
				// Define expectation: when GetEmployees is called with this context...
				m.On("GetEmployees", ctx).
					// ...it should return these values.
					Return([]*pb.Employee{{Id: 1, Fullname: "John Doe"}}, "new-hash", nil).
					// We expect this call to happen exactly once.
					Once()
			},
			expectedResp: &pb.GetEmployeesResponse{
				NewHash:   "new-hash",
				Employees: []*pb.Employee{{Id: 1, Fullname: "John Doe"}},
			},
			expectedErrCode: codes.OK,
		},
		{
			name: "Success - Hashes match, empty list returned",
			req:  &pb.GetEmployeesRequest{KnownHash: "same-hash"},
			setupMock: func(m *mocks.ScraperIface) {
				m.On("GetEmployees", ctx).
					Return([]*pb.Employee{{Id: 1, Fullname: "John Doe"}}, "same-hash", nil).
					Once()
			},
			expectedResp: &pb.GetEmployeesResponse{
				NewHash:   "same-hash",
				Employees: nil,
			},
			expectedErrCode: codes.OK,
		},
		{
			name: "Failure - Scraper returns an error",
			req:  &pb.GetEmployeesRequest{KnownHash: "any-hash"},
			setupMock: func(m *mocks.ScraperIface) {
				m.On("GetEmployees", ctx).
					Return(nil, "", errors.New("internal scraper error")).
					Once()
			},
			expectedResp:    nil,
			expectedErrCode: codes.Internal,
		},
	}

	for _, tce := range testCases {
		t.Run(tce.name, func(t *testing.T) {
			// ARRANGE
			mockScraper := mocks.NewScraperIface(t)
			if tce.setupMock != nil {
				tce.setupMock(mockScraper)
			}
			server := server.NewGRPCServer(logger, mockScraper)

			// ACT
			resp, err := server.GetEmployees(ctx, tce.req)

			// ASSERT
			if tce.expectedErrCode == codes.OK {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, tce.expectedResp.GetNewHash(), resp.GetNewHash())
				assert.ElementsMatch(t, tce.expectedResp.GetEmployees(), resp.GetEmployees())
			} else {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok)
				assert.Equal(t, tce.expectedErrCode, st.Code())
			}

			mockScraper.AssertExpectations(t)
		})
	}
}

func TestGetDailyTasks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	testCases := []struct {
		name            string
		req             *pb.GetDailyTasksRequest
		setupMock       func(m *mocks.ScraperIface)
		expectedErrCode codes.Code
	}{
		{
			name: "Success - Hashes do not match for specific date",
			req: &pb.GetDailyTasksRequest{
				KnownHash: "old-hash",
				Date:      wrapperspb.String("2024-01-15"),
			},
			setupMock: func(m *mocks.ScraperIface) {
				// Use mock.AnythingOfType to match any time.Time argument.
				// This avoids fragile tests that depend on the exact time.
				m.On("GetDailyTasks", ctx, mock.AnythingOfType("time.Time")).
					// NOTE: Returning []*pb.Task to match the interface
					Return([]*pb.Task{{Description: "Test Task"}}, "new-date-hash", nil).
					Once()
			},
			expectedErrCode: codes.OK,
		},
		{
			name: "Success - Hashes match for today's date",
			req:  &pb.GetDailyTasksRequest{KnownHash: "today-hash"},
			setupMock: func(m *mocks.ScraperIface) {
				m.On("GetDailyTasks", ctx, mock.AnythingOfType("time.Time")).
					Return(nil, "today-hash", nil).
					Once()
			},
			expectedErrCode: codes.OK,
		},
		{
			name: "Failure - Invalid date format",
			req: &pb.GetDailyTasksRequest{
				Date: wrapperspb.String("15-01-2024"),
			},
			// Scraper method should NOT be called, so we don't set any expectations.
			// mockery will fail the test if GetDailyTasks is called unexpectedly.
			setupMock:       nil,
			expectedErrCode: codes.InvalidArgument,
		},
		{
			name: "Failure - Scraper returns an error",
			req:  &pb.GetDailyTasksRequest{},
			setupMock: func(m *mocks.ScraperIface) {
				m.On("GetDailyTasks", ctx, mock.AnythingOfType("time.Time")).
					Return(nil, "", assert.AnError).
					Once()
			},
			expectedErrCode: codes.Internal,
		},
	}

	for _, tce := range testCases {
		t.Run(tce.name, func(t *testing.T) {
			// ARRANGE
			mockScraper := mocks.NewScraperIface(t)
			if tce.setupMock != nil {
				tce.setupMock(mockScraper)
			}
			server := server.NewGRPCServer(logger, mockScraper)

			// ACT
			_, err := server.GetDailyTasks(ctx, tce.req)

			// ASSERT
			if tce.expectedErrCode == codes.OK {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok)
				assert.Equal(t, tce.expectedErrCode, st.Code())
			}

			mockScraper.AssertExpectations(t)
		})
	}
}

func TestGetTaskTypes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	testCases := []struct {
		name            string
		req             *pb.GetTaskTypesRequest
		setupMock       func(m *mocks.ScraperIface)
		expectedResp    *pb.GetTaskTypesResponse
		expectedErrCode codes.Code
	}{
		{
			name: "Success - Hashes do not match",
			req:  &pb.GetTaskTypesRequest{KnownHash: "old-hash"},
			setupMock: func(m *mocks.ScraperIface) {
				// NOTE: Returning []string to match the interface
				m.On("GetTaskTypes", ctx).
					Return([]string{"Coding", "Meeting"}, "new-types-hash", nil).
					Once()
			},
			expectedResp: &pb.GetTaskTypesResponse{
				NewHash: "new-types-hash",
				Types:   []string{"Coding", "Meeting"},
			},
			expectedErrCode: codes.OK,
		},
		{
			name: "Success - Hashes match, empty list returned",
			req:  &pb.GetTaskTypesRequest{KnownHash: "same-hash"},
			setupMock: func(m *mocks.ScraperIface) {
				m.On("GetTaskTypes", ctx).
					Return([]string{"Coding", "Meeting"}, "same-hash", nil).
					Once()
			},
			expectedResp: &pb.GetTaskTypesResponse{
				NewHash: "same-hash",
				Types:   nil,
			},
			expectedErrCode: codes.OK,
		},
		{
			name: "Failure - Scraper returns an error",
			req:  &pb.GetTaskTypesRequest{},
			setupMock: func(m *mocks.ScraperIface) {
				m.On("GetTaskTypes", ctx).
					Return(nil, "", errors.New("scraper error")).
					Once()
			},
			expectedResp:    nil,
			expectedErrCode: codes.Internal,
		},
	}

	for _, tce := range testCases {
		t.Run(tce.name, func(t *testing.T) {
			// ARRANGE
			mockScraper := mocks.NewScraperIface(t)
			if tce.setupMock != nil {
				tce.setupMock(mockScraper)
			}

			server := server.NewGRPCServer(logger, mockScraper)

			// ACT
			resp, err := server.GetTaskTypes(ctx, tce.req)

			// ASSERT
			if tce.expectedErrCode == codes.OK {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, tce.expectedResp.GetNewHash(), resp.GetNewHash())
				assert.ElementsMatch(t, tce.expectedResp.GetTypes(), resp.GetTypes())
			} else {
				require.Error(t, err)
				st, ok := status.FromError(err)
				require.True(t, ok)
				assert.Nil(t, resp)
				assert.Equal(t, tce.expectedErrCode, st.Code())
			}
			mockScraper.AssertExpectations(t)
		})
	}
}
