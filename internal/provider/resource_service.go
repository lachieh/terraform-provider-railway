package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	railway "github.com/railwayapp/railway-go-sdk"
)

type serviceModel struct {
	commonModel
	serviceSettings
	Source *sourceModel `tfsdk:"source"`
}

// serviceSettings are the service attributes databases share.
type serviceSettings struct {
	Build              *buildModel               `tfsdk:"build"`
	Deploy             *deployModel              `tfsdk:"deploy"`
	NumReplicas        *int64                    `tfsdk:"num_replicas"`
	Replicas           map[string]int64          `tfsdk:"replicas"`
	CustomDomains      map[string]domainModel    `tfsdk:"custom_domains"`
	ServiceDomains     map[string]domainModel    `tfsdk:"service_domains"`
	TCPProxies         []int64                   `tfsdk:"tcp_proxies"`
	Variables          map[string]string         `tfsdk:"variables"`
	PreserveVariables  []string                  `tfsdk:"preserve_variables"`
	SharedVariables    map[string]string         `tfsdk:"shared_variables"`
	VariableReferences map[string]referenceModel `tfsdk:"variable_references"`
	SecretVariables    types.Map                 `tfsdk:"secret_variables"`
	SecretVersion      types.String              `tfsdk:"secret_variables_version"`
	SecretNames        types.Set                 `tfsdk:"secret_variable_names"`
	VolumeMounts       map[string]string         `tfsdk:"volume_mounts"`
	ConfigFile         *string                   `tfsdk:"config_file"`
	Tracing            *tracingModel             `tfsdk:"tracing"`
	CDN                *cdnModel                 `tfsdk:"cdn"`
	Group              *string                   `tfsdk:"group"`
	ReplicaStackers    map[string]string         `tfsdk:"replica_stackers"`
	ParentServiceID    *string                   `tfsdk:"parent_service_id"`
	ClusterRole        *string                   `tfsdk:"cluster_role"`
	ReplicaConfig      *replicaConfigModel       `tfsdk:"replica_config"`
	ClusterDisplay     *clusterDisplayModel      `tfsdk:"cluster_display"`

	// secretValues are the secret_variables values this apply sends; the
	// other secret_variable_names are preserved.
	secretValues map[string]string
}

func (s *serviceSettings) settings() *serviceSettings { return s }

type replicaConfigModel struct {
	MinReplicas *int64 `tfsdk:"min_replicas"`
	MaxReplicas *int64 `tfsdk:"max_replicas"`
	Step        *int64 `tfsdk:"step"`
	Scalable    *bool  `tfsdk:"scalable"`
}

type clusterDisplayModel struct {
	Badge        *string `tfsdk:"badge"`
	BadgeVariant *string `tfsdk:"badge_variant"`
}

type sourceModel struct {
	GitHub   *githubSourceModel   `tfsdk:"github"`
	Image    *imageSourceModel    `tfsdk:"image"`
	Template *templateSourceModel `tfsdk:"template"`
	Empty    *emptySourceModel    `tfsdk:"empty"`
}

type emptySourceModel struct {
	RootDirectory *string `tfsdk:"root_directory"`
}

type githubSourceModel struct {
	Repo          string  `tfsdk:"repo"`
	Branch        *string `tfsdk:"branch"`
	CheckSuites   *bool   `tfsdk:"check_suites"`
	RootDirectory *string `tfsdk:"root_directory"`
	CommitSHA     *string `tfsdk:"commit_sha"`
}

type imageSourceModel struct {
	Image       string            `tfsdk:"image"`
	AutoUpdates *autoUpdatesModel `tfsdk:"auto_updates"`
}

type autoUpdatesModel struct {
	Type     string              `tfsdk:"type"`
	TagMode  *string             `tfsdk:"tag_mode"`
	Schedule []updateWindowModel `tfsdk:"schedule"`
}

type updateWindowModel struct {
	Day       int64 `tfsdk:"day"`
	StartHour int64 `tfsdk:"start_hour"`
	EndHour   int64 `tfsdk:"end_hour"`
}

type templateSourceModel struct {
	Template string `tfsdk:"template"`
}

