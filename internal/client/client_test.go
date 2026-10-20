package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if err == nil || client.IsNotFound(err) {
		t.Fatalf("expected auth error, got %v", err)
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
	_, err := client.New(srv.URL, "t", "test").ListZones(context.Background())
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
	if got := client.FQDN("@", "example.com."); got != "example.com" {
		t.Errorf("FQDN apex = %q", got)
	}
	if got := client.FQDN("www", "example.com"); got != "www.example.com" {
		t.Errorf("FQDN www = %q", got)
	}
	if got := client.UnquoteTXT(`"v=spf1 -all"`); got != "v=spf1 -all" {
		t.Errorf("UnquoteTXT = %q", got)
	}
}
