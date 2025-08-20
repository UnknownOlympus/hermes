package server_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/UnknownOlympus/hermes/internal/monitoring"
	"github.com/UnknownOlympus/hermes/internal/server"
	"github.com/UnknownOlympus/hermes/tests/mocks"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
	"github.com/go-redis/redismock/v9"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestGetEmployees(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	testMetrics := monitoring.NewMetrics(prometheus.NewRegistry())

	// --- Test Case 1: Cache Hit, hashes match ---
	t.Run("Success - Cache HIT, hashes match", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		cachedResp := &pb.GetEmployeesResponse{
			NewHash:   "same-hash",
			Employees: []*pb.Employee{{Id: 1, Fullname: "Cached John Doe"}},
		}
		cachedtbytes, err := proto.Marshal(cachedResp)
		require.NoError(t, err)
		mockRedis.ExpectGet("hermes:employees:all").SetVal(string(cachedtbytes))

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetEmployeesRequest{KnownHash: "same-hash"}
		resp, err := srv.GetEmployees(ctx, req)

		// ASSERT
		require.NoError(t, err)
		assert.Equal(t, "same-hash", resp.GetNewHash())
		assert.Empty(t, resp.GetEmployees())
		mockScraper.AssertNotCalled(t, "GetEmployees")
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 2: Cache Hit, hashes DON'T match ---
	t.Run("Success - Cache HIT, hashes do not match", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		cachedResp := &pb.GetEmployeesResponse{
			NewHash:   "new-cached-hash",
			Employees: []*pb.Employee{{Id: 1, Fullname: "Cached John Doe"}},
		}
		cachedtbytes, _ := proto.Marshal(cachedResp)
		mockRedis.ExpectGet("hermes:employees:all").SetVal(string(cachedtbytes))

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetEmployeesRequest{KnownHash: "old-hash"}
		resp, err := srv.GetEmployees(ctx, req)

		// ASSERT
		require.NoError(t, err)
		assert.Equal(t, "new-cached-hash", resp.GetNewHash())
		assert.NotEmpty(t, resp.GetEmployees())
		assert.Equal(t, "Cached John Doe", resp.GetEmployees()[0].GetFullname())
		mockScraper.AssertNotCalled(t, "GetEmployees")
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 3: Cache Miss, successful scrape ---
	t.Run("Success - Cache MISS, successful scrape", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		freshResp := &pb.GetEmployeesResponse{
			NewHash:   "fresh-hash",
			Employees: []*pb.Employee{{Id: 2, Fullname: "Fresh Jane Doe"}},
		}
		freshBytes, _ := proto.Marshal(freshResp)

		mockRedis.ExpectGet("hermes:employees:all").SetErr(redis.Nil)
		mockScraper.On("GetEmployees", ctx).Return(freshResp.GetEmployees(), freshResp.GetNewHash(), nil).Once()
		mockRedis.ExpectSet("hermes:employees:all", freshBytes, 10*time.Minute).SetVal("OK")

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetEmployeesRequest{KnownHash: "any-hash"}
		resp, err := srv.GetEmployees(ctx, req)

		// ASSERT
		require.NoError(t, err)
		assert.Equal(t, "fresh-hash", resp.GetNewHash())
		assert.NotEmpty(t, resp.GetEmployees())
		assert.Equal(t, "Fresh Jane Doe", resp.GetEmployees()[0].GetFullname())
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 4: Cache Miss, scraper fails ---
	t.Run("Failure - Cache MISS, scraper fails", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		mockRedis.ExpectGet("hermes:employees:all").SetErr(redis.Nil)
		mockScraper.On("GetEmployees", ctx).Return(nil, "", errors.New("internal scraper error")).Once()

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		_, err := srv.GetEmployees(ctx, &pb.GetEmployeesRequest{})

		// ASSERT
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.Internal, st.Code())
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})
}

func TestGetDailyTasks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	testMetrics := monitoring.NewMetrics(prometheus.NewRegistry())
	dtb, _ := redismock.NewClientMock()

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
			server := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

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
	testMetrics := monitoring.NewMetrics(prometheus.NewRegistry())

	// --- Test Case 1: Cache Hit, hashes match ---
	t.Run("Success - Cache HIT, hashes match", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		cachedResp := &pb.GetTaskTypesResponse{
			NewHash: "same-hash",
			Types:   []string{"bug", "test"},
		}
		cachedtbytes, err := proto.Marshal(cachedResp)
		require.NoError(t, err)
		mockRedis.ExpectGet("hermes:task_types:all").SetVal(string(cachedtbytes))

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetTaskTypesRequest{KnownHash: "same-hash"}
		resp, err := srv.GetTaskTypes(ctx, req)

		// ASSERT
		require.NoError(t, err)
		assert.Equal(t, "same-hash", resp.GetNewHash())
		assert.Empty(t, resp.GetTypes())
		mockScraper.AssertNotCalled(t, "GetTaskTypes")
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 2: Cache Hit, hashes DON'T match ---
	t.Run("Success - Cache HIT, hashes do not match", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		cachedResp := &pb.GetTaskTypesResponse{
			NewHash: "new-cached-hash",
			Types:   []string{"bug", "test"},
		}
		cachedtbytes, _ := proto.Marshal(cachedResp)
		mockRedis.ExpectGet("hermes:task_types:all").SetVal(string(cachedtbytes))

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetTaskTypesRequest{KnownHash: "old-hash"}
		resp, err := srv.GetTaskTypes(ctx, req)

		// ASSERT
		require.NoError(t, err)
		assert.Equal(t, "new-cached-hash", resp.GetNewHash())
		assert.NotEmpty(t, resp.GetTypes())
		assert.Equal(t, "bug", resp.GetTypes()[0])
		assert.Equal(t, "test", resp.GetTypes()[1])
		mockScraper.AssertNotCalled(t, "GetEmployees")
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 3: Cache Miss, successful scrape ---
	t.Run("Success - Cache MISS, successful scrape", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		freshResp := &pb.GetTaskTypesResponse{
			NewHash: "fresh-hash",
			Types:   []string{"bug", "test"},
		}
		freshBytes, _ := proto.Marshal(freshResp)

		mockRedis.ExpectGet("hermes:task_types:all").SetErr(redis.Nil)
		mockScraper.On("GetTaskTypes", ctx).Return(freshResp.GetTypes(), freshResp.GetNewHash(), nil).Once()
		mockRedis.ExpectSet("hermes:task_types:all", freshBytes, 1*time.Hour).SetVal("OK")

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetTaskTypesRequest{KnownHash: "any-hash"}
		resp, err := srv.GetTaskTypes(ctx, req)

		// ASSERT
		require.NoError(t, err)
		assert.Equal(t, "fresh-hash", resp.GetNewHash())
		assert.NotEmpty(t, resp.GetTypes())
		assert.Equal(t, "bug", resp.GetTypes()[0])
		assert.Equal(t, "test", resp.GetTypes()[1])
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 4: Cache Miss, scraper fails ---
	t.Run("Failure - Cache MISS, scraper fails", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		mockRedis.ExpectGet("hermes:task_types:all").SetErr(redis.Nil)
		mockScraper.On("GetTaskTypes", ctx).Return(nil, "", errors.New("internal scraper error")).Once()

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		_, err := srv.GetTaskTypes(ctx, &pb.GetTaskTypesRequest{})

		// ASSERT
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.Internal, st.Code())
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})
}

func TestGetAgreements(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	testMetrics := monitoring.NewMetrics(prometheus.NewRegistry())
	testAgreement := pb.Agreement{
		Id:       1,
		Ip:       "192.168.0.1",
		Name:     "John Doe",
		Contract: "#12345",
		Balance:  "100",
		Tariff:   "optimal",
		Address:  "Canada",
		Number:   "555-555-55-55",
	}

	// --- Test Case 1: Cache Hit ---
	t.Run("Success - Cache HIT, request by ID", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		cachedResp := &pb.GetAgreementsResponse{
			Agreements: []*pb.Agreement{&testAgreement},
		}
		cachedtbytes, err := proto.Marshal(cachedResp)
		require.NoError(t, err)
		mockRedis.ExpectGet(fmt.Sprintf("hermes:agreements:id:%d", testAgreement.GetId())).SetVal(string(cachedtbytes))

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetAgreementsRequest{Identifier: &pb.GetAgreementsRequest_CustomerId{CustomerId: testAgreement.GetId()}}
		resp, err := srv.GetAgreements(ctx, req)

		// ASSERT
		require.NoError(t, err)
		actualAgreement := resp.GetAgreements()[0]
		assert.Equal(t, testAgreement.GetId(), actualAgreement.GetId())
		assert.Equal(t, testAgreement.GetIp(), actualAgreement.GetIp())
		assert.Equal(t, testAgreement.GetName(), actualAgreement.GetName())
		assert.Equal(t, testAgreement.GetContract(), actualAgreement.GetContract())
		assert.Equal(t, testAgreement.GetBalance(), actualAgreement.GetBalance())
		assert.Equal(t, testAgreement.GetAddress(), actualAgreement.GetAddress())
		assert.Equal(t, testAgreement.GetNumber(), actualAgreement.GetNumber())
		mockScraper.AssertNotCalled(t, "GetAgreementsByID")
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 2: Cache Hit, request by name ---
	t.Run("Success - Cache HIT, request by name", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		cachedResp := &pb.GetAgreementsResponse{
			Agreements: []*pb.Agreement{&testAgreement},
		}
		cachedtbytes, err := proto.Marshal(cachedResp)
		require.NoError(t, err)
		mockRedis.ExpectGet(fmt.Sprintf("hermes:agreements:name:%s", testAgreement.GetName())).SetVal(string(cachedtbytes))

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetAgreementsRequest{Identifier: &pb.GetAgreementsRequest_CustomerName{CustomerName: testAgreement.GetName()}}
		resp, err := srv.GetAgreements(ctx, req)

		// ASSERT
		require.NoError(t, err)
		actualAgreement := resp.GetAgreements()[0]
		assert.Equal(t, testAgreement.GetId(), actualAgreement.GetId())
		assert.Equal(t, testAgreement.GetIp(), actualAgreement.GetIp())
		assert.Equal(t, testAgreement.GetName(), actualAgreement.GetName())
		assert.Equal(t, testAgreement.GetContract(), actualAgreement.GetContract())
		assert.Equal(t, testAgreement.GetBalance(), actualAgreement.GetBalance())
		assert.Equal(t, testAgreement.GetAddress(), actualAgreement.GetAddress())
		assert.Equal(t, testAgreement.GetNumber(), actualAgreement.GetNumber())
		mockScraper.AssertNotCalled(t, "GetAgreementsByName")
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 3: Cache Miss, successful scrape ---
	t.Run("Success - Cache MISS, successful scrape for ID", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		freshResp := &pb.GetAgreementsResponse{
			Agreements: []*pb.Agreement{&testAgreement},
		}
		freshBytes, _ := proto.Marshal(freshResp)

		mockRedis.ExpectGet(fmt.Sprintf("hermes:agreements:id:%d", testAgreement.GetId())).SetErr(redis.Nil)
		mockScraper.On("GetAgreementsByID", ctx, testAgreement.GetId()).Return(freshResp.GetAgreements(), nil).Once()
		mockRedis.ExpectSet(fmt.Sprintf("hermes:agreements:id:%d", testAgreement.GetId()), freshBytes, 6*time.Hour).SetVal("OK")

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetAgreementsRequest{Identifier: &pb.GetAgreementsRequest_CustomerId{CustomerId: testAgreement.GetId()}}
		resp, err := srv.GetAgreements(ctx, req)

		// ASSERT
		require.NoError(t, err)
		actualAgreement := resp.GetAgreements()[0]
		assert.Equal(t, testAgreement.GetId(), actualAgreement.GetId())
		assert.Equal(t, testAgreement.GetIp(), actualAgreement.GetIp())
		assert.Equal(t, testAgreement.GetName(), actualAgreement.GetName())
		assert.Equal(t, testAgreement.GetContract(), actualAgreement.GetContract())
		assert.Equal(t, testAgreement.GetBalance(), actualAgreement.GetBalance())
		assert.Equal(t, testAgreement.GetAddress(), actualAgreement.GetAddress())
		assert.Equal(t, testAgreement.GetNumber(), actualAgreement.GetNumber())
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	t.Run("Success - Cache MISS, successful scrape for name", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		freshResp := &pb.GetAgreementsResponse{
			Agreements: []*pb.Agreement{&testAgreement},
		}
		freshBytes, _ := proto.Marshal(freshResp)

		mockRedis.ExpectGet(fmt.Sprintf("hermes:agreements:name:%s", testAgreement.GetName())).SetErr(redis.Nil)
		mockScraper.On("GetAgreementsByName", ctx, testAgreement.GetName()).Return(freshResp.GetAgreements(), nil).Once()
		mockRedis.ExpectSet(fmt.Sprintf("hermes:agreements:name:%s", testAgreement.GetName()), freshBytes, 6*time.Hour).SetVal("OK")

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		req := &pb.GetAgreementsRequest{Identifier: &pb.GetAgreementsRequest_CustomerName{CustomerName: testAgreement.GetName()}}
		resp, err := srv.GetAgreements(ctx, req)

		// ASSERT
		require.NoError(t, err)
		actualAgreement := resp.GetAgreements()[0]
		assert.Equal(t, testAgreement.GetId(), actualAgreement.GetId())
		assert.Equal(t, testAgreement.GetIp(), actualAgreement.GetIp())
		assert.Equal(t, testAgreement.GetName(), actualAgreement.GetName())
		assert.Equal(t, testAgreement.GetContract(), actualAgreement.GetContract())
		assert.Equal(t, testAgreement.GetBalance(), actualAgreement.GetBalance())
		assert.Equal(t, testAgreement.GetAddress(), actualAgreement.GetAddress())
		assert.Equal(t, testAgreement.GetNumber(), actualAgreement.GetNumber())
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 4: Cache Miss, scraper fails for ID ---
	t.Run("Failure - Cache MISS, scraper fails for ID", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		mockRedis.ExpectGet(fmt.Sprintf("hermes:agreements:id:%d", testAgreement.GetId())).SetErr(redis.Nil)
		mockScraper.On("GetAgreementsByID", ctx, testAgreement.GetId()).Return(nil, errors.New("internal scraper error")).Once()

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		_, err := srv.GetAgreements(ctx, &pb.GetAgreementsRequest{Identifier: &pb.GetAgreementsRequest_CustomerId{CustomerId: testAgreement.GetId()}})

		// ASSERT
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.Internal, st.Code())
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 5: Cache Miss, scraper fails for name ---
	t.Run("Failure - Cache MISS, scraper fails for name", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		mockRedis.ExpectGet(fmt.Sprintf("hermes:agreements:name:%s", testAgreement.GetName())).SetErr(redis.Nil)
		mockScraper.On("GetAgreementsByName", ctx, testAgreement.GetName()).Return(nil, errors.New("internal scraper error")).Once()

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		_, err := srv.GetAgreements(ctx, &pb.GetAgreementsRequest{Identifier: &pb.GetAgreementsRequest_CustomerName{CustomerName: testAgreement.GetName()}})

		// ASSERT
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.Internal, st.Code())
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})

	// --- Test Case 6: invalid argument ---
	t.Run("Failure - Cache MISS, scraper fails for name", func(t *testing.T) {
		// ARRANGE
		mockScraper := mocks.NewScraperIface(t)
		dtb, mockRedis := redismock.NewClientMock()

		srv := server.NewGRPCServer(logger, dtb, mockScraper, testMetrics)

		// ACT
		_, err := srv.GetAgreements(ctx, &pb.GetAgreementsRequest{})

		// ASSERT
		require.Error(t, err)
		st, ok := status.FromError(err)
		require.True(t, ok)
		assert.Equal(t, codes.InvalidArgument, st.Code())
		mockScraper.AssertExpectations(t)
		require.NoError(t, mockRedis.ExpectationsWereMet())
	})
}
