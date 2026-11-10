terraform {
  required_providers {
    mastrafactory = {
      source  = "SmadjaPaul/mastrafactory"
      version = "~> 0.1"
    }
  }
}
provider "mastrafactory" {
  endpoint = var.factory_endpoint
}

variable "factory_endpoint" {
  type        = string
  description = "The Factory endpoint pinned by the credentialed runner."
}
