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

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// DefaultBaseURL is the public ConnByte API endpoint.
const DefaultBaseURL = "https://api.connbyte.com"

// ErrNotFound means that the requested zone or record does not exist. The client returns it only
// after it has checked the zone or record list, never because of a bare HTTP 404 or an error text.
var ErrNotFound = errors.New("not found")

const (
	defaultMaxRetries = 3
	maxRetryWait      = 30 * time.Second
)

// DefaultRetryWait is the RetryWait of new clients. Tests lower it.
var DefaultRetryWait = time.Second

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
	// MaxRetries is the number of extra attempts for read requests after a network error,
	// HTTP 5xx, 423 or 429. Write requests are never retried.
	MaxRetries int
	// RetryWait is the wait before the first retry. It doubles with every retry.
	RetryWait time.Duration
}

// New returns a client with a 30 second request timeout and retries for read requests.
func New(baseURL, token, userAgent string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIToken:   token,
		UserAgent:  userAgent,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		MaxRetries: defaultMaxRetries,
		RetryWait:  DefaultRetryWait,
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
	return errors.Is(err, ErrNotFound)
}

// checkable reports whether an API error may mean that the object is gone, so that a look at the
// zone or record list is worth it. Auth and rate limit errors are never checked.
func checkable(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.HTTPStatus {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusLocked, http.StatusTooManyRequests:
		return false
	}
	return true
}

// transportError is a network error before any HTTP response arrived.
type transportError struct {
	path string
	err  error
}

func (e *transportError) Error() string { return fmt.Sprintf("connbyte: %s: %v", e.path, e.err) }
func (e *transportError) Unwrap() error { return e.err }

func retryable(status int) bool {
	return status/100 == 5 || status == http.StatusLocked || status == http.StatusTooManyRequests
}

// doRead sends a request that does not change data. It is retried on temporary errors.
func (c *Client) doRead(ctx context.Context, path string, payload any, out any) error {
	wait := c.RetryWait
	for attempt := 0; ; attempt++ {
		retryAfter, err := c.do(ctx, path, payload, out)
		if err == nil || attempt >= c.MaxRetries || ctx.Err() != nil {
			return err
		}
		var apiErr *APIError
		var tErr *transportError
		switch {
		case errors.As(err, &apiErr):
			if !retryable(apiErr.HTTPStatus) {
				return err
			}
		case !errors.As(err, &tErr):
			return err
		}
		d := wait
		if retryAfter > d {
			d = retryAfter
		}
		if d > maxRetryWait {
			d = maxRetryWait
		}
		tflog.Debug(ctx, "connbyte API: retrying read request", map[string]any{"path": path, "attempt": attempt + 1, "wait": d.String(), "error": err.Error()})
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return err
		case <-t.C:
		}
		wait *= 2
	}
}

