package provider

import (
	"context"

	"github.com/SmadjaPaul/terraform-provider-mastrafactory/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = (*SkillDataSource)(nil)
var _ datasource.DataSourceWithConfigure = (*SkillDataSource)(nil)

type SkillDataSource struct{ client *client.Client }
type skillData struct {
	ID              types.String `tfsdk:"id"`
	ProjectID       types.String `tfsdk:"project_id"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	Instructions    types.String `tfsdk:"instructions"`
	Origin          types.String `tfsdk:"origin"`
	Edited          types.Bool   `tfsdk:"edited"`
	OriginalChanged types.Bool   `tfsdk:"original_changed"`
}

func (d *SkillDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_skill"
}
func (d *SkillDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Read an effective Factory skill (project edits take precedence).", Attributes: map[string]schema.Attribute{
		"id":               schema.StringAttribute{Computed: true},
		"project_id":       schema.StringAttribute{Required: true},
		"name":             schema.StringAttribute{Required: true},
		"description":      schema.StringAttribute{Computed: true},
		"instructions":     schema.StringAttribute{Computed: true},
		"origin":           schema.StringAttribute{Computed: true},
		"edited":           schema.BoolAttribute{Computed: true},
		"original_changed": schema.BoolAttribute{Computed: true},
	}}
}
func (d *SkillDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Invalid provider configuration", "Client missing.")
		return
	}
	d.client = c
}
func (d *SkillDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m skillData
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Missing Factory client.")
		return
	}
	s, err := d.client.Skill(ctx, m.ProjectID.ValueString(), m.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read effective skill failed", err.Error())
		return
	}
	m.ID = types.StringValue(skillIdentity(m.ProjectID.ValueString(), m.Name.ValueString()))
	m.Description = types.StringValue(s.Description)
	m.Instructions = types.StringValue(s.Content)
	m.Origin = types.StringValue(s.Origin)
	m.Edited = types.BoolValue(s.Edited != nil)
	m.OriginalChanged = types.BoolValue(s.OriginalChanged)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
