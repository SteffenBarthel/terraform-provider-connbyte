data "connbyte_dns_zone" "example" {
  name = "example.com"
}

output "zone_id" {
  value = data.connbyte_dns_zone.example.id
}
