package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/SmadjaPaul/terraform-provider-mastrafactory/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = (*SkillOverrideResource)(nil)
var _ resource.ResourceWithConfigure = (*SkillOverrideResource)(nil)
var _ resource.ResourceWithImportState = (*SkillOverrideResource)(nil)

type SkillOverrideResource struct{ client *client.Client }

type skillOverrideModel struct {
	ID              types.String `tfsdk:"id"`
	ProjectID       types.String `tfsdk:"project_id"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	Instructions    types.String `tfsdk:"instructions"`
	Origin          types.String `tfsdk:"origin"`
	OriginalChanged types.Bool   `tfsdk:"original_changed"`
}

func (r *SkillOverrideResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_skill_override"
}
func (r *SkillOverrideResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "An override of a pre-existing Factory skill; destroy reverts to upstream, never deletes the original skill.", Attributes: map[string]schema.Attribute{
		"id":               schema.StringAttribute{Computed: true, Description: "Import ID: project-uuid/skill-name."},
		"project_id":       schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Factory project UUID."},
		"name":             schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, Description: "Existing skill name (cannot create a new skill)."},
		"description":      schema.StringAttribute{Required: true, Description: "Skill description. Quoted safely in YAML frontmatter."},
		"instructions":     schema.StringAttribute{Required: true, Description: "Skill instructions; semantically compared after trimming outer whitespace."},
		"origin":           schema.StringAttribute{Computed: true, Description: "Original skill origin: code, folder or bundled."},
		"original_changed": schema.BoolAttribute{Computed: true, Description: "Whether original skill has changed since the override was saved."},
	}}
}
func (r *SkillOverrideResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Missing client", "Expected Mastra Factory client in provider configuration.")
		return
	}
	r.client = c
}
func skillIdentity(p, n string) string { return p + "/" + n }
func sameText(a, b string) bool        { return strings.TrimSpace(a) == strings.TrimSpace(b) }
func (r *SkillOverrideResource) requireClient(resp *resource.ReadResponse) bool {
	return r.client != nil
}
func (r *SkillOverrideResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m skillOverrideModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Missing Factory client.")
		return
	}
	project, name := m.ProjectID.ValueString(), m.Name.ValueString()
	remote, err := r.client.Skill(ctx, project, name)
	if err != nil {
		resp.Diagnostics.AddError("Skill lookup failed", err.Error())
		return
	}
	if remote.Edited != nil {
		resp.Diagnostics.AddError("Skill override already exists", "An override exists for this project and skill. Import it instead of overwriting unmanaged configuration.")
		return
	}
	// Persist the planned identity before the network write. If the API applies
	// the PUT but its response or confirmation read is lost, the next refresh
	// can reconcile state instead of leaving an untracked remote object.
	m.ID = types.StringValue(skillIdentity(project, name))
	m.Origin = types.StringValue(remote.Origin)
	m.OriginalChanged = types.BoolValue(false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.UpsertSkill(ctx, project, name, m.Description.ValueString(), m.Instructions.ValueString()); err != nil {
		resp.Diagnostics.AddError("Skill override creation failed", err.Error())
		return
	}
	confirmed, err := r.client.Skill(ctx, project, name)
	if err != nil || confirmed.Edited == nil || !sameText(confirmed.Description, m.Description.ValueString()) || !sameText(confirmed.Content, m.Instructions.ValueString()) {
		resp.Diagnostics.AddError("Skill override verification failed", "The Factory API did not confirm the requested content after writing; inspect the remote override before retrying.")
		return
	}
	// Provider config values are preserved exactly; the API trims instruction
	// whitespace. Read compares semantic values to prevent perpetual drift.
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *SkillOverrideResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m skillOverrideModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Missing Factory client.")
		return
	}
	project, name := m.ProjectID.ValueString(), m.Name.ValueString()
	if m.ProjectID.IsNull() || m.Name.IsNull() || project == "" || name == "" {
		// `terraform import` initially gives us only `id`.
		s := m.ID.ValueString()
		var ok bool
		project, name, ok = strings.Cut(s, "/")
		if !ok {
			resp.Diagnostics.AddError("Invalid import ID", "Use project-uuid/skill-name.")
			return
		}
	}
	remote, err := r.client.Skill(ctx, project, name)
	if err != nil {
		resp.Diagnostics.AddError("Skill refresh failed", err.Error())
		return
	}
	if remote.Edited == nil {
		// Override reverted in UI; the managed override really is absent.
		resp.State.RemoveResource(ctx)
		return
	}
	m.ID = types.StringValue(skillIdentity(project, name))
	m.ProjectID = types.StringValue(project)
	m.Name = types.StringValue(name)
	if m.Description.IsNull() || m.Description.IsUnknown() || !sameText(m.Description.ValueString(), remote.Description) {
		m.Description = types.StringValue(remote.Description)
	}
	if m.Instructions.IsNull() || m.Instructions.IsUnknown() || !sameText(m.Instructions.ValueString(), remote.Content) {
		m.Instructions = types.StringValue(remote.Content)
	}
	m.Origin = types.StringValue(remote.Origin)
	m.OriginalChanged = types.BoolValue(remote.OriginalChanged)
	if remote.OriginalChanged {
		resp.Diagnostics.AddWarning("Upstream skill changed", "The original Factory skill changed after this project override was saved. Review the effective instructions and reapply intentionally.")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *SkillOverrideResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, previous skillOverrideModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Missing Factory client.")
		return
	}
	project, name := m.ProjectID.ValueString(), m.Name.ValueString()
	remote, err := r.client.Skill(ctx, project, name)
	if err != nil {
		resp.Diagnostics.AddError("Skill pre-update read failed", err.Error())
		return
	}
	if remote.Edited == nil || !sameText(remote.Description, previous.Description.ValueString()) || !sameText(remote.Content, previous.Instructions.ValueString()) {
		resp.Diagnostics.AddError("Concurrent or missing override", "Factory configuration changed since the plan/state read. Refresh plan and review drift before writing.")
		return
	}
	if err := r.client.UpsertSkill(ctx, project, name, m.Description.ValueString(), m.Instructions.ValueString()); err != nil {
		resp.Diagnostics.AddError("Skill override update failed", err.Error())
		return
	}
	confirmed, err := r.client.Skill(ctx, project, name)
	if err != nil || confirmed.Edited == nil || !sameText(confirmed.Description, m.Description.ValueString()) || !sameText(confirmed.Content, m.Instructions.ValueString()) {
		resp.Diagnostics.AddError("Skill override verification failed", "The Factory API did not confirm the requested update; inspect the remote override before retrying.")
		return
	}
	m.ID = types.StringValue(skillIdentity(project, name))
	m.Origin = types.StringValue(remote.Origin)
	m.OriginalChanged = types.BoolValue(false)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *SkillOverrideResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m skillOverrideModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Missing Factory client.")
		return
	}
	remote, err := r.client.Skill(ctx, m.ProjectID.ValueString(), m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Skill pre-delete read failed", err.Error())
		return
	}
	if remote.Edited == nil {
		return
	}
	if !sameText(remote.Description, m.Description.ValueString()) || !sameText(remote.Content, m.Instructions.ValueString()) {
		resp.Diagnostics.AddError("Concurrent override change", "Remote override differs from state. Refresh state and approve a new plan before revert.")
		return
	}
	if err := r.client.DeleteSkillOverride(ctx, m.ProjectID.ValueString(), m.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Skill revert failed", err.Error())
		return
	}
	confirmed, err := r.client.Skill(ctx, m.ProjectID.ValueString(), m.Name.ValueString())
	if err != nil || confirmed.Edited != nil {
		resp.Diagnostics.AddError("Skill revert verification failed", "The native skill override may still exist; inspect it before retrying.")
	}
}
func (r *SkillOverrideResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	project, name, ok := strings.Cut(req.ID, "/")
	if !ok || project == "" || name == "" || strings.Contains(name, "/") {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Use project-uuid/skill-name, got a value of length %d.", len(req.ID)))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
