package provider

import (
	"context"
	"os"
	"strings"

	"github.com/SmadjaPaul/terraform-provider-mastrafactory/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = (*FactoryProvider)(nil)

type FactoryProvider struct{ version string }
type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &FactoryProvider{version: version} }
}
func (p *FactoryProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "mastrafactory"
	resp.Version = p.version
}
func (p *FactoryProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{Description: "Declarative configuration of a self-hosted Mastra Factory 0.27 API. No run/approval APIs.", Attributes: map[string]schema.Attribute{
		"endpoint": schema.StringAttribute{Optional: true, Description: "Factory origin URL. Can also set MASTRAFACTORY_ENDPOINT. Must match runner-controlled MASTRAFACTORY_TRUSTED_ENDPOINT; HTTPS outside loopback or Kubernetes service DNS."},
	}}
}
func (p *FactoryProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var m providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.Endpoint.IsUnknown() {
		resp.Diagnostics.AddError("Unknown endpoint", "Set an explicit endpoint or MASTRAFACTORY_ENDPOINT.")
		return
	}
	endpoint := strings.TrimSpace(m.Endpoint.ValueString())
	if endpoint == "" {
		endpoint = os.Getenv("MASTRAFACTORY_ENDPOINT")
	}
	c, err := client.New(client.Config{
		Endpoint:        endpoint,
		TrustedEndpoint: os.Getenv("MASTRAFACTORY_TRUSTED_ENDPOINT"),
		AuthOrigin:      os.Getenv("MASTRAFACTORY_AUTH_ORIGIN"),
		Email:           os.Getenv("MASTRAFACTORY_EMAIL"),
		Password:        os.Getenv("MASTRAFACTORY_PASSWORD"),
		SessionToken:    os.Getenv("MASTRAFACTORY_SESSION_TOKEN"),
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid Mastra Factory provider configuration", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}
func (p *FactoryProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{func() resource.Resource { return &SkillOverrideResource{} }}
}
func (p *FactoryProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		func() datasource.DataSource { return &SkillDataSource{} },
		func() datasource.DataSource { return &BoardDataSource{} },
	}
}
