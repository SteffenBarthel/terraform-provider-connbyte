# Terraform / OpenTofu Provider for ConnByte DNS

Manage DNS records for domains hosted at [ConnByte](https://connbyte.com/) with
[Terraform](https://www.terraform.io/) or [OpenTofu](https://opentofu.org/).

- Resource `connbyte_dns_record`: A, AAAA, CNAME, MX, NS, SRV and TXT records
- Data source `connbyte_dns_zone`: look up a zone by name or ID

Documentation: [docs/](docs/index.md)

## Usage

```terraform
terraform {
  required_providers {
    connbyte = {
      source = "SteffenBarthel/connbyte"
    }
  }
}

provider "connbyte" {
  # or set CONNBYTE_API_TOKEN
  api_token = var.connbyte_api_token
}

data "connbyte_dns_zone" "example" {
  name = "example.com"
}

resource "connbyte_dns_record" "www" {
  zone_id = data.connbyte_dns_zone.example.id
  name    = "www"
  type    = "A"
  content = "192.0.2.10"
}
```

Create the API token in your [ConnByte](https://connbyte.com/) account.

## Development

Requirements: [Go](https://go.dev/) (see `go.mod`) and a `terraform` or `tofu` binary.

```shell
go build ./...
make test       # unit and acceptance tests against an in-memory mock API
make lint       # golangci-lint
make generate   # format examples and regenerate docs/
```

The tests never call the real API. To run them with OpenTofu:

```shell
TF_ACC_TERRAFORM_PATH=$(command -v tofu) TF_ACC_PROVIDER_HOST=registry.opentofu.org \
TF_ACC_PROVIDER_NAMESPACE=SteffenBarthel go test ./...
```

To try a local build with your own configuration, add a
[development override](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers)
for `SteffenBarthel/connbyte` that points to `$(go env GOPATH)/bin` and run `go install .`.

## License

MIT, see [LICENSE](LICENSE).
