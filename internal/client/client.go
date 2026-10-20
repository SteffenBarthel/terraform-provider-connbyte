// Package client is a small client for the ConnByte DNS API (https://api.connbyte.com).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public ConnByte API endpoint.
const DefaultBaseURL = "https://api.connbyte.com"

// RecordTypes are the record types accepted by the ConnByte DNS API.
var RecordTypes = []string{"A", "AAAA", "CNAME", "MX", "NS", "SRV", "TXT"}

const (
	MinTTL      = 60
	MaxTTL      = 86400
	MinPriority = 0
	MaxPriority = 1000
)

// Client talks to the ConnByte DNS API. All endpoints are POST requests with a JSON body.
type Client struct {
	BaseURL    string
	APIToken   string
	UserAgent  string
	HTTPClient *http.Client
}

// New returns a client with a 30 second request timeout.
func New(baseURL, token, userAgent string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIToken:   token,
		UserAgent:  userAgent,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Zone is a DNS zone.
type Zone struct {
	ID        string  `json:"id"`
	ProjectID string  `json:"projectId"`
	Title     string  `json:"title"`
	Zone      string  `json:"zone"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt *string `json:"updatedAt"`
}

// Record is a DNS record as returned by the API. Name is the fully qualified name without trailing dot.
type Record struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int64  `json:"ttl"`
	Priority int64  `json:"priority"`
	Disabled int64  `json:"disabled"`
}

// RecordInput is the body for creating or updating a record. Name is relative to the zone ("" for the apex).
type RecordInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int64  `json:"ttl"`
	Priority int64  `json:"priority"`
	Disabled bool   `json:"disabled"`
}

type status struct {
	Code    int    `json:"code"`
	Title   string `json:"title"`
	Message string `json:"message"`
	LogID   string `json:"logId"`
}

type envelope struct {
	Status status              `json:"status"`
	Data   json.RawMessage     `json:"data"`
	Field  []map[string]string `json:"field"`
}

// APIError is returned when the API answers with an error status.
type APIError struct {
	HTTPStatus int
	Code       int
	Title      string
	Message    string
	LogID      string
	Field      []map[string]string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "connbyte API error %d", e.Code)
	if e.Title != "" {
		fmt.Fprintf(&b, " %s", e.Title)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	for _, f := range e.Field {
		for k, v := range f {
			fmt.Fprintf(&b, " [%s: %s]", k, v)
		}
	}
	if e.LogID != "" {
		fmt.Fprintf(&b, " (logId %s)", e.LogID)
	}
	return b.String()
}

// IsNotFound reports whether err means that the zone or record does not exist.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.HTTPStatus == http.StatusNotFound || apiErr.Code == http.StatusNotFound {
		return true
	}
	for _, f := range apiErr.Field {
		for k, v := range f {
			if (k == "zoneId" || k == "recordId") && strings.Contains(strings.ToLower(v), "not found") {
				return true
			}
		}
	}
	return false
}

func (c *Client) do(ctx context.Context, path string, payload any, out any) error {
	if c.APIToken == "" {
		return errors.New("connbyte: API token is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("connbyte: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("connbyte: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIToken)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connbyte: %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return fmt.Errorf("connbyte: %s: read response: %w", path, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode/100 != 2 {
			return &APIError{HTTPStatus: resp.StatusCode, Code: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		}
		return fmt.Errorf("connbyte: %s: HTTP %d: decode response: %w", path, resp.StatusCode, err)
	}

	if resp.StatusCode/100 != 2 || (env.Status.Code != 0 && env.Status.Code/100 != 2) {
		code := env.Status.Code
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{
			HTTPStatus: resp.StatusCode,
			Code:       code,
			Title:      env.Status.Title,
			Message:    env.Status.Message,
			LogID:      env.Status.LogID,
			Field:      env.Field,
		}
	}

	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("connbyte: %s: decode data: %w", path, err)
	}
	return nil
}

type zoneItem struct {
	Zone Zone `json:"zone"`
}

type recordItem struct {
	Record Record `json:"record"`
}

// ListZones returns all zones the token can access.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var items []zoneItem
	if err := c.do(ctx, "/ns/zone/list", struct{}{}, &items); err != nil {
		return nil, err
	}
	zones := make([]Zone, 0, len(items))
	for _, it := range items {
		zones = append(zones, it.Zone)
	}
	return zones, nil
}

// GetZone returns one zone by ID.
func (c *Client) GetZone(ctx context.Context, zoneID string) (*Zone, error) {
	var item zoneItem
	if err := c.do(ctx, "/ns/zone/data", map[string]string{"zoneId": zoneID}, &item); err != nil {
		return nil, err
	}
	return &item.Zone, nil
}

// FindZoneByName returns the zone with the given name (case-insensitive, trailing dot ignored).
func (c *Client) FindZoneByName(ctx context.Context, name string) (*Zone, error) {
	zones, err := c.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	want := NormalizeName(name)
	for i := range zones {
		if NormalizeName(zones[i].Zone) == want {
			return &zones[i], nil
		}
	}
	return nil, &APIError{HTTPStatus: http.StatusNotFound, Code: http.StatusNotFound, Message: fmt.Sprintf("zone %q not found", want)}
}

// ListRecords returns all records of a zone.
func (c *Client) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	var items []recordItem
	if err := c.do(ctx, "/ns/record/list", map[string]string{"zoneId": zoneID}, &items); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(items))
	for _, it := range items {
		records = append(records, it.Record)
	}
	return records, nil
}

// GetRecord returns one record. It reads the record list because it has a stable response shape.
func (c *Client) GetRecord(ctx context.Context, zoneID string, recordID int64) (*Record, error) {
	records, err := c.ListRecords(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].ID == recordID {
			return &records[i], nil
		}
	}
	return nil, &APIError{HTTPStatus: http.StatusNotFound, Code: http.StatusNotFound, Message: fmt.Sprintf("record %d not found", recordID)}
}

type createRequest struct {
	ZoneID string `json:"zoneId"`
	RecordInput
}

type updateRequest struct {
	ZoneID   string `json:"zoneId"`
	RecordID int64  `json:"recordId"`
	RecordInput
}

type deleteRequest struct {
	ZoneID   string `json:"zoneId"`
	RecordID int64  `json:"recordId"`
}

// flexibleID accepts a JSON number or a numeric string.
type flexibleID int64

func (f *flexibleID) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid record id %s", string(b))
	}
	*f = flexibleID(n)
	return nil
}

type writeResult struct {
	ID     flexibleID `json:"id"`
	Record string     `json:"record"`
}

// CreateRecord creates a record and returns its ID.
func (c *Client) CreateRecord(ctx context.Context, zoneID string, in RecordInput) (int64, error) {
	var res writeResult
	if err := c.do(ctx, "/ns/record/create", createRequest{ZoneID: zoneID, RecordInput: in}, &res); err != nil {
		return 0, err
	}
	if res.ID == 0 {
		return 0, errors.New("connbyte: /ns/record/create: response has no record id")
	}
	return int64(res.ID), nil
}

// UpdateRecord replaces all fields of an existing record.
func (c *Client) UpdateRecord(ctx context.Context, zoneID string, recordID int64, in RecordInput) error {
	return c.do(ctx, "/ns/record/update", updateRequest{ZoneID: zoneID, RecordID: recordID, RecordInput: in}, nil)
}

// DeleteRecord deletes a record. A record that is already gone is not an error.
func (c *Client) DeleteRecord(ctx context.Context, zoneID string, recordID int64) error {
	err := c.do(ctx, "/ns/record/delete", deleteRequest{ZoneID: zoneID, RecordID: recordID}, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// NormalizeName lowercases a DNS name and removes the trailing dot.
func NormalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// RelativeName converts a fully qualified record name into a name relative to zone. The apex is "@".
func RelativeName(fqdn, zone string) string {
	f := NormalizeName(fqdn)
	z := NormalizeName(zone)
	if f == z || f == "" || f == "@" {
		return "@"
	}
	if strings.HasSuffix(f, "."+z) {
		return strings.TrimSuffix(f, "."+z)
	}
	return f
}

// FQDN converts a relative record name into a fully qualified name without trailing dot.
func FQDN(name, zone string) string {
	n := NormalizeName(name)
	z := NormalizeName(zone)
	if n == "" || n == "@" {
		return z
	}
	return n + "." + z
}

// UnquoteTXT removes the quotes the API adds around TXT content.
func UnquoteTXT(s string) string {
	if len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && !strings.Contains(s[1:len(s)-1], `" "`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return s[1 : len(s)-1]
	}
	return s
}
