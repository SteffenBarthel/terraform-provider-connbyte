data "connbyte_dns_zone" "example" {
  name = "example.com"
}

resource "connbyte_dns_record" "www" {
  zone_id = data.connbyte_dns_zone.example.id
  name    = "www"
  type    = "A"
  content = "192.0.2.10"
  ttl     = 3600
}

resource "connbyte_dns_record" "apex_mx" {
  zone_id  = data.connbyte_dns_zone.example.id
  name     = "@"
  type     = "MX"
  content  = "mail.example.com"
  priority = 10
}

resource "connbyte_dns_record" "spf" {
  zone_id = data.connbyte_dns_zone.example.id
  name    = "@"
  type    = "TXT"
  content = "v=spf1 mx -all"
}
