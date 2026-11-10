package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SmadjaPaul/terraform-provider-mastrafactory/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const acceptanceTestProjectID = "00000000-0000-4000-8000-000000000001"

func TestCreatePreservesIdentityForAmbiguousWriteAndReadReconciles(t *testing.T) {
	var committed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/web/factory/skills":
			edited := any(nil)
			description, content := "Upstream plan", "Upstream instructions"
			if committed {
				edited = map[string]string{"updatedAt": "2026-11-10T00:00:00Z"}
				description, content = "Plan", "Instructions"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"skills": []any{map[string]any{
				"name": "factory-plan", "description": description, "content": content,
				"origin": "code", "edited": edited,
			}}})
		case r.Method == http.MethodPut:
			committed = true
			http.Error(w, "lost response", http.StatusUnprocessableEntity)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, err := client.New(client.Config{Endpoint: server.URL, TrustedEndpoint: server.URL, SessionToken: "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	r := &SkillOverrideResource{client: c}
	var schemaResponse resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResponse)
	if schemaResponse.Diagnostics.HasError() {
		t.Fatalf("schema error: %v", schemaResponse.Diagnostics)
	}
	tfType := schemaResponse.Schema.Type().(interface {
		TerraformType(context.Context) tftypes.Type
	}).TerraformType(context.Background())
	planRaw := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"project_id":       tftypes.NewValue(tftypes.String, acceptanceTestProjectID),
		"name":             tftypes.NewValue(tftypes.String, "factory-plan"),
		"description":      tftypes.NewValue(tftypes.String, "Plan"),
		"instructions":     tftypes.NewValue(tftypes.String, "Instructions"),
		"origin":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"original_changed": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
	})
	req := resource.CreateRequest{Plan: tfsdk.Plan{Raw: planRaw, Schema: schemaResponse.Schema}}
	resp := resource.CreateResponse{State: tfsdk.State{Raw: planRaw, Schema: schemaResponse.Schema}}
	r.Create(context.Background(), req, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected ambiguous PUT error")
	}
	var afterCreate skillOverrideModel
	if diags := resp.State.Get(context.Background(), &afterCreate); diags.HasError() {
		t.Fatalf("could not read provisional state: %v", diags)
	}
	if afterCreate.ID.ValueString() != acceptanceTestProjectID+"/factory-plan" {
		t.Fatalf("provisional identity missing after failed create: %q", afterCreate.ID.ValueString())
	}

	// Even if OpenTofu discarded partial state after the failed response, a
	// later Create adopts only the exact planned content instead of writing a
	// second time or leaving an unmanaged override behind.
	retryResponse := resource.CreateResponse{State: tfsdk.State{Raw: planRaw, Schema: schemaResponse.Schema}}
	r.Create(context.Background(), req, &retryResponse)
	if retryResponse.Diagnostics.HasError() {
		t.Fatalf("exact remote match was not reconciled on retry: %v", retryResponse.Diagnostics)
	}

	readResponse := resource.ReadResponse{State: retryResponse.State}
	r.Read(context.Background(), resource.ReadRequest{State: retryResponse.State}, &readResponse)
	if readResponse.Diagnostics.HasError() {
		t.Fatalf("read failed to reconcile committed override: %v", readResponse.Diagnostics)
	}
	var reconciled skillOverrideModel
	if diags := readResponse.State.Get(context.Background(), &reconciled); diags.HasError() {
		t.Fatalf("could not read reconciled state: %v", diags)
	}
	if reconciled.Description.ValueString() != "Plan" || reconciled.Instructions.ValueString() != "Instructions" {
		t.Fatalf("unexpected recovered state: %#v", reconciled)
	}
}
