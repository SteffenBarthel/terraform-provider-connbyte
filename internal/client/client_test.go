package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SteffenBarthel/terraform-provider-connbyte/internal/client"
	"github.com/SteffenBarthel/terraform-provider-connbyte/internal/mockapi"
)

func TestRecordLifecycle(t *testing.T) {
	m := mockapi.New("test-token")
	defer m.Close()
	c := client.New(m.URL, "test-token", "test")
	ctx := context.Background()

	zone, err := c.FindZoneByName(ctx, "Example.COM.")
	if err != nil {
		t.Fatal(err)
	}
	if zone.ID != mockapi.ZoneExampleID {
		t.Fatalf("zone id = %q", zone.ID)
	}

	id, err := c.CreateRecord(ctx, zone.ID, client.RecordInput{Name: "_acme-challenge", Type: "TXT", Content: `a "quoted" value`, TTL: 120})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := c.GetRecord(ctx, zone.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "_acme-challenge.example.com" || client.UnquoteTXT(rec.Content) != `a "quoted" value` || rec.TTL != 120 {
		t.Fatalf("unexpected record %+v", rec)
	}

	if err := c.UpdateRecord(ctx, zone.ID, id, client.RecordInput{Name: "_acme-challenge", Type: "TXT", Content: "new", TTL: 60, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	rec, err = c.GetRecord(ctx, zone.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if client.UnquoteTXT(rec.Content) != "new" || rec.Disabled != 1 || rec.TTL != 60 {
		t.Fatalf("update not applied: %+v", rec)
	}

	if err := c.DeleteRecord(ctx, zone.ID, id); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteRecord(ctx, zone.ID, id); err != nil {
		t.Fatalf("second delete must be a no-op, got %v", err)
	}
	if _, err := c.GetRecord(ctx, zone.ID, id); !client.IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestErrors(t *testing.T) {
	m := mockapi.New("test-token")
	defer m.Close()
	ctx := context.Background()

	_, err := client.New(m.URL, "wrong", "test").ListZones(ctx)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusUnauthorized || client.IsNotFound(err) {
		t.Fatalf("expected 401 auth error, got %v", err)
	}
	if _, err := client.New(m.URL, "wrong", "test").GetZone(ctx, mockapi.ZoneExampleID); err == nil || client.IsNotFound(err) {
		t.Fatalf("auth error must not look like a missing zone, got %v", err)
	}

	c := client.New(m.URL, "test-token", "test")
	if _, err := c.GetZone(ctx, "01NOPE"); !client.IsNotFound(err) {
		t.Fatalf("expected zone not found, got %v", err)
	}
	if _, err := c.FindZoneByName(ctx, "missing.org"); !client.IsNotFound(err) {
		t.Fatalf("expected zone not found, got %v", err)
	}
	_, err = c.CreateRecord(ctx, mockapi.ZoneExampleID, client.RecordInput{Name: "x", Type: "A", Content: "192.0.2.1", TTL: 10})
	if err == nil || client.IsNotFound(err) {
		t.Fatalf("expected validation error, got %v", err)
	}

	if _, err := client.New(m.URL, "", "test").ListZones(ctx); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestNonJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer srv.Close()
	c := client.New(srv.URL, "t", "test")
	c.RetryWait = time.Millisecond
	_, err := c.ListZones(context.Background())
	apiErr, ok := err.(*client.APIError)
	if !ok || apiErr.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("expected APIError 502, got %v", err)
	}
}

func TestNames(t *testing.T) {
	cases := []struct{ fqdn, zone, rel string }{
		{"example.com", "example.com", "@"},
		{"www.example.com", "example.com.", "www"},
		{"A.B.Example.com.", "example.com", "a.b"},
	}
	for _, tc := range cases {
		if got := client.RelativeName(tc.fqdn, tc.zone); got != tc.rel {
			t.Errorf("RelativeName(%q,%q)=%q want %q", tc.fqdn, tc.zone, got, tc.rel)
		}
	}
}

func TestUnquoteTXT(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"v=spf1 -all"`, "v=spf1 -all"},
		{`"a \"quoted\" value"`, `a "quoted" value`},
		{`"back\\slash"`, `back\slash`},
		{`"part1" "part2"`, "part1part2"},
		{`"part1"  "part2" "3"`, "part1part23"},
		{`"\065BC"`, "ABC"},
		{`plain text`, "plain text"},
		{`"unterminated`, `"unterminated`},
		{`"a" b`, `"a" b`},
		{``, ``},
		{`""`, ``},
	}
	for _, tc := range cases {
		if got := client.UnquoteTXT(tc.in); got != tc.want {
			t.Errorf("UnquoteTXT(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLongTXT(t *testing.T) {
	m := mockapi.New("test-token")
	defer m.Close()
	c := client.New(m.URL, "test-token", "test")
	ctx := context.Background()

	long := "v=DKIM1; k=rsa; p=" + strings.Repeat("MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A", 12)
	id, err := c.CreateRecord(ctx, mockapi.ZoneExampleID, client.RecordInput{Name: "sel._domainkey", Type: "TXT", Content: long, TTL: 3600})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := c.GetRecord(ctx, mockapi.ZoneExampleID, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Content, `" "`) {
		t.Fatalf("mock should split long TXT content, got %q", rec.Content)
	}
	if got := client.UnquoteTXT(rec.Content); got != long {
		t.Fatalf("long TXT round trip failed:\n got %q\nwant %q", got, long)
	}
}

// TestNotFoundIsNotGuessed checks that an HTTP 404 for an unknown path (for example a wrong
// api_url) is not taken as "record deleted".
func TestNotFoundIsNotGuessed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":{"code":404,"title":"Not Found","message":"Resource not found."}}`))
	}))
	defer srv.Close()
	c := client.New(srv.URL, "t", "test")
	c.RetryWait = time.Millisecond
	ctx := context.Background()

	if _, err := c.GetZone(ctx, mockapi.ZoneExampleID); err == nil || client.IsNotFound(err) {
		t.Fatalf("GetZone: expected a plain error, got %v", err)
	}
	if _, err := c.GetRecord(ctx, mockapi.ZoneExampleID, 1); err == nil || client.IsNotFound(err) {
		t.Fatalf("GetRecord: expected a plain error, got %v", err)
	}
	if err := c.DeleteRecord(ctx, mockapi.ZoneExampleID, 1); err == nil || client.IsNotFound(err) {
		t.Fatalf("DeleteRecord: expected a plain error, got %v", err)
	}
}