// do sends one request. The returned duration is the Retry-After value of the response, if any.
func (c *Client) do(ctx context.Context, path string, payload any, out any) (time.Duration, error) {
	if c.APIToken == "" {
		return 0, errors.New("connbyte: API token is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("connbyte: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("connbyte: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Error texts and field messages are localized; always ask for English.
	req.Header.Set("Accept-Language", "en-US")
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
		return 0, &transportError{path: path, err: err}
	}
	defer resp.Body.Close()

	var retryAfter time.Duration
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		retryAfter = time.Duration(s) * time.Second
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return retryAfter, fmt.Errorf("connbyte: %s: read response: %w", path, err)
	}

	var env envelope
	jsonErr := json.Unmarshal(raw, &env)
	tflog.Debug(ctx, "connbyte API response", map[string]any{"path": path, "http_status": resp.StatusCode, "code": env.Status.Code, "log_id": env.Status.LogID})
	if jsonErr != nil {
		if resp.StatusCode/100 != 2 {
			return retryAfter, &APIError{HTTPStatus: resp.StatusCode, Code: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
		}
		return retryAfter, fmt.Errorf("connbyte: %s: HTTP %d: decode response: %w", path, resp.StatusCode, jsonErr)
	}

	if resp.StatusCode/100 != 2 || (env.Status.Code != 0 && env.Status.Code/100 != 2) {
		code := env.Status.Code
		if code == 0 {
			code = resp.StatusCode
		}
		httpStatus := resp.StatusCode
		if httpStatus/100 == 2 {
			httpStatus = code
		}
		return retryAfter, &APIError{
			HTTPStatus: httpStatus,
			Code:       code,
			Title:      env.Status.Title,
			Message:    env.Status.Message,
			LogID:      env.Status.LogID,
			Field:      env.Field,
		}
	}

	if out == nil || len(env.Data) == 0 {
		return retryAfter, nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return retryAfter, fmt.Errorf("connbyte: %s: decode data: %w", path, err)
	}
	return retryAfter, nil
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
	if err := c.doRead(ctx, "/ns/zone/list", struct{}{}, &items); err != nil {
		return nil, err
	}
	zones := make([]Zone, 0, len(items))
	for _, it := range items {
		zones = append(zones, it.Zone)
	}
	return zones, nil
}

// GetZone returns one zone by ID. It returns ErrNotFound only when the zone list was read and does
// not contain the zone.
func (c *Client) GetZone(ctx context.Context, zoneID string) (*Zone, error) {
	var item zoneItem
	err := c.doRead(ctx, "/ns/zone/data", map[string]string{"zoneId": zoneID}, &item)
	if err == nil {
		return &item.Zone, nil
	}
	if !checkable(err) {
		return nil, err
	}
	zones, listErr := c.ListZones(ctx)
	if listErr != nil {
		return nil, err
	}
	for i := range zones {
		if zones[i].ID == zoneID {
			return nil, err
		}
	}
	return nil, fmt.Errorf("connbyte: zone %q: %w", zoneID, ErrNotFound)
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
	return nil, fmt.Errorf("connbyte: zone %q: %w", want, ErrNotFound)
}

// ListRecords returns all records of a zone. It returns ErrNotFound when the zone does not exist.
func (c *Client) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	if zoneID == "" {
		return nil, errors.New("connbyte: zone ID is empty")
	}
	var items []recordItem
	if err := c.doRead(ctx, "/ns/record/list", map[string]string{"zoneId": zoneID}, &items); err != nil {
		if checkable(err) {
			if _, zoneErr := c.GetZone(ctx, zoneID); IsNotFound(zoneErr) {
				return nil, zoneErr
			}
		}
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
	return nil, fmt.Errorf("connbyte: record %d in zone %q: %w", recordID, zoneID, ErrNotFound)
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

// CreateRecord creates a record and returns its ID. It is never retried, so a lost response cannot
// create the record twice.
func (c *Client) CreateRecord(ctx context.Context, zoneID string, in RecordInput) (int64, error) {
	if zoneID == "" {
		return 0, errors.New("connbyte: zone ID is empty")
	}
	var res writeResult
	if _, err := c.do(ctx, "/ns/record/create", createRequest{ZoneID: zoneID, RecordInput: in}, &res); err != nil {
		return 0, err
	}
	if res.ID == 0 {
		return 0, errors.New("connbyte: /ns/record/create: response has no record id")
	}
	return int64(res.ID), nil
}

// UpdateRecord replaces all fields of an existing record.
func (c *Client) UpdateRecord(ctx context.Context, zoneID string, recordID int64, in RecordInput) error {
	if zoneID == "" {
		return errors.New("connbyte: zone ID is empty")
	}
	_, err := c.do(ctx, "/ns/record/update", updateRequest{ZoneID: zoneID, RecordID: recordID, RecordInput: in}, nil)
	return err
}

// DeleteRecord deletes a record. A record that is already gone is not an error. When the API
// rejects the request, the record list decides: if the record is not in it, the delete succeeded.
func (c *Client) DeleteRecord(ctx context.Context, zoneID string, recordID int64) error {
	if zoneID == "" {
		return errors.New("connbyte: zone ID is empty")
	}
	_, err := c.do(ctx, "/ns/record/delete", deleteRequest{ZoneID: zoneID, RecordID: recordID}, nil)
	if err == nil || !checkable(err) {
		return err
	}
	if _, getErr := c.GetRecord(ctx, zoneID, recordID); IsNotFound(getErr) {
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

// UnquoteTXT turns TXT content in zone file notation into plain text. The API returns TXT content
// as one or more quoted strings; long values (over 255 bytes) are split into several strings, for
// example `"part1" "part2"`. The strings are joined without a separator. Content that is not in this
// notation is returned unchanged.
func UnquoteTXT(s string) string {
	var out strings.Builder
	i, n := 0, len(s)
	parts := 0
	for {
		for i < n && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i == n {
			break
		}
		if s[i] != '"' {
			return s
		}
		i++
		closed := false
		for i < n {
			ch := s[i]
			if ch == '"' {
				closed = true
				i++
				break
			}
			if ch == '\\' && i+1 < n {
				if i+3 < n && isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) {
					v, _ := strconv.Atoi(s[i+1 : i+4])
					if v > 255 {
						return s
					}
					out.WriteByte(byte(v))
					i += 4
					continue
				}
				out.WriteByte(s[i+1])
				i += 2
				continue
			}
			out.WriteByte(ch)
			i++
		}
		if !closed {
			return s
		}
		parts++
	}
	if parts == 0 {
		return s
	}
	return out.String()
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
