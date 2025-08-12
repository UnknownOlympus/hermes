// static_test/static_test.go
package static_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/UnknownOlympus/hermes/internal/config"
	"github.com/UnknownOlympus/hermes/internal/scraper/static"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mockLoginSuccessPage = `<html><head><title>Main Page</title></head></html>`
const mockLoginPage = `<html><head><title>Login Page</title></head></html>`

const mockActiveTasksHTML = `
<table><tr tag="row_1">
	<td></td><td></td><td></td><td></td>
	<td><a href="#">Comment 1</a><br/><i>info</i></td> <td></td>
	<td><a href="#">101</a></td> <td>01.08.2025 </td> <td>Some Address 1</td> <td><a> Test User - testlogin </a></td> <td></td>
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
		scraper, err := static.NewScraper(cfg, logger)

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

		scraper, err := static.NewScraper(cfg, logger)

		require.Error(t, err)
		assert.Nil(t, scraper)
	})
}

func TestScraper_GetDailyTasks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		if params.Get("core_section") == "task_list" {
			if params.Get("task_state0_value") == "1" { // Active
				w.Write([]byte(mockActiveTasksHTML))
				return
			}
			if params.Get("task_state0_value") == "2" { // Completed
				w.Write([]byte(mockCompletedTasksHTML))
				return
			}
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(mockLoginSuccessPage))
			return
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scraper, err := static.NewScraper(cfg, logger)
	require.NoError(t, err)

	tasks, _, err := scraper.GetDailyTasks(context.Background(), time.Now())
	require.NoError(t, err)

	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	taskMap := make(map[int64]*pb.Task)
	for _, task := range tasks {
		taskMap[task.GetId()] = task
	}

	activeTask, ok := taskMap[101]
	assert.True(t, ok)
	assert.Equal(t, "Test User", activeTask.GetCustomerName())
	assert.Equal(t, "testlogin", activeTask.GetCustomerLogin())
	assert.False(t, activeTask.GetIsClosed())
	assert.Equal(t, []string{"Executor A", "Executor B"}, activeTask.GetExecutors())

	completedTask, ok := taskMap[202]
	assert.True(t, ok)
	assert.Equal(t, "Just a customer", completedTask.GetCustomerName())
	assert.Equal(t, "n/a", completedTask.GetCustomerLogin())
	assert.True(t, completedTask.GetIsClosed())
	assert.NotNil(t, completedTask.GetClosingDate())
}

func TestScraper_GetEmployees(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		if params.Get("core_section") == "staff_unit" {
			if params.Get("is_with_leaved") == "1" {
				w.Write([]byte(mockDismissedStaffHTML))
			} else {
				w.Write([]byte(mockActiveStaffHTML))
			}
			return
		}
		if params.Get("core_section") == "staff" && params.Get("action") == "division" {
			w.Write([]byte(mockStaffShortnamesHTML))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(mockLoginSuccessPage))
			return
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scraper, err := static.NewScraper(cfg, logger)
	if err != nil {
		t.Fatalf("setup failed: could not create scraper: %v", err)
	}

	employees, _, err := scraper.GetEmployees(context.Background())
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

	john, ok := empMap[10]
	if !ok || john.GetFullname() != "John Doe" || john.GetShortname() != "John D." {
		t.Errorf("failed to parse John Doe correctly: %+v", john)
	}

	jane, ok := empMap[20]
	if !ok || jane.GetFullname() != "Jane Smith" || jane.GetShortname() != "Jane S." {
		t.Errorf("failed to parse Jane Smith correctly: %+v", jane)
	}
}

func TestScraper_GetTaskTypes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		if params.Get("core_section") == "task" && params.Get("action") == "group_task_type_list" {
			w.Write([]byte(mockTaskTypesHTML))
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(mockLoginSuccessPage))
			return
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := &config.Config{LoginURL: server.URL, TargetURL: server.URL}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scraper, err := static.NewScraper(cfg, logger)
	if err != nil {
		t.Fatalf("setup failed: could not create scraper: %v", err)
	}

	taskTypes, _, err := scraper.GetTaskTypes(context.Background())
	if err != nil {
		t.Fatalf("GetTaskTypes failed: %v", err)
	}

	expectedTypes := []string{"Type 1", "Type 2", "Type 1", "Type 2", "Type 1", "Type 2"}
	if !reflect.DeepEqual(taskTypes, expectedTypes) {
		t.Errorf("expected task types %v, got %v", expectedTypes, taskTypes)
	}
}