func TestDeleteChecksRecordList(t *testing.T) {
	m := mockapi.New("test-token")
	defer m.Close()
	c := client.New(m.URL, "test-token", "test")
	c.RetryWait = time.Millisecond
	ctx := context.Background()

	// The API rejects the delete, but the record still exists: the error must be returned.
	m.FailNext("/ns/record/delete", http.StatusBadRequest, 1)
	if err := c.DeleteRecord(ctx, mockapi.ZoneOtherID, 3); err == nil {
		t.Fatal("expected an error while the record still exists")
	}
	if m.RecordCount(mockapi.ZoneOtherID) != 1 {
		t.Fatal("record must still exist")
	}

	// A 401 is never checked against the list.
	before := m.CallCount("/ns/record/list")
	if err := client.New(m.URL, "wrong", "test").DeleteRecord(ctx, mockapi.ZoneOtherID, 3); err == nil {
		t.Fatal("expected auth error")
	}
	if m.CallCount("/ns/record/list") != before {
		t.Fatal("auth errors must not trigger a record list lookup")
	}
}

func TestReadRetry(t *testing.T) {
	m := mockapi.New("test-token")
	defer m.Close()
	c := client.New(m.URL, "test-token", "test")
	c.RetryWait = time.Millisecond
	ctx := context.Background()

	m.FailNext("/ns/record/list", http.StatusLocked, 2)
	if _, err := c.ListRecords(ctx, mockapi.ZoneExampleID); err != nil {
		t.Fatalf("read must succeed after retries, got %v", err)
	}
	if n := m.CallCount("/ns/record/list"); n != 3 {
		t.Fatalf("expected 3 list calls, got %d", n)
	}

	m.FailNext("/ns/record/list", http.StatusInternalServerError, 10)
	if _, err := c.ListRecords(ctx, mockapi.ZoneExampleID); err == nil || client.IsNotFound(err) {
		t.Fatalf("expected a server error after all retries, got %v", err)
	}

	// Writes are never retried, so a lost answer cannot create a record twice.
	m.FailNext("/ns/record/create", http.StatusInternalServerError, 1)
	before := m.CallCount("/ns/record/create")
	if _, err := c.CreateRecord(ctx, mockapi.ZoneExampleID, client.RecordInput{Name: "x", Type: "A", Content: "192.0.2.1", TTL: 60}); err == nil {
		t.Fatal("expected create error")
	}
	if n := m.CallCount("/ns/record/create") - before; n != 1 {
		t.Fatalf("create must be sent once, got %d", n)
	}
}

func TestHeaders(t *testing.T) {
	m := mockapi.New("test-token")
	defer m.Close()
	if _, err := client.New(m.URL, "test-token", "test/1").ListZones(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := m.Calls()[0].Header
	if h.Get("Accept-Language") != "en-US" || h.Get("User-Agent") != "test/1" {
		t.Fatalf("unexpected headers %v", h)
	}
}
