package static

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
)

type staffShortname struct {
	ID        int64
	Shortname string
}

// GetEmployees combines the logic for getting all employees.
func (s *Scraper) GetEmployees(ctx context.Context) ([]*pb.Employee, string, error) {
	startTime := time.Now()

	defer func() {
		duration := time.Since(startTime).Seconds()
		s.metrics.ScrapeDuration.WithLabelValues("employees").Observe(duration)
	}()

	staff, err := s.parseStaffPage(ctx, false) // Actual
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse staff: %w", err)
	}

	dismissedStaff, err := s.parseStaffPage(ctx, true) // Dismissed
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse dismissed staff: %w", err)
	}
	staff = append(staff, dismissedStaff...)

	shortnames, err := s.parseStaffShortNames(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse employee's shortnames: %w", err)
	}

	fullStaff := s.updateStaffShortNames(staff, shortnames)

	hash, err := calculateSortedHash(fullStaff, func(i, j int) bool {
		return fullStaff[i].GetId() < fullStaff[j].GetId()
	})
	if err != nil {
		s.metrics.ScrapeErrors.WithLabelValues("employees", "hash_failed").Inc()
		return nil, "", err
	}

	return fullStaff, hash, nil
}

func (s *Scraper) parseStaffPage(ctx context.Context, dismissed bool) ([]*pb.Employee, error) {
	data := url.Values{"core_section": {"staff_unit"}}
	tdMap := map[bool][]int{
		false: {2, 3, 4, 5, 6}, // actual
		true:  {3, 4, 5, 6, 7}, // dismissed
	}
	tds := tdMap[dismissed]
	if dismissed {
		data.Set("is_with_leaved", "1")
	}

	resp, err := s.getHTMLResponse(ctx, &data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)
	var employees []*pb.Employee
	doc.Find(`tr[tag^="row_"]`).Each(func(_ int, row *goquery.Selection) {
		employeeID, _ := strconv.Atoi(row.Find(fmt.Sprintf("td:nth-child(%d) input", tds[0])).AttrOr("value", "0"))
		emp := &pb.Employee{
			Id:       int64(employeeID),
			Fullname: strings.TrimSpace(row.Find(fmt.Sprintf("td:nth-child(%d)", tds[1])).Text()),
			Position: strings.TrimSpace(row.Find(fmt.Sprintf("td:nth-child(%d)", tds[2])).Text()),
			Email:    strings.TrimSpace(row.Find(fmt.Sprintf("td:nth-child(%d)", tds[3])).Text()),
			Phone:    strings.TrimSpace(row.Find(fmt.Sprintf("td:nth-child(%d)", tds[4])).Text()),
		}
		employees = append(employees, emp)
	})

	return employees, nil
}

func (s *Scraper) parseStaffShortNames(ctx context.Context) ([]staffShortname, error) {
	data := url.Values{}
	var employees []staffShortname

	data.Set("core_section", "staff")
	data.Set("action", "division")

	resp, err := s.getHTMLResponse(ctx, &data)
	if err != nil {
		return nil, fmt.Errorf("failed to get response which should retrieve task types: %w", err)
	}
	defer resp.Body.Close()

	doc, _ := goquery.NewDocumentFromReader(resp.Body)

	doc.Find(`div.div_space`).Each(func(_ int, div *goquery.Selection) {
		link := div.Find("a")
		if link.Length() > 0 {
			href, exists := link.Attr("href")
			if exists {
				id, errID := parseIDFromHref(href)
				if id > 0 && errID == nil {
					employee := staffShortname{
						ID:        int64(id),
						Shortname: strings.TrimSpace(link.Text()),
					}
					employees = append(employees, employee)
				}
			}
		}
	})

	return employees, nil
}

func (s *Scraper) updateStaffShortNames(employees []*pb.Employee, shortnames []staffShortname) []*pb.Employee {
	shortNameMap := make(map[int]string)
	for _, sn := range shortnames {
		shortNameMap[int(sn.ID)] = sn.Shortname
	}

	updatedEmployees := make([]*pb.Employee, len(employees))
	copy(updatedEmployees, employees)

	for i := range updatedEmployees {
		if shortname, ok := shortNameMap[int(updatedEmployees[i].GetId())]; ok {
			s.log.Debug("shortname was updated", "old sn", updatedEmployees[i].GetShortname(), "new sn", shortname)
			updatedEmployees[i].Shortname = shortname
		}
	}

	return updatedEmployees
}
