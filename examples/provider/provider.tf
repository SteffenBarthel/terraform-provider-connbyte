terraform {
  required_providers {
    connbyte = {
      source = "SteffenBarthel/connbyte"
    }
  }
}

# The API token can also be set with the CONNBYTE_API_TOKEN environment variable.
provider "connbyte" {
  api_token = var.connbyte_api_token
}

variable "connbyte_api_token" {
  type      = string
  sensitive = true
}