type buildModel struct {
	Builder            *string  `tfsdk:"builder"`
	BuildCommand       *string  `tfsdk:"build_command"`
	BuildEnvironment   *string  `tfsdk:"build_environment"`
	DockerfilePath     *string  `tfsdk:"dockerfile_path"`
	WatchPatterns      []string `tfsdk:"watch_patterns"`
	NixpacksConfigPath *string  `tfsdk:"nixpacks_config_path"`
	NixpacksVersion    *string  `tfsdk:"nixpacks_version"`
	RailpackVersion    *string  `tfsdk:"railpack_version"`
}

type deployModel struct {
	StartCommand            *string                   `tfsdk:"start_command"`
	PreDeployCommand        []string                  `tfsdk:"pre_deploy_command"`
	HealthcheckPath         *string                   `tfsdk:"healthcheck_path"`
	HealthcheckTimeout      *int64                    `tfsdk:"healthcheck_timeout"`
	RestartPolicyType       *string                   `tfsdk:"restart_policy_type"`
	RestartPolicyMaxRetries *int64                    `tfsdk:"restart_policy_max_retries"`
	DrainingSeconds         *int64                    `tfsdk:"draining_seconds"`
	OverlapSeconds          *int64                    `tfsdk:"overlap_seconds"`
	SleepApplication        *bool                     `tfsdk:"sleep_application"`
	CronSchedule            *string                   `tfsdk:"cron_schedule"`
	IPv6EgressEnabled       *bool                     `tfsdk:"ipv6_egress_enabled"`
	LimitOverride           *limitOverrideModel       `tfsdk:"limit_override"`
	RegistryCredentials     *registryCredentialsModel `tfsdk:"registry_credentials"`
}

type limitOverrideModel struct {
	CPU         *float64 `tfsdk:"cpu"`
	MemoryBytes *int64   `tfsdk:"memory_bytes"`
	DiskBytes   *int64   `tfsdk:"disk_bytes"`
}

type registryCredentialsModel struct {
	Username string `tfsdk:"username"`
	Password string `tfsdk:"password"`
}

type domainModel struct {
	Port *int64 `tfsdk:"port"`
}

type referenceModel struct {
	Resource string `tfsdk:"resource"`
	Output   string `tfsdk:"output"`
}

type tracingModel struct {
	Enabled             *bool `tfsdk:"enabled"`
	AutoInstrumentation *bool `tfsdk:"auto_instrumentation"`
}

type cdnModel struct {
	Enabled              bool    `tfsdk:"enabled"`
	DefaultTTLSeconds    *int64  `tfsdk:"default_ttl_seconds"`
	HTMLCaching          *string `tfsdk:"html_caching"`
	StaleWhileRevalidate *bool   `tfsdk:"stale_while_revalidate"`
	PurgeOnDeploy        *string `tfsdk:"purge_on_deploy"`
}

