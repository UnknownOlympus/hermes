package static

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type taskSelectors struct {
	id          string
	createdAt   string
	closedAt    string
	address     string
	customer    string
	taskType    string
	description string
	executors   string
	comments    string
}

func (s *Scraper) GetDailyTasks(ctx context.Context, date time.Time) ([]*pb.Task, string, error) {
	type result struct {
		tasks []*pb.Task
		err   error
	}
	goroutines := 2
	channels := make(chan result, goroutines)

	go func() {
		tasks, err := s.scrapeTasksByState(ctx, date, true)
		channels <- result{tasks, err}
	}()
	go func() {
		tasks, err := s.scrapeTasksByState(ctx, date, false)
		channels <- result{tasks, err}
	}()

	var allTasks []*pb.Task
	for range 2 {
		res := <-channels
		if res.err != nil {
			return nil, "", res.err
		}
		allTasks = append(allTasks, res.tasks...)
	}

	hash, err := calculateSortedHash(allTasks, func(i, j int) bool {
		return allTasks[i].GetId() < allTasks[j].GetId()
	})
	if err != nil {
		return nil, "", err
	}

	return allTasks, hash, nil
}

func (s *Scraper) scrapeTasksByState(ctx context.Context, date time.Time, isCompleted bool) ([]*pb.Task, error) {
	formData := url.Values{
		"core_section":       {"task_list"},
		"filter_selector0":   {"task_state"},
		"filter_selector1":   {"task_group"},
		"task_group1_value":  {"3"},
		"filter_selector2":   {"date_update"},
		"date_update2":       {"3"},
		"date_update2_date1": {date.Format("02.01.2006")},
		"date_update2_date2": {date.Format("02.01.2006")},
	}
	if isCompleted {
		formData.Set("task_state0_value", "2")
	} else {
		formData.Set("task_state0_value", "1")
	}

	resp, err := s.getHTMLResponse(ctx, &formData)
	if err != nil {
		return nil, fmt.Errorf("failed to get html for tasks (completed=%t): %w", isCompleted, err)
	}
	defer resp.Body.Close()

	return s.parseTasksFromBody(resp.Body, isCompleted)
}

func (s *Scraper) parseTasksFromBody(body io.Reader, isCompleted bool) ([]*pb.Task, error) {
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, fmt.Errorf("failed to create goquery document: %w", err)
	}

	completedTasksConfig := taskSelectors{
		id:          "td:nth-child(7) a",
		createdAt:   "td:nth-child(8)",
		closedAt:    "td:nth-child(9)",
		address:     "td:nth-child(10)",
		customer:    "td:nth-child(11)",
		taskType:    "td:nth-child(13)",
		description: "td:nth-child(13) .div_journal_opis",
		executors:   "td:nth-child(14)",
		comments:    "td:nth-child(5)",
	}
	activeTasksConfig := taskSelectors{
		id:          "td:nth-child(7) a",
		createdAt:   "td:nth-child(8)",
		closedAt:    "",
		address:     "td:nth-child(9)",
		customer:    "td:nth-child(10)",
		taskType:    "td:nth-child(12)",
		description: "td:nth-child(12) .div_journal_opis",
		executors:   "td:nth-child(13)",
		comments:    "td:nth-child(5)",
	}

	var selectors taskSelectors
	if isCompleted {
		selectors = completedTasksConfig
	} else {
		selectors = activeTasksConfig
	}

	var tasks []*pb.Task
	doc.Find(`tr[tag^="row_"]`).Each(func(_ int, row *goquery.Selection) {
		task := &pb.Task{}

		taskID, _ := strconv.Atoi(row.Find(selectors.id).Text())
		task.Id = int64(taskID)

		createdAtStr := strings.TrimSpace(row.Find(selectors.createdAt).Contents().First().Text())
		created, createdErr := time.Parse("02.01.2006", createdAtStr)
		if createdErr == nil {
			task.CreationDate = timestamppb.New(created)
		}

		if isCompleted && selectors.closedAt != "" {
			closedAtStr := strings.TrimSpace(row.Find(selectors.closedAt).Contents().First().Text())
			closed, closedErr := time.Parse("02.01.2006", closedAtStr)
			if closedErr == nil {
				task.ClosingDate = timestamppb.New(closed)
			}
		}

		task.IsClosed = isCompleted
		task.Address = strings.TrimSpace(row.Find(selectors.address).Text())
		task.Type = strings.TrimSpace(row.Find(selectors.taskType + " b").First().Text())
		task.Description = strings.TrimSpace(row.Find(selectors.description).Text())
		if !utf8.ValidString(task.GetDescription()) {
			task.Description = ""
			s.log.Warn("Description contains invalid UTF-8 symbols, cleared.", "id", task.GetId())
		}

		customerHTML, _ := row.Find(selectors.customer).Html()
		task.CustomerName, task.CustomerLogin = ParseCustomerInfo(customerHTML, s.log)

		executorsHTML, _ := row.Find(selectors.executors).Html()
		task.Executors = ParseLinks(executorsHTML)

		commentsHTML, _ := row.Find(selectors.comments).Html()
		task.Comments = ParseLinks(commentsHTML)

		tasks = append(tasks, task)
	})

	return tasks, nil
}

