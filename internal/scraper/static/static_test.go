// static_test/static_test.go
package static_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/UnknownOlympus/hermes/internal/config"
	"github.com/UnknownOlympus/hermes/internal/monitoring"
	"github.com/UnknownOlympus/hermes/internal/scraper/static"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mockLoginSuccessPage = `<html><head><title>Main Page</title></head></html>`
const mockLoginPage = `<html><head><title>Login Page</title></head></html>`

const mockActiveTasksHTML = `
<table><tr tag="row_1">
	<td></td><td></td><td></td><td></td>
	<td><a href="#">Comment 1</a><br/><i>info</i></td> <td></td>
	<td><a href="#">101</a></td> <td>01.08.2025 </td> <td>Some Address 1</td> <td><a onmouseover="break_href = 1;" onmouseout="break_href = 0;" href="?core_section=customer&action=show&id=123" target="_blank">Test User - testLogin</a></td> <td></td>
	<td><b>Task Type A</b><div class="div_journal_opis">Description A</div></td> <td>Executor A<br/>Executor B</td> </tr></table>`

const mockCompletedTasksHTML = `
<table><tr tag="row_2">
	<td></td><td></td><td></td><td></td>
	<td><a href="#">Comment 2</a></td> <td></td>
	<td><a href="#">202</a></td> <td>02.08.2025</td> <td>03.08.2025</td> <td>Some Address 2</td> <td>Just a customer</td> <td></td>
	<td><b>Task Type B</b><div class="div_journal_opis">Description B</div></td> <td>Executor C</td> </tr></table>`

const mockTaskTypesHTML = `
<div>
    <a title="Добавить задание">Type 1</a>
    <a title="Добавить задание">Type 2</a>
</div>`

const mockActiveStaffHTML = `
<table><tr tag="row_1">
    <td></td>
    <td><input value="10"></td> <td>John Doe</td> <td>Developer</td> <td>john.doe@example.com</td> <td>111-222-333</td> </tr></table>`

const mockDismissedStaffHTML = `
<table><tr tag="row_2">
    <td></td><td></td>
    <td><input value="20"></td> <td>Jane Smith</td> <td>Manager</td> <td>jane.smith@example.com</td> <td>444-555-666</td> </tr></table>`

const mockStaffShortnamesHTML = `
<div>
    <div class="div_space"><a href="...&id=10&...">John D.</a></div>
    <div class="div_space"><a href="...&id=20&...">Jane S.</a></div>
</div>`

func TestScraper_NewScraper(t *testing.T) {
	t.Run("successful login", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, mockLoginSuccessPage)
			}
		}))
		defer server.Close()

		cfg := &config.Config{LoginURL: server.URL}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		testRegistry := prometheus.NewRegistry()
		testMetrics := monitoring.NewMetrics(testRegistry)
		scraper, err := static.NewScraper(cfg, logger, testMetrics)

		require.NoError(t, err)
		assert.NotNil(t, scraper)
	})

	t.Run("failed login with retries", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping integration test in short mode.")
		}
		loginAttempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				loginAttempts++
				w.Header().Set("Location", "exampl.com/login")
				w.WriteHeader(http.StatusFound)
				fmt.Fprint(w, mockLoginPage)
			}
		}))
		defer server.Close()

		loginURLWithFailPath := server.URL + "/login"

		cfg := &config.Config{LoginURL: loginURLWithFailPath}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))

		_, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		testRegistry := prometheus.NewRegistry()
		testMetrics := monitoring.NewMetrics(testRegistry)
		scraper, err := static.NewScraper(cfg, logger, testMetrics)

		require.Error(t, err)
		assert.Nil(t, scraper)
	})
}

