# terraform-provider-mastrafactory (pre-release)

Small OpenTofu/Terraform provider for **self-hosted Mastra Factory 0.27.0** configuration. It intentionally has no tools for starting sessions, running agents, changing approvals, fetching secrets, or touching Kubernetes. The provider source is maintained in this standalone repository. It remains pre-release and has not been published to the Terraform Registry.

## Implemented surface

| Kind | Name | Factory API |
|---|---|---|
| Resource | `mastrafactory_skill_override` | `GET /web/factory/skills?projectId=`, `PUT/DELETE /web/factory/projects/:id/skills/:name` |
| Data source | `mastrafactory_skill` | Skill catalog GET |
| Data source | `mastrafactory_board` | `GET /web/factory/projects/:id/boards/:boardKey` |

The Board data source is **read-only**. The `work` Board is a code Board in Factory 0.27.0 and rejects edits (`409 code_board`). Never switch its plan approval off to simplify automation. Native Boards-as-data is evolving in upstream 0.28 alpha and is out of scope here.

## Authentication

The provider supports two mechanisms, in order:

- `MASTRAFACTORY_SESSION_TOKEN`: an existing Better Auth **session** token, passed as `Authorization: Bearer`. This must not be confused with an Authentik OIDC access token.
- `MASTRAFACTORY_EMAIL` and `MASTRAFACTORY_PASSWORD`: one sign-in per provider process via `POST /auth/api/sign-in/email`, then the returned session token in Bearer requests. Requires the account to exist, have an email/password credential, and have Factory organization admin permissions. The request includes `Origin` and `rememberMe: false`, as required by the deployed Better Auth configuration (the production public-Origin flow was corrected in the referenced Better Auth integration work).

`MASTRAFACTORY_ENDPOINT` defaults to no value; it must be set or supplied in the provider block. The credentialed runner must independently set `MASTRAFACTORY_TRUSTED_ENDPOINT` to the exact expected endpoint origin; the provider refuses to send credentials or session tokens if the HCL/environment endpoint differs from this runner-controlled pin. When contacting the internal Kubernetes Service, set `MASTRAFACTORY_AUTH_ORIGIN=https://factory.example.com` (or your configured Better Auth public trusted origin). Without this separate origin, Better Auth rejects the headless sign-in with `403 MISSING_OR_NULL_ORIGIN`. HTTP is only allowed to localhost or internal Kubernetes Service DNS; all other endpoints must use HTTPS. Requests reject cross-origin HTTP redirects, have a 25-second timeout, and suppress response bodies from diagnostics to avoid leaking credentials. No credentials are Terraform resource attributes.

**Known constraint:** Factory 0.27 authorization requires an organization admin for skill overrides. This v0.1 provider does not create a reduced-scope Factory API credential. Keep credentials in Doppler/ESO and inject them only into the dedicated runner, not into Git, plan files or Pod environment readable by agents. Internal service DNS does not remove the need for authentication; restrict network egress where practical. The trusted endpoint pin is operational configuration, not a credential: protect it from untrusted pull-request changes in the runner environment.

## Example

```hcl
terraform {
  required_providers {
    mastrafactory = {
      source  = "SmadjaPaul/mastrafactory"
      version = "~> 0.1"
    }
  }
}

provider "mastrafactory" {
  endpoint = "http://mastra-factory.<namespace>.svc.cluster.local:4111"
}

resource "mastrafactory_skill_override" "plan" {
  project_id   = "00000000-0000-4000-8000-000000000000"
  name         = "factory-plan"
  description  = "Produce and submit an implementation plan for human approval"
  instructions = file("${path.module}/skills/factory-plan.md")
}

data "mastrafactory_board" "work" {
  project_id = "00000000-0000-4000-8000-000000000000"
  key        = "work"
}
```

For a local build, install Go 1.24+ and run:

```sh
go mod tidy
go test -race ./...
go vet ./...
go build ./...
```

The module is not yet published in the registry; the above HCL is the target interface and requires a local development override or release before registry installation. The root OpenTofu, backend state, runner credentials and apply permissions are a separate deployment gate.

## State, drift and lifecycle

- Resource identity is the exact `project-uuid/skill-name`. `terraform import mastrafactory_skill_override.plan "project-uuid/skill-name"` imports an existing **edited** skill. Importing a non-edited original results in no managed resource.
- Create refuses to silently adopt an existing UI-created override. Import first.
- Read compares normalized description and instruction text. The Factory 0.27 catalog strips Markdown frontmatter and trims outer whitespace; a trailing newline in `file()` is not considered drift. We intentionally support only `name`, `description`, `instructions`, not arbitrary frontmatter.
- A manual UI edit is visible on the next refresh/plan. If an override disappears, the managed override is removed from state; `apply` can recreate it. No native skill is ever deleted.
- Delete calls native `DELETE` to revert the project override only. It will not remove the original bundled/code/folder skill.
- Update/Delete perform a precondition read and refuse if the remote instructions differ from the state. Factory 0.27 lacks atomic CAS for skill writes: a narrow TOCTOU window remains between the precondition GET and PUT/DELETE. Serialize applies in the state backend, and do not have two configuration owners modifying the same skill concurrently.
- The catalog's `originalChanged` flag is exposed. Review changed upstream originals explicitly before republishing overrides.
- No deletion is inferred on 401, 403 or 5xx errors.

## Pre-deployment qualification gate

1. `go mod tidy`, `go test -race ./...`, `go vet ./...`, `go build ./...` on the exact commit (local environment may not download dependencies).
2. Run the lifecycle acceptance test against a **disposable** Factory 0.27 and its own database (not production): `TF_ACC=1 TF_ACC_TERRAFORM_PATH=$(command -v tofu) MASTRAFACTORY_ACCEPTANCE_DISPOSABLE=I_UNDERSTAND_THIS_IS_DISPOSABLE go test -v ./internal/provider -run TestAccSkillOverrideLifecycle`. Set the disposable endpoint/project/skill, production endpoint guard, trusted endpoint pin and Better Auth credentials in the runner environment. This covers create, read, update, import, no-op plan, simulated remote drift, and destroy/revert. HTTP 401/403/422 and connection failures have deterministic sanitized client tests; a live 401/403 integration requires a disposable identity/project with corresponding permissions and must never use production credentials.
3. Verify runner network access to the internal Service endpoint, runner-pinned `MASTRAFACTORY_TRUSTED_ENDPOINT`, public `MASTRAFACTORY_AUTH_ORIGIN`, and the Better Auth Bearer flow including `Origin`; this live network test is not yet performed. Provision a dedicated service identity only after explicit operator approval.
4. After qualification, configure the repository release signing credentials, publish the signed v0.1.0 release, and add a dedicated root to `homelab-infra`/ARC. Require explicit approval for the first production apply. Never change Factory deployment flags, admission or approvals in this provider.

## Why this is not a generic REST provider

Factory 0.27 `GET /web/factory/skills` is a structured catalog, not an object-level `GET /skills/{id}`. It hides the original frontmatter and returns the resolved override. A bespoke provider can handle this model without brittle JSON-path tricks, while keeping the code and its operational permissions very small.
