package provider

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/SmadjaPaul/terraform-provider-mastrafactory/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const disposableAcknowledgement = "I_UNDERSTAND_THIS_IS_DISPOSABLE"

func canonicalOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ""
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}

func acceptanceValues(t *testing.T) (endpoint, project, skill string) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 only for a disposable Factory 0.27 acceptance environment")
	}
	if os.Getenv("MASTRAFACTORY_ACCEPTANCE_DISPOSABLE") != disposableAcknowledgement {
		t.Fatal("acceptance run requires MASTRAFACTORY_ACCEPTANCE_DISPOSABLE=" + disposableAcknowledgement)
	}
	endpoint = strings.TrimSpace(os.Getenv("MASTRAFACTORY_ACCEPTANCE_ENDPOINT"))
	productionEndpoint := strings.TrimSpace(os.Getenv("MASTRAFACTORY_PRODUCTION_ENDPOINT"))
	disposableEndpoint := strings.TrimSpace(os.Getenv("MASTRAFACTORY_ACCEPTANCE_TRUSTED_ENDPOINT"))
	project = strings.TrimSpace(os.Getenv("MASTRAFACTORY_ACCEPTANCE_PROJECT_ID"))
	skill = strings.TrimSpace(os.Getenv("MASTRAFACTORY_ACCEPTANCE_SKILL_NAME"))
	productionProject := strings.TrimSpace(os.Getenv("MASTRAFACTORY_PRODUCTION_PROJECT_ID"))
	if endpoint == "" || productionEndpoint == "" || disposableEndpoint == "" || project == "" || productionProject == "" || skill == "" {
		t.Fatal("acceptance endpoint, protected production endpoint and project, disposable endpoint and project UUID, and existing skill name are required")
	}
	acceptanceOrigin, productionOrigin, disposableOrigin := canonicalOrigin(endpoint), canonicalOrigin(productionEndpoint), canonicalOrigin(disposableEndpoint)
	if acceptanceOrigin == "" || productionOrigin == "" || disposableOrigin == "" {
		t.Fatal("acceptance and production endpoints must be valid HTTP(S) origins without paths, query, or user info")
	}
	if acceptanceOrigin == productionOrigin {
		t.Fatal("refusing to run acceptance tests against the production Factory endpoint")
	}
	if acceptanceOrigin != disposableOrigin || canonicalOrigin(os.Getenv("MASTRAFACTORY_TRUSTED_ENDPOINT")) != disposableOrigin {
		t.Fatal("acceptance endpoint must match the independently runner-pinned disposable endpoint")
	}
	if project == productionProject {
		t.Fatal("refusing to run acceptance tests against the production Factory project")
	}
	return endpoint, project, skill
}

func TestAccSkillOverrideLifecycle(t *testing.T) {
	endpoint, project, skill := acceptanceValues(t)
	ctx := context.Background()
	api, err := client.New(client.Config{
		Endpoint: endpoint, TrustedEndpoint: os.Getenv("MASTRAFACTORY_TRUSTED_ENDPOINT"),
		AuthOrigin: os.Getenv("MASTRAFACTORY_AUTH_ORIGIN"), Email: os.Getenv("MASTRAFACTORY_EMAIL"),
		Password: os.Getenv("MASTRAFACTORY_PASSWORD"), SessionToken: os.Getenv("MASTRAFACTORY_SESSION_TOKEN"),
	})
	if err != nil {
		t.Fatal("invalid acceptance client configuration")
	}
	baseline, err := api.Skill(ctx, project, skill)
	if err != nil {
		t.Fatalf("disposable Factory preflight failed: %v", err)
	}
	if baseline.Edited != nil {
		t.Fatal("selected skill already has a project override; use a fresh disposable project or restore it first")
	}

	providerConfig := fmt.Sprintf(`provider "mastrafactory" { endpoint = %q }`, endpoint)
	resourceConfig := func(description, instructions string) string {
		return providerConfig + fmt.Sprintf(`
resource "mastrafactory_skill_override" "test" {
  project_id = %q
  name = %q
  description = %q
  instructions = %q
}
`, project, skill, description, instructions)
	}

	check := func(description, instructions string) resource.TestCheckFunc {
		return resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr("mastrafactory_skill_override.test", "description", description),
			resource.TestCheckResourceAttr("mastrafactory_skill_override.test", "instructions", instructions),
		)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){
			"mastrafactory": providerserver.NewProtocol6WithError(New("acceptance")()),
		},
		Steps: []resource.TestStep{
			{Config: resourceConfig("Acceptance baseline", "test instructions one"), Check: check("Acceptance baseline", "test instructions one")},
			{Config: resourceConfig("Acceptance baseline", "test instructions one"), PlanOnly: true, ExpectNonEmptyPlan: false},
			{
				Config: resourceConfig("Acceptance baseline", "test instructions one"),
				PreConfig: func() {
					if err := api.UpsertSkill(ctx, project, skill, "Out-of-band drift", "drifted instructions"); err != nil {
						t.Fatalf("could not create disposable drift fixture: %v", err)
					}
				},
				Check: check("Acceptance baseline", "test instructions one"),
			},
			{Config: resourceConfig("Acceptance updated", "test instructions two"), Check: check("Acceptance updated", "test instructions two")},
			{
				ResourceName: "mastrafactory_skill_override.test", ImportState: true, ImportStateVerify: true,
				ImportStatePersist: true, Check: check("Acceptance updated", "test instructions two"),
			},
			{Config: resourceConfig("Acceptance updated", "test instructions two"), PlanOnly: true, ExpectNonEmptyPlan: false},
		},
		CheckDestroy: func(*terraform.State) error {
			remote, err := api.Skill(ctx, project, skill)
			if err != nil {
				return fmt.Errorf("could not verify Factory override revert: %w", err)
			}
			if remote.Edited != nil {
				return fmt.Errorf("Factory override still exists after OpenTofu destroy")
			}
			return nil
		},
	})
}

func TestAcceptanceOriginNormalization(t *testing.T) {
	if canonicalOrigin("https://factory.example/") != canonicalOrigin("https://factory.example") {
		t.Fatal("trailing slash must not bypass production endpoint comparison")
	}
	for _, invalid := range []string{"https://factory.example/path", "https://user:pass@factory.example", "https://factory.example/?x=1", "not a URL"} {
		if canonicalOrigin(invalid) != "" {
			t.Errorf("accepted non-origin endpoint %q", invalid)
		}
	}
}
