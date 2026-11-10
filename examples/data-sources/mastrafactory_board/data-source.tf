data "mastrafactory_board" "work" {
  project_id = var.factory_project_id
  key        = "work"
}

variable "factory_project_id" {
  type        = string
  description = "Factory Project UUID."
}

output "work_board_origin" {
  value = data.mastrafactory_board.work.source
}

output "work_board_phases" {
  value = jsondecode(data.mastrafactory_board.work.phases_json)
}
