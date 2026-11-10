package provider

import (
	"context"
	"encoding/json"

	"github.com/SmadjaPaul/terraform-provider-mastrafactory/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = (*BoardDataSource)(nil)
var _ datasource.DataSourceWithConfigure = (*BoardDataSource)(nil)

type BoardDataSource struct{ client *client.Client }
type boardData struct {
	ID           types.String `tfsdk:"id"`
	ProjectID    types.String `tfsdk:"project_id"`
	Key          types.String `tfsdk:"key"`
	Name         types.String `tfsdk:"name"`
	Source       types.String `tfsdk:"source"`
	Editable     types.Bool   `tfsdk:"editable"`
	Revision     types.Int64  `tfsdk:"revision"`
	PhasesJSON   types.String `tfsdk:"phases_json"`
	ProblemsJSON types.String `tfsdk:"problems_json"`
}

func (d *BoardDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_board"
}
func (d *BoardDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Read only: Board Work or a user Board. Mastra 0.27 code boards cannot be edited via API.", Attributes: map[string]schema.Attribute{
		"id":            schema.StringAttribute{Computed: true},
		"project_id":    schema.StringAttribute{Required: true},
		"key":           schema.StringAttribute{Required: true},
		"name":          schema.StringAttribute{Computed: true},
		"source":        schema.StringAttribute{Computed: true},
		"editable":      schema.BoolAttribute{Computed: true},
		"revision":      schema.Int64Attribute{Computed: true},
		"phases_json":   schema.StringAttribute{Computed: true},
		"problems_json": schema.StringAttribute{Computed: true},
	}}
}
func (d *BoardDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (d *BoardDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m boardData
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.client == nil {
		resp.Diagnostics.AddError("Provider not configured", "Missing Factory client.")
		return
	}
	b, err := d.client.Board(ctx, m.ProjectID.ValueString(), m.Key.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read Factory board failed", err.Error())
		return
	}
	phases := json.RawMessage(b.Phases)
	if len(phases) == 0 {
		phases = json.RawMessage("[]")
	}
	var compact json.RawMessage
	if err := json.Unmarshal(phases, &compact); err != nil {
		resp.Diagnostics.AddError("Invalid Board phases", "Factory returned malformed JSON.")
		return
	}
	problems, err := json.Marshal(b.Problems)
	if err != nil {
		resp.Diagnostics.AddError("Board problems encoding failed", "Could not encode problems.")
		return
	}
	if string(problems) == "null" {
		problems = []byte("[]")
	}
	m.ID = types.StringValue(skillIdentity(m.ProjectID.ValueString(), m.Key.ValueString()))
	m.Name = types.StringValue(b.Name)
	m.Source = types.StringValue(b.Source)
	m.Editable = types.BoolValue(b.Editable)
	m.Revision = types.Int64Null()
	if b.Revision != nil {
		m.Revision = types.Int64Value(*b.Revision)
	}
	m.PhasesJSON = types.StringValue(string(compact))
	m.ProblemsJSON = types.StringValue(string(problems))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