func TestScraper_GetDailyTasks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()
		if params.Get("core_section") == "task_list" && params.Get("task_group1_value") == "3" {
			if params.Get("task_state0_value") == "1" { // Active
				writer.Write([]byte(mockActiveTasksHTML))
				return
			}
			if params.Get("task_state0_value") == "2" { // Completed
				writer.Write([]byte(mockCompletedTasksHTML))
				return
			}
		}
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	require.NoError(t, err)

	tasks, _, err := scraper.GetDailyTasks(t.Context(), time.Now())
	require.NoError(t, err)

	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	taskMap := make(map[int64]*pb.Task)
	for _, task := range tasks {
		taskMap[task.GetId()] = task
	}

	activeTask, exists := taskMap[101]
	assert.True(t, exists)
	assert.NotNil(t, activeTask.GetCustomers())
	assert.Equal(t, "Test User", activeTask.GetCustomers()[0].GetName())
	assert.Equal(t, "testLogin", activeTask.GetCustomers()[0].GetLogin())
	assert.False(t, activeTask.GetIsClosed())
	assert.Equal(t, []string{"Executor A", "Executor B"}, activeTask.GetExecutors())

	completedTask, ok := taskMap[202]
	assert.True(t, ok)
	assert.Equal(t, "Just a customer", completedTask.GetCustomers()[0].GetName())
	assert.Equal(t, "n/a", completedTask.GetCustomers()[0].GetLogin())
	assert.True(t, completedTask.GetIsClosed())
	assert.NotNil(t, completedTask.GetClosingDate())
}

func TestScraper_GetEmployees(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()
		if params.Get("core_section") == "staff_unit" {
			if params.Get("is_with_leaved") == "1" {
				writer.Write([]byte(mockDismissedStaffHTML))
			} else {
				writer.Write([]byte(mockActiveStaffHTML))
			}
			return
		}
		if params.Get("core_section") == "staff" && params.Get("action") == "division" {
			writer.Write([]byte(mockStaffShortnamesHTML))
			return
		}
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	if err != nil {
		t.Fatalf("setup failed: could not create scraper: %v", err)
	}

	employees, _, err := scraper.GetEmployees(t.Context())
	if err != nil {
		t.Fatalf("GetEmployees failed: %v", err)
	}

	if len(employees) != 2 {
		t.Fatalf("expected 2 employees, got %d", len(employees))
	}

	empMap := make(map[int64]*pb.Employee)
	for _, e := range employees {
		empMap[e.GetId()] = e
	}

	john, exists := empMap[10]
	if !exists || john.GetFullname() != "John Doe" || john.GetShortname() != "John D." {
		t.Errorf("failed to parse John Doe correctly: %+v", john)
	}

	jane, exists := empMap[20]
	if !exists || jane.GetFullname() != "Jane Smith" || jane.GetShortname() != "Jane S." {
		t.Errorf("failed to parse Jane Smith correctly: %+v", jane)
	}
}

func TestScraper_GetTaskTypes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()
		if params.Get("core_section") == "task" && params.Get("action") == "group_task_type_list" {
			writer.Write([]byte(mockTaskTypesHTML))
			return
		}
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	if err != nil {
		t.Fatalf("setup failed: could not create scraper: %v", err)
	}

	taskTypes, _, err := scraper.GetTaskTypes(t.Context())
	if err != nil {
		t.Fatalf("GetTaskTypes failed: %v", err)
	}

	expectedTypes := []string{"Type 1", "Type 1", "Type 1", "Type 2", "Type 2", "Type 2"}
	if !reflect.DeepEqual(taskTypes, expectedTypes) {
		t.Errorf("expected task types %v, got %v", expectedTypes, taskTypes)
	}
}

const mockAgreementsHTML = `
<table><tr tag="row_1">
	<td></td>
	<td><input type="checkbox" name="object_ids[]" value="48111""></td>
	<td><a>Canada, test street </a><br>555-555-55-55</td>
	<td>192.168.0.1</td>
	<td>#0825-1245<br>2025-08-19</td>
	<td>John Doe</td>
	<td>19.08.2025</td>
	<td></td>
	<td>290.00</td>
	<td>200M - PON - 30 - 30$</td>
	<td>-</td></tr></table>`