func NewServiceResource() resource.Resource {
	attributes := serviceSettingsAttributes()
	attributes["source"] = schema.SingleNestedAttribute{
		MarkdownDescription: "Where the service's code comes from. Set exactly one of `github`, `image`, `template` or `empty`; omit for an empty service.",
		Optional:            true,
		Attributes: map[string]schema.Attribute{
			"github": schema.SingleNestedAttribute{
				MarkdownDescription: "A GitHub repository, as `railway.Github`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"repo":           schema.StringAttribute{MarkdownDescription: "`owner/name` of the repository.", Required: true},
					"branch":         schema.StringAttribute{MarkdownDescription: "Branch to deploy. `railway.Github` defaults it to `main`, so a branch owned by the environment, as in pull request environments, cannot be expressed.", Optional: true},
					"check_suites":   schema.BoolAttribute{MarkdownDescription: "Wait for GitHub check suites before deploying.", Optional: true},
					"root_directory": schema.StringAttribute{MarkdownDescription: "Directory of the repository to build from.", Optional: true},
					"commit_sha":     schema.StringAttribute{MarkdownDescription: "Commit to deploy instead of the branch head.", Optional: true},
				},
			},
			"image": schema.SingleNestedAttribute{
				MarkdownDescription: "A Docker image, as `railway.Image`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"image": schema.StringAttribute{MarkdownDescription: "Image reference, such as `ghcr.io/acme/api:1` or a `@sha256:` digest.", Required: true},
					"auto_updates": schema.SingleNestedAttribute{
						MarkdownDescription: "Follow new tags of a Docker Hub or GHCR image, as `railway.AutoUpdates`.",
						Optional:            true,
						Attributes: map[string]schema.Attribute{
							"type": schema.StringAttribute{
								MarkdownDescription: "`disabled`, `patch` or `minor`.",
								Required:            true,
								Validators:          []validator.String{stringvalidator.OneOf(string(railway.AutoUpdatesDisabled), string(railway.AutoUpdatesPatch), string(railway.AutoUpdatesMinor))},
							},
							"tag_mode": schema.StringAttribute{
								MarkdownDescription: "How new tags are matched: `semver` or `sha`.",
								Optional:            true,
								Validators:          []validator.String{stringvalidator.OneOf("semver", "sha")},
							},
							"schedule": schema.ListNestedAttribute{
								MarkdownDescription: "Weekly UTC maintenance windows updates may run in.",
								Optional:            true,
								NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
									"day":        schema.Int64Attribute{MarkdownDescription: "0 (Sunday) through 6 (Saturday).", Required: true},
									"start_hour": schema.Int64Attribute{MarkdownDescription: "Opening hour, 0 through 23, inclusive.", Required: true},
									"end_hour":   schema.Int64Attribute{MarkdownDescription: "Closing hour, 1 through 24, exclusive.", Required: true},
								}},
							},
						},
					},
				},
			},
			"empty": schema.SingleNestedAttribute{
				MarkdownDescription: "No source, as `railway.Empty`: deploys come from `railway up` or the dashboard.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"root_directory": schema.StringAttribute{MarkdownDescription: "Directory uploads build from.", Optional: true},
				},
			},
			"template": schema.SingleNestedAttribute{
				MarkdownDescription: "A Railway template service, as `railway.Template`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"template": schema.StringAttribute{MarkdownDescription: "Template name.", Required: true},
				},
			},
		},
		Validators: []validator.Object{sourceExactlyOne{}},
	}
	return &nodeResource[serviceModel]{kind: nodeKind[serviceModel]{
		graphType: "service",
		description: "A Railway service in one environment, built with `railway.ServiceNamed`. " +
			"The configuration is the service's whole definition, as a `railway.go` file is for `railway config apply`: " +
			"a source, build or deploy setting left unset is reset to Railway's default, and a variable missing from " +
			"`variables`, `preserve_variables`, `shared_variables` and `variable_references` is deleted. " +
			"Domains and TCP proxies the configuration does not list stay attached.",
		attributes: attributes,
		common:     func(m *serviceModel) *commonModel { return &m.commonModel },
		build: func(m *serviceModel) func() any {
			return func() any {
				config := m.config()
				if source := m.Source.sdk(); source != nil {
					config["source"] = source
				}
				return railway.ServiceNamed(m.Name.ValueString(), config)
			}
		},
	}}
}

func (s *sourceModel) sdk() map[string]any {
	switch {
	case s == nil:
		return nil
	case s.GitHub != nil:
		options := map[string]any{}
		putString(options, "branch", s.GitHub.Branch)
		putBool(options, "checkSuites", s.GitHub.CheckSuites)
		putString(options, "rootDirectory", s.GitHub.RootDirectory)
		putString(options, "commitSha", s.GitHub.CommitSHA)
		return railway.Github(s.GitHub.Repo, options)
	case s.Image != nil:
		options := map[string]any{}
		if updates := s.Image.AutoUpdates; updates != nil {
			value := railway.AutoUpdates{Type: railway.AutoUpdatesType(updates.Type)}
			if updates.TagMode != nil {
				value.TagMode = *updates.TagMode
			}
			for _, window := range updates.Schedule {
				value.Schedule = append(value.Schedule, railway.AutoUpdateWindow{
					Day: int(window.Day), StartHour: int(window.StartHour), EndHour: int(window.EndHour),
				})
			}
			options["autoUpdates"] = value
		}
		return railway.Image(s.Image.Image, options)
	case s.Template != nil:
		return railway.Template(s.Template.Template)
	case s.Empty != nil:
		options := map[string]any{}
		putString(options, "rootDirectory", s.Empty.RootDirectory)
		return railway.Empty(options)
	}
	return nil
}

