package static

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime).Seconds()
		s.metrics.ScrapeDuration.WithLabelValues("tasks").Observe(duration)
	}()

	taskGroupIDs := []string{"1", "2", "3"}
	completionStates := []bool{true, false}

	type result struct {
		tasks []*pb.Task
		err   error
	}

	var wgr sync.WaitGroup
	resultsChan := make(chan result)

	for _, groupID := range taskGroupIDs {
		for _, isCompleted := range completionStates {
			wgr.Add(1)

			go func(id string, completed bool) {
				defer wgr.Done()
				tasks, err := s.scrapeTasksByState(ctx, date, completed, id)
				resultsChan <- result{tasks, err}
			}(groupID, isCompleted)
		}
	}

	go func() {
		wgr.Wait()
		close(resultsChan)
	}()

	var allTasks []*pb.Task
	for res := range resultsChan {
		if res.err != nil {
			return nil, "", res.err
		}
		allTasks = append(allTasks, res.tasks...)
	}

	hash, err := calculateSortedHash(allTasks, func(i, j int) bool {
		return allTasks[i].GetId() < allTasks[j].GetId()
	})
	if err != nil {
		s.metrics.ScrapeErrors.WithLabelValues("tasks", "hash_failed").Inc()
		return nil, "", err
	}

	return allTasks, hash, nil
}

func (s *Scraper) scrapeTasksByState(
	ctx context.Context,
	date time.Time,
	isCompleted bool,
	taskGroupID string,
) ([]*pb.Task, error) {
	formData := url.Values{
		"core_section":       {"task_list"},
		"filter_selector0":   {"task_state"},
		"filter_selector1":   {"task_group"},
		"task_group1_value":  {taskGroupID},
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
		return nil, fmt.Errorf(
			"failed to get html for tasks (completed=%t, group=%s): %w",
			isCompleted,
			taskGroupID,
			err,
		)
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
		task.Address = s.sanitazeUTF8(strings.TrimSpace(row.Find(selectors.address).Text()), taskID)
		task.Type = s.sanitazeUTF8(strings.TrimSpace(row.Find(selectors.taskType+" b").First().Text()), taskID)
		task.Description = s.sanitazeUTF8(strings.TrimSpace(row.Find(selectors.description).Text()), taskID)

		customerHTML, _ := row.Find(selectors.customer).Html()
		task.Customers = ParseCustomerInfo(customerHTML, s.log)

		executorsHTML, _ := row.Find(selectors.executors).Html()
		task.Executors = ParseLinks(executorsHTML)

		commentsHTML, _ := row.Find(selectors.comments).Html()
		task.Comments = ParseLinks(commentsHTML)

		tasks = append(tasks, task)
	})

	return tasks, nil
}

func (s *Scraper) sanitazeUTF8(str string, taskID int) string {
	if !utf8.ValidString(str) {
		s.metrics.ScrapeErrors.WithLabelValues("tasks", "invalid_utf8")
		s.log.Warn("Text contains invalid UTF-8 symbols, cleared.", "id", taskID)
	}
	return strings.ToValidUTF8(str, "")
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

func ParseCustomerInfo(rawHTML string, log *slog.Logger) []*pb.Customer {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		log.Debug("failed to parse customer info", "error", err)
		return nil
	}

	var customers []*pb.Customer

	customerLoginNode := doc.Find("a")
	if customerLoginNode.Length() != 0 {
		customerLoginNode.Each(func(_ int, s *goquery.Selection) {
			customer, selectionErr := parseCustomerFromSelection(s)
			if selectionErr != nil {
				log.Debug("failed to parse a customer link", "error", selectionErr)
				return
			}

			customers = append(customers, customer)
		})
	} else {
		plainTextName := strings.TrimSpace(doc.Text())

		if plainTextName != "" {
			customers = append(customers, &pb.Customer{
				Id:    0,
				Name:  plainTextName,
				Login: "n/a",
			})
		}
	}

	return customers
}

func parseCustomerFromSelection(sel *goquery.Selection) (*pb.Customer, error) {
	href, exists := sel.Attr("href")
	if !exists {
		return nil, errors.New("href attribute not found")
	}

	idStr := ""
	if idIndex := strings.LastIndex(href, "id="); idIndex != -1 {
		idStr = href[idIndex+3:]
	}

	idx, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse customer ID from href '%s': %w", href, err)
	}

	customerData := strings.TrimSpace(sel.Text())
	parts := strings.Split(customerData, " - ")
	const partsLen = 2
	if len(parts) != partsLen {
		return nil, fmt.Errorf("customer text is not in 'Name - Login' format: %s", customerData)
	}

	name := strings.TrimSpace(parts[0])
	login := strings.TrimSpace(parts[1])

	return &pb.Customer{
		Id:    idx,
		Name:  name,
		Login: login,
	}, nil
}

func (s *Scraper) GetTaskTypes(ctx context.Context) ([]string, string, error) {
	startTime := time.Now()

	defer func() {
		duration := time.Since(startTime).Seconds()
		s.metrics.ScrapeDuration.WithLabelValues("task_types").Observe(duration)
	}()

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

// AddComment executes post request to add comment for a given task.
func (s *Scraper) AddComment(ctx context.Context, taskID int64, text string) error {
	formData := url.Values{
		"core_section":     {"task"},
		"action":           {"comment_add"},
		"reply_id":         {"0"},
		"standart_comment": {"1"},
		"id":               {strconv.FormatInt(taskID, 10)},
		"opis":             {text},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TargetURL, strings.NewReader(formData.Encode()))
	if err != nil {
		s.metrics.ScrapeErrors.WithLabelValues("task", "create_request_failed").Inc()
		return fmt.Errorf("failed to create tasks request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		s.metrics.ScrapeErrors.WithLabelValues("task", "request_failed").Inc()
		return fmt.Errorf("failed to perform add comment request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.metrics.ScrapeErrors.WithLabelValues("task", "bad_status_code").Inc()
		return fmt.Errorf("add comment request failed with status: %s", resp.Status)
	}

	s.log.InfoContext(ctx, "Successfully added comment to task", "task_id", taskID)

	return nil
}

// GetCommentsFromTask gets and parses only the comments for a given task.
func (s *Scraper) GetCommentsFromTask(ctx context.Context, taskID int64) ([]string, error) {
	data := url.Values{
		"core_section": {"task"},
		"action":       {"show"},
		"id":           {strconv.FormatInt(taskID, 10)},
	}

	resp, err := s.getHTMLResponse(ctx, &data)
	if err != nil {
		return nil, fmt.Errorf("failed to get task page %d: %w", taskID, err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse task page: %w", err)
	}

	var comments []string

	// 1. Find each main comment block. The selector looks for a div
	//    with a class 'j_card_comment_div' and an ID that starts with 'comment2_'.
	doc.Find("div.j_card_comment_div[id^='comment2_']").Each(func(_ int, selector *goquery.Selection) {
		// 2. Within each block, find the author's name.
		// author := strings.TrimSpace(s.Find("div[style*='font-weight: bold']").Text())
		// 3. Find the comment's date and time.
		// dateTime := strings.TrimSpace(s.Find("div[style*='float: right']").Text())
		// 4. Find the comment text itself. It's in a span with an ID like 'comment_xxxxx_id'.
		commentText := strings.TrimSpace(selector.Find("span[id^='comment_']").Text())

		// 5. Format the final string.
		// Example format: "👤 Author Name (23.08.2025 14:25): The comment text"
		// formattedComment := fmt.Sprintf("👤 %s (%s): %s", author, dateTime, commentText)

		comments = append(comments, commentText)
	})

	return comments, nil
}