func TestGetAgreementsByID_success(t *testing.T) {
	// init mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()
		if params.Get("core_section") == "customer_list" {
			writer.Write([]byte(mockAgreementsHTML))
			return
		}
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// init scraper and deps
	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	require.NoError(t, err)

	agreements, err := scraper.GetAgreementsByID(t.Context(), 48111)

	require.NoError(t, err)
	actualAgreement := agreements[0]
	assert.Equal(t, int64(48111), actualAgreement.GetId())
	assert.Equal(t, "Canada, test street", actualAgreement.GetAddress())
	assert.Equal(t, "555-555-55-55", actualAgreement.GetNumber())
	assert.Equal(t, "192.168.0.1", actualAgreement.GetIp())
	assert.Equal(t, "#0825-1245", actualAgreement.GetContract())
	assert.Equal(t, "John Doe", actualAgreement.GetName())
	assert.Equal(t, "290.00", actualAgreement.GetBalance())
	assert.Equal(t, "200M - PON - 30", actualAgreement.GetTariff())
}

func TestGetAgreementsByID_formDataError(t *testing.T) {
	// init mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// init scraper and deps
	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	require.NoError(t, err)

	_, err = scraper.GetAgreementsByID(t.Context(), 0)

	require.Error(t, err)
	require.ErrorIs(t, err, static.ErrNoParameters)
}

func TestGetAgreementsByID_scrapeError(t *testing.T) {
	// init mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()
		if params.Get("core_section") == "customer_list" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// init scraper and deps
	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	require.NoError(t, err)

	_, err = scraper.GetAgreementsByID(t.Context(), 48111)

	require.Error(t, err)
	require.ErrorContains(t, err, "failed to scrape agreements data")
}

func TestGetAgreementsByName(t *testing.T) {
	// init mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()
		if params.Get("core_section") == "customer_list" {
			writer.Write([]byte(mockAgreementsHTML))
			return
		}
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// init scraper and deps
	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	require.NoError(t, err)

	agreements, err := scraper.GetAgreementsByName(t.Context(), "John Doe")

	require.NoError(t, err)
	actualAgreement := agreements[0]
	assert.Equal(t, int64(48111), actualAgreement.GetId())
	assert.Equal(t, "Canada, test street", actualAgreement.GetAddress())
	assert.Equal(t, "555-555-55-55", actualAgreement.GetNumber())
	assert.Equal(t, "192.168.0.1", actualAgreement.GetIp())
	assert.Equal(t, "#0825-1245", actualAgreement.GetContract())
	assert.Equal(t, "John Doe", actualAgreement.GetName())
	assert.Equal(t, "290.00", actualAgreement.GetBalance())
	assert.Equal(t, "200M - PON - 30", actualAgreement.GetTariff())
}

func TestGetAgreementsByName_formDataError(t *testing.T) {
	// init mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// init scraper and deps
	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	require.NoError(t, err)

	_, err = scraper.GetAgreementsByName(t.Context(), "")

	require.Error(t, err)
	require.ErrorIs(t, err, static.ErrNoParameters)
}

func TestGetAgreementsByName_scrapeError(t *testing.T) {
	// init mock server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()
		if params.Get("core_section") == "customer_list" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// init scraper and deps
	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	testRegistry := prometheus.NewRegistry()
	testMetrics := monitoring.NewMetrics(testRegistry)
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	require.NoError(t, err)

	_, err = scraper.GetAgreementsByName(t.Context(), "John Doe")

	require.Error(t, err)
	require.ErrorContains(t, err, "failed to scrape agreements data")
}

func TestParseIDFromRow(t *testing.T) {
	testCases := []struct {
		name        string
		html        string
		expectedID  int64
		expectedErr error
	}{
		{
			name:        "Success",
			html:        `<table><tr><td><input type="checkbox" name="id" value="12345"></td></tr></table>`,
			expectedID:  12345,
			expectedErr: nil,
		},
		{
			name:        "Checkbox not found",
			html:        `<table><tr><td>Just text, without checkbox</td></tr></table>`,
			expectedID:  0,
			expectedErr: static.ErrBoxNotFound,
		},
		{
			name:        "Attribute value is not exists",
			html:        `<table><tr><td><input type="checkbox" name="id"></td></tr></table>`,
			expectedID:  0,
			expectedErr: static.ErrEmptyValue,
		},
		{
			name:        "Attribute value is empty",
			html:        `<table><tr><td><input type="checkbox" name="id" value=""></td></tr></table>`,
			expectedID:  0,
			expectedErr: static.ErrEmptyValue,
		},
		{
			name:        "Value is not an integer",
			html:        `<table><tr><td><input type="checkbox" name="id" value="not-a-number"></td></tr></table>`,
			expectedID:  0,
			expectedErr: &strconv.NumError{},
		},
		{
			name:        "Too big integer",
			html:        `<table><tr><td><input type="checkbox" name="id" value="9223372036854775808"></td></tr></table>`,
			expectedID:  0,
			expectedErr: &strconv.NumError{},
		},
	}

	for _, tce := range testCases {
		t.Run(tce.name, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(tce.html))
			require.NoError(t, err)

			rowSelection := doc.Find(`tr`)

			id, err := static.ParseIDFromRow(rowSelection)

			assert.Equal(t, tce.expectedID, id)

			if tce.expectedErr == nil {
				if err != nil {
					t.Errorf("expected no error, but got: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error '%v', but got nil", tce.expectedErr)
				return
			}

			var numErr *strconv.NumError
			if errors.As(err, &numErr) {
				return
			} else if !errors.Is(err, tce.expectedErr) {
				t.Errorf("expected error %v, but got %v", tce.expectedErr, err)
			}
		})
	}
}

