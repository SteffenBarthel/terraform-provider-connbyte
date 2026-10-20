package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/SteffenBarthel/terraform-provider-connbyte/internal/mockapi"
)

const testToken = "test-token"

var protoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"connbyte": providerserver.NewProtocol6WithError(New("test")()),
}

// These tests run the real Terraform (or OpenTofu, via TF_ACC_TERRAFORM_PATH) binary against the
// in-memory mock API. They never talk to api.connbyte.com.

func providerConfig(m *mockapi.Server) string {
	return fmt.Sprintf(`
provider "connbyte" {
  api_url   = %q
  api_token = %q
}
`, m.URL, testToken)
}

func TestDNSZoneDataSource(t *testing.T) {
	m := mockapi.New(testToken)
	defer m.Close()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(m) + `
data "connbyte_dns_zone" "by_name" {
  name = "EXAMPLE.com."
}

data "connbyte_dns_zone" "by_id" {
  id = "` + mockapi.ZoneOtherID + `"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.connbyte_dns_zone.by_name", "id", mockapi.ZoneExampleID),
					resource.TestCheckResourceAttr("data.connbyte_dns_zone.by_name", "name", "example.com"),
					resource.TestCheckResourceAttr("data.connbyte_dns_zone.by_name", "project_id", mockapi.ProjectID),
					resource.TestCheckResourceAttr("data.connbyte_dns_zone.by_name", "updated_at", ""),
					resource.TestCheckResourceAttr("data.connbyte_dns_zone.by_id", "name", "other.net"),
					resource.TestCheckResourceAttr("data.connbyte_dns_zone.by_id", "title", "Other"),
				),
			},
			{
				Config:      providerConfig(m) + `data "connbyte_dns_zone" "x" { name = "missing.org" }`,
				ExpectError: regexp.MustCompile(`ConnByte DNS zone not found`),
			},
			{
				Config:      providerConfig(m) + `data "connbyte_dns_zone" "x" {}`,
				ExpectError: regexp.MustCompile(`Exactly one of these attributes must be configured`),
			},
			{
				// Valid last step so the post-test destroy can run.
				Config: providerConfig(m),
			},
		},
	})
}

func TestDNSRecordResource(t *testing.T) {
	m := mockapi.New(testToken)
	defer m.Close()

	cfg := func(name, content string, ttl int, disabled bool) string {
		return providerConfig(m) + fmt.Sprintf(`
data "connbyte_dns_zone" "main" {
  name = "example.com"
}

resource "connbyte_dns_record" "txt" {
  zone_id  = data.connbyte_dns_zone.main.id
  name     = %q
  type     = "TXT"
  content  = %q
  ttl      = %d
  disabled = %t
}

resource "connbyte_dns_record" "mx" {
  zone_id  = data.connbyte_dns_zone.main.id
  name     = "@"
  type     = "MX"
  content  = "mail.example.com."
  priority = 10
}

resource "connbyte_dns_record" "a" {
  zone_id = data.connbyte_dns_zone.main.id
  name    = "www"
  type    = "A"
  content = "192.0.2.1"
}
`, name, content, ttl, disabled)
	}

	var firstID string
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6ProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			if n := m.RecordCount(mockapi.ZoneExampleID); n != 2 {
				return fmt.Errorf("expected only the 2 NS records after destroy, got %d", n)
			}
			if n := m.RecordCount(mockapi.ZoneOtherID); n != 1 {
				return fmt.Errorf("records of other zones must not change, got %d", n)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg("_acme-challenge", `v=1 "quoted"`, 120, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "name", "_acme-challenge"),
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "fqdn", "_acme-challenge.example.com"),
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "content", `v=1 "quoted"`),
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "ttl", "120"),
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "priority", "0"),
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "disabled", "false"),
					resource.TestCheckResourceAttr("connbyte_dns_record.mx", "fqdn", "example.com"),
					resource.TestCheckResourceAttr("connbyte_dns_record.mx", "name", "@"),
					resource.TestCheckResourceAttr("connbyte_dns_record.mx", "content", "mail.example.com."),
					resource.TestCheckResourceAttr("connbyte_dns_record.mx", "priority", "10"),
					resource.TestCheckResourceAttr("connbyte_dns_record.a", "ttl", "3600"),
					resource.TestCheckResourceAttrWith("connbyte_dns_record.txt", "id", func(v string) error {
						firstID = v
						return nil
					}),
					func(_ *terraform.State) error {
						rec, ok := m.FindRecord(mockapi.ZoneExampleID, "_acme-challenge.example.com", "TXT")
						if !ok || rec.Content != `"v=1 \"quoted\""` || rec.TTL != 120 {
							return fmt.Errorf("unexpected record in API: %+v", rec)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "connbyte_dns_record.txt",
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources["connbyte_dns_record.txt"]
					return rs.Primary.Attributes["zone_id"] + "/" + rs.Primary.ID, nil
				},
			},
			{
				// In-place update: same ID, new values.
				Config: cfg("_acme-challenge", "v=2", 300, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "content", "v=2"),
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "ttl", "300"),
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "disabled", "true"),
					resource.TestCheckResourceAttrWith("connbyte_dns_record.txt", "id", sameID(&firstID)),
				),
			},
			{
				// Rename is also an in-place update.
				Config: cfg("_acme-challenge.sub", "v=2", 300, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("connbyte_dns_record.txt", "fqdn", "_acme-challenge.sub.example.com"),
					resource.TestCheckResourceAttrWith("connbyte_dns_record.txt", "id", sameID(&firstID)),
				),
			},
			{
				// A record deleted outside Terraform is planned for re-creation, not an error.
				PreConfig: func() {
					rec, _ := m.FindRecord(mockapi.ZoneExampleID, "www.example.com", "A")
					m.DeleteRecordOutOfBand(mockapi.ZoneExampleID, rec.ID)
				},
				Config:             cfg("_acme-challenge.sub", "v=2", 300, true),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg("_acme-challenge.sub", "v=2", 300, true),
				Check:  resource.TestCheckResourceAttr("connbyte_dns_record.a", "fqdn", "www.example.com"),
			},
		},
	})
}

// sameID fails when the record got a new ID, which means Terraform replaced it instead of updating it.
func sameID(first *string) resource.CheckResourceAttrWithFunc {
	return func(v string) error {
		if v != *first {
			return fmt.Errorf("record was replaced: id %s -> %s", *first, v)
		}
		return nil
	}
}

func TestDNSRecordValidation(t *testing.T) {
	m := mockapi.New(testToken)
	defer m.Close()

	record := func(body string) string {
		return providerConfig(m) + `resource "connbyte_dns_record" "x" {
  zone_id = "` + mockapi.ZoneExampleID + `"
` + body + "\n}\n"
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      record(`name = "www"` + "\n" + `type = "PTR"` + "\n" + `content = "x"`),
				ExpectError: regexp.MustCompile(`value must be one of`),
			},
			{
				Config:      record(`name = "www"` + "\n" + `type = "A"` + "\n" + `content = "192.0.2.1"` + "\n" + `ttl = 30`),
				ExpectError: regexp.MustCompile(`must be between 60 and 86400`),
			},
			{
				Config:      record(`name = "www; rm -rf /"` + "\n" + `type = "A"` + "\n" + `content = "192.0.2.1"`),
				ExpectError: regexp.MustCompile(`lower-case host name`),
			},
			{
				Config:      record(`name = "www.example.com"` + "\n" + `type = "A"` + "\n" + `content = "192.0.2.1"`),
				ExpectError: regexp.MustCompile(`Record name must be relative to the zone`),
			},
			{
				Config:      record(`name = "@"` + "\n" + `type = "MX"` + "\n" + `content = "10 mail.example.com"`),
				ExpectError: regexp.MustCompile(`MX preference belongs in priority`),
			},
			{
				Config: providerConfig(m),
			},
		},
	})
	if n := m.RecordCount(mockapi.ZoneExampleID); n != 2 {
		t.Fatalf("validation errors must not create records, got %d", n)
	}
}

func TestProviderMissingToken(t *testing.T) {
	t.Setenv(envAPIToken, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      `data "connbyte_dns_zone" "x" { name = "example.com" }`,
				ExpectError: regexp.MustCompile(`Missing ConnByte API token`),
			},
			{
				Config: `provider "connbyte" {}`,
			},
		},
	})
}

func TestProviderEnvConfig(t *testing.T) {
	m := mockapi.New(testToken)
	defer m.Close()
	t.Setenv(envAPIToken, testToken)
	t.Setenv(envAPIURL, m.URL)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `data "connbyte_dns_zone" "x" { name = "other.net" }`,
				Check:  resource.TestCheckResourceAttr("data.connbyte_dns_zone.x", "id", mockapi.ZoneOtherID),
			},
		},
	})
}