// config is the railway.ServiceConfig for the shared settings.
func (s *serviceSettings) config() railway.ServiceConfig {
	config := railway.ServiceConfig{}
	if build := s.Build.sdk(); len(build) > 0 {
		config["build"] = build
	}
	if deploy := s.Deploy.sdk(); len(deploy) > 0 {
		config["deploy"] = deploy
	}
	switch {
	case s.Replicas != nil:
		regions := map[string]any{}
		for region, count := range s.Replicas {
			if stacker, ok := s.ReplicaStackers[region]; ok {
				regions[region] = map[string]any{"count": int(count), "stacker": stacker}
				continue
			}
			regions[region] = int(count)
		}
		config["replicas"] = regions
	case s.NumReplicas != nil:
		config["replicas"] = int(*s.NumReplicas)
	}
	if s.CustomDomains != nil {
		domains := make([]any, 0, len(s.CustomDomains))
		for domain, settings := range s.CustomDomains {
			item := map[string]any{"domain": domain}
			if settings.Port != nil {
				item["port"] = int(*settings.Port)
			}
			domains = append(domains, item)
		}
		config["domains"] = domains
	}
	if s.ServiceDomains != nil {
		domains := map[string]any{}
		for domain, settings := range s.ServiceDomains {
			item := map[string]any{}
			putInt(item, "port", settings.Port)
			domains[domain] = item
		}
		config["networking"] = map[string]any{"serviceDomains": domains}
	}
	if s.TCPProxies != nil {
		ports := make([]any, 0, len(s.TCPProxies))
		for _, port := range s.TCPProxies {
			ports = append(ports, int(port))
		}
		config["tcp"] = ports
	}
	env := variableEnv(s.Variables, s.PreserveVariables, s.SharedVariables, s.VariableReferences)
	for _, name := range setStrings(s.SecretNames) {
		if _, ok := env[name]; ok {
			panic(fmt.Sprintf("Variable %s is set in secret_variables and in another variables attribute.", name))
		}
		if value, ok := s.secretValues[name]; ok {
			env[name] = value
		} else {
			env[name] = railway.Preserve()
		}
	}
	if len(env) > 0 {
		config["env"] = env
	}
	if s.VolumeMounts != nil {
		mounts := map[string]any{}
		for mountPath, volume := range s.VolumeMounts {
			mounts[mountPath] = railway.Volume(volume)
		}
		config["volumeMounts"] = mounts
	}
	if s.ConfigFile != nil {
		config["configFile"] = *s.ConfigFile
	}
	if s.Group != nil {
		config["groupId"] = *s.Group
	}
	if s.ParentServiceID != nil {
		config["parentServiceId"] = *s.ParentServiceID
	}
	if s.ClusterRole != nil {
		config["clusterRole"] = *s.ClusterRole
	}
	if c := s.ReplicaConfig; c != nil {
		replicaConfig := map[string]any{}
		putInt(replicaConfig, "minReplicas", c.MinReplicas)
		putInt(replicaConfig, "maxReplicas", c.MaxReplicas)
		putInt(replicaConfig, "step", c.Step)
		putBool(replicaConfig, "scalable", c.Scalable)
		config["replicaConfig"] = replicaConfig
	}
	if d := s.ClusterDisplay; d != nil {
		display := map[string]any{}
		putString(display, "badge", d.Badge)
		putString(display, "badgeVariant", d.BadgeVariant)
		config["clusterDisplay"] = display
	}
	if s.Tracing != nil {
		tracing := map[string]any{}
		putBool(tracing, "enabled", s.Tracing.Enabled)
		putBool(tracing, "autoInstrumentation", s.Tracing.AutoInstrumentation)
		config["tracing"] = tracing
	}
	if s.CDN != nil {
		cdn := railway.ServiceCdn{Enabled: s.CDN.Enabled, StaleWhileRevalidate: s.CDN.StaleWhileRevalidate}
		if s.CDN.DefaultTTLSeconds != nil {
			cdn.DefaultTtlSeconds = int(*s.CDN.DefaultTTLSeconds)
		}
		if s.CDN.HTMLCaching != nil {
			cdn.HtmlCaching = railway.ServiceCdnHtmlCaching(*s.CDN.HTMLCaching)
		}
		if s.CDN.PurgeOnDeploy != nil {
			cdn.PurgeOnDeploy = railway.ServiceCdnPurgeOnDeploy(*s.CDN.PurgeOnDeploy)
		}
		config["cdn"] = cdn
	}
	return config
}

func (b *buildModel) sdk() map[string]any {
	if b == nil {
		return nil
	}
	out := map[string]any{}
	putString(out, "builder", b.Builder)
	putString(out, "buildCommand", b.BuildCommand)
	putString(out, "buildEnvironment", b.BuildEnvironment)
	putString(out, "dockerfilePath", b.DockerfilePath)
	if b.WatchPatterns != nil {
		out["watchPatterns"] = stringsToAny(b.WatchPatterns)
	}
	putString(out, "nixpacksConfigPath", b.NixpacksConfigPath)
	putString(out, "nixpacksVersion", b.NixpacksVersion)
	putString(out, "railpackVersion", b.RailpackVersion)
	return out
}

