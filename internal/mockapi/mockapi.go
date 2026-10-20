// Package mockapi is an in-memory fake of the ConnByte DNS API (/ns/*) for tests.
// It follows the response shapes of the public Swagger documentation at https://api.connbyte.com.
package mockapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Server is a running mock API.
type Server struct {
	*httptest.Server
	Token string

	mu      sync.Mutex
	zones   []zone
	records map[string][]*record
	nextID  int64
	calls   []Call
}

// Call is one request the mock received.
type Call struct {
	Path string
	Body map[string]any
}

type zone struct {
	ID        string  `json:"id"`
	ProjectID string  `json:"projectId"`
	Title     string  `json:"title"`
	Zone      string  `json:"zone"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt *string `json:"updatedAt"`
}

type record struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int64  `json:"ttl"`
	Priority int64  `json:"priority"`
	Disabled int64  `json:"disabled"`
}

const (
	ZoneExampleID = "01JZEXAMPLECOM000000000000"
	ZoneOtherID   = "01JZOTHERNET00000000000000"
	ProjectID     = "01JPPROJECT000000000000000"
)

// New starts a mock API with two zones: example.com and other.net.
func New(token string) *Server {
	s := &Server{
		Token: token,
		zones: []zone{
			{ID: ZoneExampleID, ProjectID: ProjectID, Zone: "example.com", CreatedAt: "2026-04-05T21:43:40.000Z"},
			{ID: ZoneOtherID, ProjectID: ProjectID, Title: "Other", Zone: "other.net", CreatedAt: "2026-04-05T21:43:40.000Z"},
		},
		records: map[string][]*record{},
		nextID:  435,
	}
	s.records[ZoneExampleID] = []*record{
		{ID: 1, Name: "example.com", Type: "NS", Content: "ns.connbyte.com", TTL: 86400},
		{ID: 2, Name: "example.com", Type: "NS", Content: "ns.connbyte.de", TTL: 86400},
	}
	s.records[ZoneOtherID] = []*record{
		{ID: 3, Name: "www.other.net", Type: "A", Content: "192.0.2.10", TTL: 3600},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Calls returns a copy of all received calls.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// RecordCount returns the number of records in a zone.
func (s *Server) RecordCount(zoneID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records[zoneID])
}

// DeleteRecordOutOfBand removes a record without going through the API (simulates manual deletion).
func (s *Server) DeleteRecordOutOfBand(zoneID string, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs := s.records[zoneID]
	for i, r := range recs {
		if r.ID == id {
			s.records[zoneID] = append(recs[:i], recs[i+1:]...)
			return
		}
	}
}

// FindRecord returns a copy of the record with the given fully qualified name and type.
func (s *Server) FindRecord(zoneID, fqdn, typ string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.records[zoneID] {
		if r.Name == fqdn && r.Type == typ {
			return Record(*r), true
		}
	}
	return Record{}, false
}

// Record is an exported copy of a stored record.
type Record struct {
	ID       int64
	Name     string
	Type     string
	Content  string
	TTL      int64
	Priority int64
	Disabled int64
}

func status(code int, path string) map[string]any {
	title, msg := "OK", "Data were transferred."
	switch code {
	case http.StatusBadRequest:
		title, msg = "Bad Request", "Transferred data are incorrect."
	case http.StatusForbidden:
		title, msg = "Forbidden", "Access denied."
	case http.StatusNotFound:
		title, msg = "Not Found", "Resource not found."
	}
	return map[string]any{
		"code": code, "title": title, "message": msg, "id": "01DXF6DT00ZZZHQPP3JFJS8VEE",
		"titleOriginal": title, "messageOriginal": msg, "logId": "01JLOG0000000000000000000",
		"path": path, "time": 1775426008, "version": "4.17.0",
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	send := func(code int, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	}
	fail := func(code int, field, msg string) {
		body := map[string]any{"status": status(code, r.URL.Path)}
		if field != "" {
			body["field"] = []map[string]string{{field: msg}}
		}
		send(code, body)
	}
	ok := func(data any) {
		send(http.StatusOK, map[string]any{"status": status(http.StatusOK, r.URL.Path), "data": data})
	}

	var b map[string]any
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&b); err != nil {
		fail(http.StatusBadRequest, "body", "invalid json")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Path: r.URL.Path, Body: b})

	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		fail(http.StatusBadRequest, "authorization", "'Authorization' must be set.")
		return
	}
	if r.Method != http.MethodPost {
		fail(http.StatusNotFound, "", "")
		return
	}

	switch r.URL.Path {
	case "/ns/zone/list":
		out := make([]map[string]any, 0, len(s.zones))
		for _, z := range s.zones {
			out = append(out, map[string]any{"zone": z})
		}
		ok(out)
		return
	}

	zoneID, _ := b["zoneId"].(string)
	z := s.zone(zoneID)
	if z == nil {
		fail(http.StatusBadRequest, "zoneId", "Zone not found.")
		return
	}

	switch r.URL.Path {
	case "/ns/zone/data":
		ok(map[string]any{"zone": z})
	case "/ns/record/list":
		recs := append([]*record(nil), s.records[z.ID]...)
		sort.Slice(recs, func(i, j int) bool { return recs[i].ID > recs[j].ID })
		out := make([]map[string]any, 0, len(recs))
		for _, rec := range recs {
			out = append(out, map[string]any{"record": rec})
		}
		ok(out)
	case "/ns/record/create":
		rec, field, msg := s.parseRecord(z, b)
		if field != "" {
			fail(http.StatusBadRequest, field, msg)
			return
		}
		rec.ID = s.nextID
		s.nextID++
		s.records[z.ID] = append(s.records[z.ID], rec)
		ok(map[string]any{"id": rec.ID, "record": rec.Name})
	case "/ns/record/update":
		id, err := toInt(b["recordId"])
		if err != nil {
			fail(http.StatusBadRequest, "recordId", "Field must be an integer.")
			return
		}
		existing := s.record(z.ID, id)
		if existing == nil {
			fail(http.StatusBadRequest, "recordId", "Record not found.")
			return
		}
		rec, field, msg := s.parseRecord(z, b)
		if field != "" {
			fail(http.StatusBadRequest, field, msg)
			return
		}
		rec.ID = id
		*existing = *rec
		// The real API returns the ID as a string here.
		ok(map[string]any{"id": strconv.FormatInt(id, 10), "record": rec.Name})
	case "/ns/record/delete":
		id, err := toInt(b["recordId"])
		if err != nil {
			fail(http.StatusBadRequest, "recordId", "Field must be an integer.")
			return
		}
		recs := s.records[z.ID]
		for i, rec := range recs {
			if rec.ID == id {
				s.records[z.ID] = append(recs[:i], recs[i+1:]...)
				ok(map[string]any{})
				return
			}
		}
		fail(http.StatusBadRequest, "recordId", "Record not found.")
	default:
		fail(http.StatusNotFound, "", "")
	}
}

func (s *Server) zone(id string) *zone {
	for i := range s.zones {
		if s.zones[i].ID == id {
			return &s.zones[i]
		}
	}
	return nil
}

func (s *Server) record(zoneID string, id int64) *record {
	for _, r := range s.records[zoneID] {
		if r.ID == id {
			return r
		}
	}
	return nil
}

var validTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "MX": true, "NS": true, "SRV": true, "TXT": true}

func (s *Server) parseRecord(z *zone, b map[string]any) (*record, string, string) {
	for _, k := range []string{"name", "type", "content", "ttl", "priority", "disabled"} {
		if _, ok := b[k]; !ok {
			return nil, k, "Field must be set."
		}
	}
	name, _ := b["name"].(string)
	typ, _ := b["type"].(string)
	content, _ := b["content"].(string)
	disabled, isBool := b["disabled"].(bool)
	if !isBool {
		return nil, "disabled", "Field must be a boolean."
	}
	ttl, err := toInt(b["ttl"])
	if err != nil || ttl < 60 || ttl > 86400 {
		return nil, "ttl", "Field must be between 60 and 86400."
	}
	prio, err := toInt(b["priority"])
	if err != nil || prio < 0 || prio > 1000 {
		return nil, "priority", "Field must be between 0 and 1000."
	}
	if !validTypes[typ] {
		return nil, "type", "Invalid record type."
	}
	if content == "" {
		return nil, "content", "Field must not be empty."
	}
	if strings.ContainsAny(name, " /\\") {
		return nil, "name", "Invalid hostname."
	}
	fqdn := z.Zone
	if name != "" && name != "@" {
		fqdn = strings.ToLower(name) + "." + z.Zone
	}
	if typ == "TXT" {
		content = strconv.Quote(content)
	}
	dis := int64(0)
	if disabled {
		dis = 1
	}
	return &record{Name: fqdn, Type: typ, Content: content, TTL: ttl, Priority: prio, Disabled: dis}, "", ""
}

func toInt(v any) (int64, error) {
	switch n := v.(type) {
	case json.Number:
		return n.Int64()
	case string:
		return strconv.ParseInt(n, 10, 64)
	default:
		return 0, fmt.Errorf("not a number: %v", v)
	}
}
