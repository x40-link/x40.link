variable "x40_link_domains" {
  type    = list(string)
  default = ["x40.link", "www.x40.link", "app.x40.link", "api.x40.link"]
}

variable "h4n_me_domains" {
  type    = list(string)
  default = ["h4n.me", "www.h4n.me"]
}

variable "dhse_link_domains" {
  type    = list(string)
  default = ["dhse.link"]
}

variable "x5e_link_domains" {
  type    = list(string)
  default = ["0x5e.link"]
}

variable "shp7_link_domains" {
  type    = list(string)
  default = ["shp7.link"]
}

variable "cloud-run__x40-link" {
  type    = string
  default = "x40-link"
}

variable "region" {
  type    = string
  default = "europe-west3"
}
