# CR are the resources that are required to access the Google Cloud Run app. Notably, the app itself is not defined
# here as it is controlled on a separate lifecycle.

# Setup DNS resources for the service
resource "google_dns_managed_zone" "x40-link" {
  name        = "x40-link"
  dns_name    = "x40.link."
  description = "The short link service."
}

resource "google_dns_managed_zone" "x40-dev" {
  name        = "x40-dev"
  dns_name    = "x40.dev."
  description = "The developer documentation for the x40 project."
}

resource "google_dns_managed_zone" "h4n-me" {
  name        = "h4n-me"
  dns_name    = "h4n.me."
  description = "A short domain for 'Howden'"
}

resource "google_dns_managed_zone" "dhse-link" {
  name        = "dhse-link"
  dns_name    = "dhse.link."
  description = "A short domain for 'DHSE'"
}

resource "google_dns_managed_zone" "x5e-link" {
  name        = "x5e-link"
  dns_name    = "0x5e.link."
  description = "A short domain for 0x5e"
}

resource "google_dns_managed_zone" "shp7-link" {
  name        = "shp7-link"
  dns_name    = "shp7.link."
  description = "A short domain for shp7"
}

resource "google_dns_record_set" "x40-dev__ALIAS" {
  managed_zone = google_dns_managed_zone.x40-dev.name
  name         = google_dns_managed_zone.x40-dev.dns_name
  ttl          = 300
  type         = "ALIAS"

  rrdatas = ["andrewhowdencom.github.io."]
}

resource "google_dns_record_set" "x40-dev__CNAME" {
  managed_zone = google_dns_managed_zone.x40-dev.name
  name         = "www.${google_dns_managed_zone.x40-dev.dns_name}"
  ttl          = 300
  type         = "CNAME"

  rrdatas = ["andrewhowdencom.github.io."]

}

resource "google_dns_record_set" "x40-link" {
  name         = "${each.value}."
  for_each     = toset(var.x40_link_domains)
  managed_zone = google_dns_managed_zone.x40-link.name
  ttl          = 300
  type         = "A"

  rrdatas = [
    google_compute_global_address.x40-link.address
  ]
}

resource "google_dns_record_set" "h4n-me" {
  for_each     = toset(var.h4n_me_domains)
  name         = "${each.value}."
  managed_zone = google_dns_managed_zone.h4n-me.name
  ttl          = 300
  type         = "A"

  rrdatas = [
    google_compute_global_address.x40-link.address
  ]
}

resource "google_dns_record_set" "dhse-link" {
  for_each     = toset(var.dhse_link_domains)
  name         = "${each.value}."
  managed_zone = google_dns_managed_zone.dhse-link.name
  ttl          = 300
  type         = "A"

  rrdatas = [
    google_compute_global_address.x40-link.address
  ]
}

resource "google_dns_record_set" "x5e-link" {
  for_each     = toset(var.x5e_link_domains)
  name         = "${each.value}."
  managed_zone = google_dns_managed_zone.x5e-link.name
  ttl          = 300
  type         = "A"

  rrdatas = [
    google_compute_global_address.x40-link.address
  ]
}

resource "google_dns_record_set" "shp7-link" {
  for_each     = toset(var.shp7_link_domains)
  name         = "${each.value}."
  managed_zone = google_dns_managed_zone.shp7-link.name
  ttl          = 300
  type         = "A"

  rrdatas = [
    google_compute_global_address.x40-link.address
  ]
}

# Setup network path to access the service
resource "google_compute_global_address" "x40-link" {
  name = "x40-link"
}

resource "google_compute_managed_ssl_certificate" "all-link-shorteners-v4" {
  name = "all-link-shorteners-v4"

  managed {
    domains = concat(var.x40_link_domains, var.dhse_link_domains, ["andrewhowden.com"])
  }
}

resource "google_compute_managed_ssl_certificate" "x5e-link" {
  name = "x5e-link"

  managed {
    domains = var.x5e_link_domains
  }
}

resource "google_compute_managed_ssl_certificate" "shp7-link" {
  name = "shp7-link"

  managed {
    domains = var.shp7_link_domains
  }
}

resource "google_compute_region_network_endpoint_group" "x40-link" {
  name                  = "x40-link"
  network_endpoint_type = "SERVERLESS"
  region                = "europe-west3"

  cloud_run {
    service = "x40-link"
  }
}

resource "google_compute_backend_service" "x40-link" {
  name                  = "x40-link"
  load_balancing_scheme = "EXTERNAL_MANAGED"


  backend {
    group = google_compute_region_network_endpoint_group.x40-link.id
  }
}

resource "google_compute_url_map" "x40-link" {
  name            = "x40-link"
  default_service = google_compute_backend_service.x40-link.id
}

resource "google_compute_target_https_proxy" "x40-link" {
  name    = "x40-link"
  url_map = google_compute_url_map.x40-link.id

  ssl_certificates = [
    google_compute_managed_ssl_certificate.all-link-shorteners-v4.id,
    google_compute_managed_ssl_certificate.x5e-link.id,
    google_compute_managed_ssl_certificate.shp7-link.id
  ]
}

resource "google_compute_global_forwarding_rule" "x40-link" {
  name                  = "x40-link"
  load_balancing_scheme = "EXTERNAL_MANAGED"

  ip_address = google_compute_global_address.x40-link.id
  target     = google_compute_target_https_proxy.x40-link.id
  port_range = "443"


}

# HTTP to HTTPS redirect(s)
resource "google_compute_url_map" "http-to-https" {
  name = "http-to-https"

  default_url_redirect {
    https_redirect         = true
    redirect_response_code = "MOVED_PERMANENTLY_DEFAULT"
    strip_query            = false
  }
}

resource "google_compute_target_http_proxy" "http-to-https" {
  name    = "http-to-https"
  url_map = google_compute_url_map.http-to-https.id
}

resource "google_compute_global_forwarding_rule" "http-to-https" {
  name                  = "http-to-https"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  ip_address            = google_compute_global_address.x40-link.id
  target                = google_compute_target_http_proxy.http-to-https.id
  port_range            = "80"
}


# Public access to the service
# Allow the general compute user to assume service accounts.
resource "google_cloud_run_service_iam_binding" "default" {
  location = var.region
  service  = var.cloud-run__x40-link
  role     = "roles/run.invoker"
  members = [
    "allUsers"
  ]
}
