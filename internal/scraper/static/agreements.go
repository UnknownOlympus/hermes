package static

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	pb "github.com/UnknownOlympus/olympus-protos/gen/go/scraper/olympus"
)

var ErrNoParameters = errors.New("arguments has zero id and empty name")
var ErrBoxNotFound = errors.New("checkbox not found in the row")
var ErrEmptyValue = errors.New("'value' attribute not found or is empty")

func (s *Scraper) GetAgreementsByID(ctx context.Context, id int64) ([]*pb.Agreement, error) {
	formData, err := getAgreementFormParameters("", id)
	if err != nil {
		return nil, fmt.Errorf("failed to get form data for agreements: %w", err)
	}

	agreements, err := s.scrapeAgreements(ctx, formData)
	if err != nil {
		return nil, fmt.Errorf("failed to scrape agreements data: %w", err)
	}

	return agreements, nil
}

func (s *Scraper) GetAgreementsByName(ctx context.Context, name string) ([]*pb.Agreement, error) {
	formData, err := getAgreementFormParameters(name, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to get form data for agreements: %w", err)
	}

	agreements, err := s.scrapeAgreements(ctx, formData)
	if err != nil {
		return nil, fmt.Errorf("failed to scrape agreements data: %w", err)
	}

	return agreements, nil
}

func (s *Scraper) scrapeAgreements(ctx context.Context, data url.Values) ([]*pb.Agreement, error) {
	resp, err := s.getHTMLResponse(ctx, &data)
	if err != nil {
		return nil, fmt.Errorf("failed to get html response: %w", err)
	}
	defer resp.Body.Close()

	return s.parseAgreementsFromBody(ctx, resp.Body)
}

func getAgreementFormParameters(name string, identifier int64) (url.Values, error) {
	formData := url.Values{
		"core_section": {"customer_list"},
	}
	switch {
	case identifier != 0:
		formData.Set("filter_selector0", "customer_id")
		formData.Set("customer_id0_value", strconv.Itoa(int(identifier)))
	case name != "":
		formData.Set("filter_selector0", "customer_name")
		formData.Set("customer_name0_value", name)
	default:
		return nil, ErrNoParameters
	}

	return formData, nil
}

func (s *Scraper) parseAgreementsFromBody(ctx context.Context, body io.Reader) ([]*pb.Agreement, error) {
	log := s.log.With("op", "parseAgreementsFromBody")
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, fmt.Errorf("failed to create goquery document: %w", err)
	}

	var agreements []*pb.Agreement
	doc.Find(`tr[tag^="row_"]`).Each(func(_ int, row *goquery.Selection) {
		clientID, paresErr := ParseIDFromRow(row)
		if paresErr != nil {
			log.WarnContext(ctx, "Could not parse ID from row", "error", paresErr)
		}

		// Find contract number
		contractCell := row.Find("td:nth-child(5)")
		contract := strings.Join(strings.Fields(contractCell.Contents().First().Text()), "")

		// Find address and phone
		addressCell := row.Find("td:nth-child(3)")
		address := strings.TrimSpace(addressCell.Find("a").Text())
		fullAddressText := strings.TrimSpace(addressCell.Text())
		phoneNumber := strings.TrimSpace(strings.TrimPrefix(fullAddressText, address))

		var tariffPlan string
		fullTariffText := getTextFromSelector(row, "10")
		if lastIndex := strings.LastIndex(fullTariffText, " - "); lastIndex != -1 {
			tariffPlan = fullTariffText[:lastIndex]
		} else {
			tariffPlan = fullTariffText
		}

		agreement := &pb.Agreement{
			Id:       clientID,
			Ip:       getTextFromSelector(row, "4"),
			Contract: contract,
			Name:     getTextFromSelector(row, "6"),
			Balance:  getTextFromSelector(row, "9"),
			Tariff:   tariffPlan,
			Address:  address,
			Number:   phoneNumber,
		}

		agreements = append(agreements, agreement)
	})

	return agreements, nil
}

func getTextFromSelector(row *goquery.Selection, id string) string {
	selector := fmt.Sprintf("td:nth-child(%s)", id)
	return strings.TrimSpace(row.Find(selector).Text())
}

func ParseIDFromRow(row *goquery.Selection) (int64, error) {
	checkbox := row.Find(`input[type="checkbox"]`)
	if checkbox.Length() == 0 {
		return 0, ErrBoxNotFound
	}

	idStr, exists := checkbox.Attr("value")
	if !exists || idStr == "" {
		return 0, ErrEmptyValue
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to convert ID '%s' to integer: %w", idStr, err)
	}

	return id, nil
}