func TestAddComment(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	testMetrics := monitoring.NewMetrics(prometheus.NewRegistry())
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			switch req.URL.Path {
			case "/login":
				assert.Equal(t, http.MethodPost, req.Method)
				http.Redirect(writer, req, "/dashboard", http.StatusFound)

			case "/dashboard":
				writer.WriteHeader(http.StatusOK)

			case "/add-comment":
				assert.Equal(t, http.MethodPost, req.Method)
				req.ParseForm()
				assert.Equal(t, "78357", req.FormValue("id"))
				assert.Equal(t, "hello world", req.FormValue("opis"))
				writer.WriteHeader(http.StatusOK)

			default:
				t.Fatalf("Received unexpected request to path: %s", req.URL.Path)
			}
		}))
		defer server.Close()

		cfg := &config.Config{
			LoginURL:  server.URL + "/login",
			TargetURL: server.URL + "/add-comment",
		}
		scraper, err := static.NewScraper(cfg, logger, testMetrics)
		require.NoError(t, err)

		// ACT
		err = scraper.AddComment(ctx, 78357, "hello world")

		// ASSERT
		require.NoError(t, err)
	})

	t.Run("server returns error on comment post", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/login":
				assert.Equal(t, http.MethodPost, r.Method)
				http.Redirect(writer, r, "/dashboard", http.StatusFound)

			case "/dashboard":
				writer.WriteHeader(http.StatusOK)

			default:
				writer.WriteHeader(http.StatusInternalServerError)
			}
		}))
		defer server.Close()

		cfg := &config.Config{LoginURL: server.URL + "/login", TargetURL: server.URL}
		scraper, err := static.NewScraper(cfg, logger, testMetrics)
		require.NoError(t, err)

		err = scraper.AddComment(ctx, 123, "any comment")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed with status: 500")
	})
}

func TestGetCommentsFromTask(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	testMetrics := monitoring.NewMetrics(prometheus.NewRegistry())
	ctx := context.Background()

	mockHTML := `
	<html><body>
		<div id="comment2_48500_id" class="j_card_comment_div">
			<span id="comment_48500_id">first comment</span>
			<div style="font-weight: bold;">John Doe</div>
			<div style="float: right;">23.08.2025 14:25</div>
		</div>
		<div id="comment2_48501_id" class="j_card_comment_div">
			<span id="comment_48501_id">second comment</span>
			<div style="font-weight: bold;">Jane Doe</div>
			<div style="float: right;">23.08.2025 14:27</div>
		</div>
	</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			writer.WriteHeader(http.StatusOK)
			writer.Write([]byte(mockLoginSuccessPage))
			return
		}

		assert.Equal(t, "show", req.URL.Query().Get("action"))
		assert.Equal(t, "78357", req.URL.Query().Get("id"))
		writer.WriteHeader(http.StatusOK)
		fmt.Fprint(writer, mockHTML)
	}))
	defer server.Close()

	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	scraper, err := static.NewScraper(cfg, logger, testMetrics)
	require.NoError(t, err)

	comments, err := scraper.GetCommentsFromTask(ctx, 78357)

	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, "first comment", comments[0])
	assert.Equal(t, "second comment", comments[1])
}