func (d *deployModel) sdk() map[string]any {
	if d == nil {
		return nil
	}
	out := map[string]any{}
	putString(out, "startCommand", d.StartCommand)
	if d.PreDeployCommand != nil {
		out["preDeployCommand"] = stringsToAny(d.PreDeployCommand)
	}
	putString(out, "healthcheckPath", d.HealthcheckPath)
	putInt(out, "healthcheckTimeout", d.HealthcheckTimeout)
	putString(out, "restartPolicyType", d.RestartPolicyType)
	putInt(out, "restartPolicyMaxRetries", d.RestartPolicyMaxRetries)
	putInt(out, "drainingSeconds", d.DrainingSeconds)
	putInt(out, "overlapSeconds", d.OverlapSeconds)
	putBool(out, "sleepApplication", d.SleepApplication)
	putString(out, "cronSchedule", d.CronSchedule)
	putBool(out, "ipv6EgressEnabled", d.IPv6EgressEnabled)
	if limits := d.LimitOverride; limits != nil {
		containers := map[string]any{}
		putFloat(containers, "cpu", limits.CPU)
		putInt(containers, "memoryBytes", limits.MemoryBytes)
		putInt(containers, "diskBytes", limits.DiskBytes)
		out["limitOverride"] = map[string]any{"containers": containers}
	}
	if credentials := d.RegistryCredentials; credentials != nil {
		out["registryCredentials"] = map[string]any{"username": credentials.Username, "password": credentials.Password}
	}
	return out
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

func serviceSettingsAttributes() map[string]schema.Attribute {
	optionalString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: description, Optional: true}
	}
	optionalInt := func(description string) schema.Int64Attribute {
		return schema.Int64Attribute{MarkdownDescription: description, Optional: true}
	}
	optionalBool := func(description string) schema.BoolAttribute {
		return schema.BoolAttribute{MarkdownDescription: description, Optional: true}
	}
	domain := schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
		"port": optionalInt("Target port. `0` lets Railway pick the port the service listens on."),
	}}
	return map[string]schema.Attribute{
		"build": schema.SingleNestedAttribute{
			MarkdownDescription: "Build settings.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"builder": schema.StringAttribute{
					MarkdownDescription: "`RAILPACK`, `DOCKERFILE`, `NIXPACKS`, `HEROKU` or `PAKETO`.",
					Optional:            true,
					Validators:          []validator.String{stringvalidator.OneOf("RAILPACK", "DOCKERFILE", "NIXPACKS", "HEROKU", "PAKETO")},
				},
				"build_command": optionalString("Command that builds the service."),
				"build_environment": schema.StringAttribute{
					MarkdownDescription: "Builder generation: `V2` or `V3`.",
					Optional:            true,
					Validators:          []validator.String{stringvalidator.OneOf("V2", "V3")},
				},
				"dockerfile_path":      optionalString("Path of the Dockerfile, relative to the root directory."),
				"watch_patterns":       schema.ListAttribute{MarkdownDescription: "Paths whose changes trigger a deploy.", Optional: true, ElementType: types.StringType},
				"nixpacks_config_path": optionalString("Path of the Nixpacks configuration file."),
				"nixpacks_version":     optionalString("Nixpacks version."),
				"railpack_version":     optionalString("Railpack version."),
			},
		},
		"deploy": schema.SingleNestedAttribute{
			MarkdownDescription: "Deploy settings.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"start_command":       optionalString("Command that starts the service."),
				"pre_deploy_command":  schema.ListAttribute{MarkdownDescription: "Commands run before each deploy.", Optional: true, ElementType: types.StringType},
				"healthcheck_path":    optionalString("Path Railway polls before routing traffic to a deploy."),
				"healthcheck_timeout": optionalInt("Seconds a deploy has to pass its health check."),
				"restart_policy_type": schema.StringAttribute{
					MarkdownDescription: "`ON_FAILURE`, `ALWAYS` or `NEVER`.",
					Optional:            true,
					Validators:          []validator.String{stringvalidator.OneOf("ON_FAILURE", "ALWAYS", "NEVER")},
				},
				"restart_policy_max_retries": optionalInt("Restarts allowed under `ON_FAILURE`."),
				"draining_seconds":           optionalInt("Seconds an old deploy keeps running after it stops receiving traffic."),
				"overlap_seconds":            optionalInt("Seconds the old and new deploys both receive traffic."),
				"sleep_application":          optionalBool("Sleep the service when it receives no traffic."),
				"cron_schedule":              optionalString("Cron schedule for a service that runs on a schedule."),
				"ipv6_egress_enabled":        optionalBool("Allow outbound IPv6."),
				"limit_override": schema.SingleNestedAttribute{
					MarkdownDescription: "Per-container resource limits.",
					Optional:            true,
					Attributes: map[string]schema.Attribute{
						"cpu":          schema.Float64Attribute{MarkdownDescription: "vCPUs.", Optional: true},
						"memory_bytes": optionalInt("Memory in bytes."),
						"disk_bytes":   optionalInt("Ephemeral disk in bytes."),
					},
				},
				"registry_credentials": schema.SingleNestedAttribute{
					MarkdownDescription: "Credentials for a private image registry.",
					Optional:            true,
					Sensitive:           true,
					Attributes: map[string]schema.Attribute{
						"username": schema.StringAttribute{Required: true, MarkdownDescription: "Registry user name."},
						"password": schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "Registry password or token."},
					},
				},
			},
		},
		"num_replicas": schema.Int64Attribute{
			MarkdownDescription: "Replica count in the service's default region. Conflicts with `replicas`.",
			Optional:            true,
			Validators:          []validator.Int64{int64validator.ConflictsWith(path.MatchRoot("replicas"))},
		},
		"replicas": schema.MapAttribute{
			MarkdownDescription: "Replica count per region, such as `{ \"us-west2\" = 1 }`.",
			Optional:            true,
			ElementType:         types.Int64Type,
		},
		"custom_domains": schema.MapNestedAttribute{
			MarkdownDescription: "Custom domains keyed by host name. The SDK defaults `port` to `8080`.",
			Optional:            true,
			NestedObject:        domain,
		},
		"service_domains": schema.MapNestedAttribute{
			MarkdownDescription: "Railway-generated `*.up.railway.app` domains keyed by host name.",
			Optional:            true,
			NestedObject:        domain,
		},
		"tcp_proxies": schema.ListAttribute{
			MarkdownDescription: "Application ports exposed through Railway's TCP proxy.",
			Optional:            true,
			ElementType:         types.Int64Type,
		},
		"variables": schema.MapAttribute{
			MarkdownDescription: "Variables with literal values. Railway expands `${{service.VAR}}` templates in values.",
			Optional:            true,
			ElementType:         types.StringType,
		},
		"preserve_variables": schema.ListAttribute{
			MarkdownDescription: "Variables Railway keeps with their current values, as `railway.Preserve()`. Use for secrets set outside Terraform.",
			Optional:            true,
			ElementType:         types.StringType,
		},
		"shared_variables": schema.MapAttribute{
			MarkdownDescription: "Variables that reference an environment shared variable, keyed by variable and valued by shared variable name, as `railway.Shared`.",
			Optional:            true,
			ElementType:         types.StringType,
		},
		"variable_references": schema.MapNestedAttribute{
			MarkdownDescription: "Variables that reference another resource's output, as `railway.Ref`.",
			Optional:            true,
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"resource": schema.StringAttribute{MarkdownDescription: "Graph address of the resource, such as `railway_database.postgres.address` (`database.postgres`).", Required: true},
				"output":   schema.StringAttribute{MarkdownDescription: "Variable of that resource, such as `DATABASE_URL`.", Required: true},
			}},
		},
		"secret_variables": schema.MapAttribute{
			MarkdownDescription: "Variables whose values Terraform sends to Railway but never stores in plan or state. Requires Terraform 1.11 or OpenTofu 1.11. " +
				"Values are sent when the resource is created, when `secret_variables_version` changes, and for names added since the last apply; " +
				"otherwise Railway keeps the current values, so change `secret_variables_version` to send a changed value.",
			Optional:    true,
			Sensitive:   true,
			WriteOnly:   true,
			ElementType: types.StringType,
		},
		"secret_variables_version": schema.StringAttribute{
			MarkdownDescription: "Any value; changing it sends every `secret_variables` value again.",
			Optional:            true,
		},
		"secret_variable_names": schema.SetAttribute{
			MarkdownDescription: "Names in `secret_variables`, which Terraform stores so a refresh can tell them from variables to delete.",
			Computed:            true,
			ElementType:         types.StringType,
		},
		"volume_mounts": schema.MapAttribute{
			MarkdownDescription: "Volumes keyed by mount path and valued by `railway_volume` name. Reference the volume (`railway_volume.data.name`) so Terraform orders the two. Backup schedules cannot be set through the SDK's volume mounts.",
			Optional:            true,
			ElementType:         types.StringType,
		},
		"replica_stackers": schema.MapAttribute{
			MarkdownDescription: "Stacker assignment per region, for regions listed in `replicas`.",
			Optional:            true,
			ElementType:         types.StringType,
		},
		"parent_service_id": optionalString("ID of the service this one is a cluster member of."),
		"cluster_role": schema.StringAttribute{
			MarkdownDescription: "Role in a cluster: `root`, `replica`, `internal` or `edge`.",
			Optional:            true,
			Validators:          []validator.String{stringvalidator.OneOf("root", "replica", "internal", "edge")},
		},
		"replica_config": schema.SingleNestedAttribute{
			MarkdownDescription: "Scaling bounds for a cluster.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"min_replicas": optionalInt("Fewest replicas."),
				"max_replicas": optionalInt("Most replicas."),
				"step":         optionalInt("Replicas added or removed per scaling step."),
				"scalable":     optionalBool("Whether the cluster scales."),
			},
		},
		"cluster_display": schema.SingleNestedAttribute{
			MarkdownDescription: "How the canvas labels a cluster member.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"badge": optionalString("Badge text."),
				"badge_variant": schema.StringAttribute{
					MarkdownDescription: "`primary`, `secondary` or `muted`.",
					Optional:            true,
					Validators:          []validator.String{stringvalidator.OneOf("primary", "secondary", "muted")},
				},
			},
		},
		"config_file": optionalString("Path of a Railway config file in the repository."),
		"group":       optionalString("Name of the canvas group the service belongs to."),
		"tracing": schema.SingleNestedAttribute{
			MarkdownDescription: "OpenTelemetry tracing switches.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"enabled":              optionalBool("Collect traces."),
				"auto_instrumentation": optionalBool("Instrument the runtime automatically."),
			},
		},
		"cdn": schema.SingleNestedAttribute{
			MarkdownDescription: "Edge caching for the service's public domains, as `railway.ServiceCdn`. Applies without a deploy.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"enabled":             schema.BoolAttribute{MarkdownDescription: "Cache responses at Railway's edge.", Required: true},
				"default_ttl_seconds": optionalInt("Seconds to keep a response without its own `Cache-Control`. Railway's default is 7200."),
				"html_caching": schema.StringAttribute{
					MarkdownDescription: "`auto`, `force` or `never`.",
					Optional:            true,
					Validators:          []validator.String{stringvalidator.OneOf(string(railway.ServiceCdnHtmlCachingAuto), string(railway.ServiceCdnHtmlCachingForce), string(railway.ServiceCdnHtmlCachingNever))},
				},
				"stale_while_revalidate": optionalBool("Honour `stale-while-revalidate`. Railway's default is `true`."),
				"purge_on_deploy": schema.StringAttribute{
					MarkdownDescription: "`off`, `html` or `all`.",
					Optional:            true,
					Validators:          []validator.String{stringvalidator.OneOf(string(railway.ServiceCdnPurgeOnDeployOff), string(railway.ServiceCdnPurgeOnDeployHtml), string(railway.ServiceCdnPurgeOnDeployAll))},
				},
			},
		},
	}
}

// sourceExactlyOne requires exactly one source kind when source is set.
type sourceExactlyOne struct{}

func (sourceExactlyOne) Description(context.Context) string {
	return "Set exactly one of github, image, template or empty."
}

func (v sourceExactlyOne) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (sourceExactlyOne) ValidateObject(_ context.Context, req validator.ObjectRequest, resp *validator.ObjectResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	set := 0
	for _, name := range []string{"github", "image", "template", "empty"} {
		value, ok := req.ConfigValue.Attributes()[name]
		if !ok || value.IsNull() {
			continue
		}
		if value.IsUnknown() {
			return
		}
		set++
	}
	if set != 1 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid source", "Set exactly one of github, image, template or empty.")
	}
}
