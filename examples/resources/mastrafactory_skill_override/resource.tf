resource "mastrafactory_skill_override" "planning" {
  project_id   = var.factory_project_id
  name         = "factory-plan"
  description  = "Produce and submit an implementation plan for human approval"
  instructions = file("${path.module}/skills/factory-plan.md")
}

variable "factory_project_id" {
  type        = string
  description = "Factory Project UUID."
}
