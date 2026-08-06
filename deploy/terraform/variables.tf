variable "region" {
  type    = string
  default = "us-east-1"
}

variable "name" {
  description = "Resource name prefix"
  type        = string
  default     = "blundernet"
}

variable "db_password" {
  description = "Postgres master password"
  type        = string
  sensitive   = true
}

variable "api_count" {
  description = "Baseline number of api tasks behind the ALB"
  type        = number
  default     = 2
}

variable "image_tag" {
  description = "Tag of the api/worker images in ECR"
  type        = string
  default     = "latest"
}