func ParseLinks(rawHTML string) []string {
	var executors []string

	parts := strings.Split(rawHTML, "<br/>")
	for _, part := range parts {
		text := strings.TrimSpace(part)
		if strings.Contains(text, "<i>") {
			text = strings.Split(text, "<i>")[0]
		}
		if text != "" {
			executors = append(executors, strings.TrimSpace(text))
		}
	}

	return executors
}

func ParseCustomerInfo(rawHTML string, log *slog.Logger) (string, string) {
	const lenParts = 2
	var customerName string
	var customerLogin string

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		log.Debug("failed to parse customer info", "error", err)
		return "", ""
	}

	customerLoginNode := doc.Find("a")
	if customerLoginNode.Length() != 0 {
		customerData := strings.TrimSpace(customerLoginNode.Text())

		parts := strings.Split(customerData, "-")
		if len(parts) == lenParts {
			customerName = strings.TrimSpace(parts[0])
			customerLogin = strings.TrimSpace(parts[1])

			return customerName, customerLogin
		}
	}

	customerName = strings.TrimSpace(doc.Text())
	if customerName != "" {
		return customerName, "n/a"
	}

	return "", ""
}

func (s *Scraper) GetTaskTypes(ctx context.Context) ([]string, string, error) {
	var allTaskTypes []string

	const taskTypeGroupCount = 3

	for idx := 1; idx <= taskTypeGroupCount; idx++ {
		formData := url.Values{
			"core_section": {"task"},
			"action":       {"group_task_type_list"},
			"id":           {strconv.Itoa(idx)},
		}

		resp, err := s.getHTMLResponse(ctx, &formData)
		if err != nil {
			return nil, "", fmt.Errorf("failed to get response for task type group %d: %w", idx, err)
		}
		defer resp.Body.Close()

		taskTypesFromPage, err := s.parseTaskTypes(resp.Body)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse task types for group %d: %w", idx, err)
		}

		allTaskTypes = append(allTaskTypes, taskTypesFromPage...)
	}

	hash, err := calculateSortedHash(allTaskTypes, func(i, j int) bool {
		return strings.ToLower(allTaskTypes[i]) < strings.ToLower(allTaskTypes[j])
	})
	if err != nil {
		return nil, "", err
	}

	return allTaskTypes, hash, nil
}

func (s *Scraper) parseTaskTypes(body io.Reader) ([]string, error) {
	var taskTypes []string

	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, fmt.Errorf("failed to create goquery document: %w", err)
	}

	doc.Find(`a[title="Добавить задание"]`).Each(func(_ int, sel *goquery.Selection) {
		taskTypeName := strings.TrimSpace(sel.Text())
		if taskTypeName != "" {
			taskTypes = append(taskTypes, taskTypeName)
		}
	})

	return taskTypes, nil
}
