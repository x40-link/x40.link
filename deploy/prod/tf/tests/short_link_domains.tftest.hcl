# Exercise DNS and TLS wiring without contacting Google Cloud.
mock_provider "google" {}

run "shp7_link_domain" {
  command = plan

  # Keep unrelated IAM and state-storage resources outside this test.
  plan_options {
    target = [
      google_dns_record_set.shp7-link,
      google_compute_target_https_proxy.x40-link,
    ]
  }

  assert {
    condition     = google_dns_managed_zone.shp7-link.dns_name == "shp7.link."
    error_message = "The short-link domain must have an authoritative DNS zone."
  }

  assert {
    condition = (
      google_dns_record_set.shp7-link["shp7.link"].name == "shp7.link." &&
      google_dns_record_set.shp7-link["shp7.link"].managed_zone == google_dns_managed_zone.shp7-link.name &&
      google_dns_record_set.shp7-link["shp7.link"].type == "A" &&
      google_dns_record_set.shp7-link["shp7.link"].ttl == 300 &&
      google_dns_record_set.shp7-link["shp7.link"].rrdatas == tolist([google_compute_global_address.x40-link.address])
    )
    error_message = "The domain apex must point to the shared load balancer."
  }

  assert {
    condition     = google_compute_managed_ssl_certificate.shp7-link.managed[0].domains == tolist(["shp7.link"])
    error_message = "The domain must have a dedicated managed TLS certificate."
  }

  assert {
    condition = toset(google_compute_target_https_proxy.x40-link.ssl_certificates) == toset([
      google_compute_managed_ssl_certificate.all-link-shorteners-v4.id,
      google_compute_managed_ssl_certificate.x5e-link.id,
      google_compute_managed_ssl_certificate.shp7-link.id,
    ])
    error_message = "The HTTPS proxy must serve all short-link certificates."
  }
}
