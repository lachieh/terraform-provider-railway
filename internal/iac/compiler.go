// Ported from railwayapp/cli src/iac/compiler.rs (MIT, Copyright (c) Railway Corp.)

package iac

import (
	"slices"
	"strings"
)

// ProviderName is the tool named in unknown-field diagnostics.
const ProviderName = "terraform-provider-railway-iac-sdk"

// IACProjectFields are the top-level project keys ProjectDefinitionToGraph
// reads, plus `variables`.
var IACProjectFields = []string{
	"name",
	"resources",
	"services",
	"Resources",
	"environments",
	"variables",
}

// IACResourceFields are the top-level resource keys the compiler reads, plus
// `environments`. `kind` and `start` are read while normalizing; `output` is
// written onto database nodes and emitted by the SDKs.
var IACResourceFields = []string{
	"address",
	"type",
	"name",
	"kind",
	"start",
	"source",
	"build",
	"deploy",
	"networking",
	"variables",
	"volumeAttachments",
	"volumeMounts",
	"config",
	"configFile",
	"parentServiceId",
	"groupId",
	"clusterRole",
	"replicaConfig",
	"clusterDisplay",
	"tracing",
	"cdn",
	"engine",
	"image",
	"env",
	"output",
	"defaultMountPath",
	"color",
	"icon",
	"isCollapsed",
	"environments",
}

type CompileOptions struct {
	ServiceIDsByName       map[string]any
	ExistingServiceIDs     []string
	VolumeIDsByServiceName map[string]any
	VolumeIDsByName        map[string]any
	BucketIDsByName        map[string]any
}

// ConfigToGraphOptions names the IDs in a live EnvironmentConfig. An empty
// ProjectName means "imported-project".
type ConfigToGraphOptions struct {
	ProjectName              string
	ServiceNamesByID         map[string]any
	VolumeNamesByID          map[string]any
	VolumeGroupIDsByID       map[string]any
	BucketNamesByID          map[string]any
	BucketGroupIDsByID       map[string]any
	CustomDomainsByServiceID map[string]any
	// TemplateServiceIDsByID marks Railway-managed databases: they are
	// deployed from the official database templates, so a service carries a
	// templateServiceId iff it came from a template. This is the only
	// provenance the API exposes; without it a `redis:7` image is just a
	// service.
	TemplateServiceIDsByID map[string]any
}

// ProjectDefinitionToGraph compiles an authored project definition into a
// graph. Resource entries that are not objects are dropped.
func ProjectDefinitionToGraph(definition map[string]any) Graph {
	raw, ok := definition["resources"]
	if !ok {
		raw, ok = definition["services"]
	}
	var resources []map[string]any
	if ok {
		for _, item := range flattenResources(raw) {
			if resource, isMap := item.(map[string]any); isMap {
				resources = append(resources, CloneMap(resource))
			}
		}
	}
	for _, resource := range resources {
		normalizeResourceEnv(resource)
	}
	seen := map[string]bool{}
	for _, resource := range resources {
		seen[ResourceAddr(resource)] = true
	}

	declared := len(resources)
	for index := 0; index < declared; index++ {
		resource := resources[index]
		switch ResourceType(resource) {
		case "service", "database":
		default:
			continue
		}
		attachments, ok := resource["volumeAttachments"].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range SortedKeys(attachments) {
			attachment := attachments[key]
			volume, ok := FieldStrOK(attachment, "volume")
			if !ok || seen[volume] {
				continue
			}
			_, volumeName, _ := strings.Cut(volume, ".")
			node := map[string]any{
				"address": volume,
				"type":    "volume",
				"name":    volumeName,
			}
			if config, ok := get(attachment, "volumeConfig"); ok {
				node["config"] = Clone(config)
			}
			seen[volume] = true
			resources = append(resources, node)
		}
	}

	edges := []Edge{}
	for _, resource := range resources {
		switch ResourceType(resource) {
		case "service", "database":
		default:
			continue
		}
		from := ResourceAddr(resource)
		if attachments, ok := resource["volumeAttachments"].(map[string]any); ok {
			for _, key := range SortedKeys(attachments) {
				attachment := attachments[key]
				volume, volumeOK := FieldStrOK(attachment, "volume")
				mount, mountOK := FieldStrOK(attachment, "mountPath")
				if volumeOK && mountOK {
					edges = append(edges, Edge{From: from, To: volume, Kind: "mount", Key: &mount})
				}
			}
		}
		if variables, ok := resource["variables"].(map[string]any); ok {
			for _, key := range SortedKeys(variables) {
				value := variables[key]
				if FieldStr(value, "type") != "reference" {
					continue
				}
				if target, ok := FieldStrOK(value, "resource"); ok {
					edgeKey := key
					edges = append(edges, Edge{From: from, To: target, Kind: "variable", Key: &edgeKey})
				}
			}
		}
	}

	for _, resource := range resources {
		dropAttachmentVolumeConfig(resource)
	}
	if resources == nil {
		resources = []map[string]any{}
	}

	name, ok := FieldStrOK(definition, "name")
	if !ok {
		name = "imported-project"
	}
	environments := []EnvironmentNode{}
	if names, ok := definition["environments"].([]any); ok {
		for _, item := range names {
			if text, ok := item.(string); ok {
				environments = append(environments, EnvironmentNode{Name: text})
			}
		}
	}
	return Graph{
		Version:      GraphVersion,
		Project:      ProjectNode{Name: name},
		Variables:    variablePolicy(definition),
		Environments: environments,
		Resources:    resources,
		Edges:        edges,
	}
}

// normalizeResourceEnv renames `env` to `variables`: authoring files say
// `env`; the graph and the diff speak `variables`. A missing key stays missing
// so an omitted env is not an empty map.
func normalizeResourceEnv(resource map[string]any) {
	env, ok := resource["env"]
	if !ok {
		return
	}
	delete(resource, "env")
	if _, ok := resource["variables"]; ok {
		return
	}
	resource["variables"] = coerceEnvMap(env)
}

func coerceEnvMap(value any) any {
	env, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := map[string]any{}
	for key, child := range env {
		if text, ok := child.(string); ok {
			out[key] = map[string]any{"type": "literal", "value": text}
			continue
		}
		out[key] = child
	}
	return out
}

func variablePolicy(definition map[string]any) VariablePolicy {
	raw, ok := definition["variables"]
	if !ok || raw == nil {
		return DefaultVariablePolicy()
	}
	invalid := func(managed bool, message string) VariablePolicy {
		return VariablePolicy{Managed: managed, Ignore: []string{}, Default: false, Error: &message}
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return invalid(true, "project.variables must be an object with managed and ignore")
	}
	managed := true
	switch value := object["managed"].(type) {
	case nil:
	case bool:
		managed = value
	default:
		return invalid(true, "project.variables.managed must be a boolean")
	}
	ignore := []string{}
	switch value := object["ignore"].(type) {
	case nil:
	case []any:
		for _, item := range value {
			pattern, ok := item.(string)
			if !ok {
				return invalid(managed, "project.variables.ignore entries must be strings")
			}
			ignore = append(ignore, pattern)
		}
	default:
		return invalid(managed, "project.variables.ignore must be an array of strings")
	}
	return VariablePolicy{Managed: managed, Ignore: ignore, Default: false}
}

// UnknownFieldDiagnostics reports every project or resource key this engine
// does not read.
func UnknownFieldDiagnostics(definition map[string]any) []Diagnostic {
	diagnostics := []Diagnostic{}
	if definition == nil {
		return diagnostics
	}
	for _, key := range SortedKeys(definition) {
		if !slices.Contains(IACProjectFields, key) {
			diagnostics = append(diagnostics, unknownField(key, "project"))
		}
	}
	raw, ok := definition["resources"]
	if !ok {
		raw, ok = definition["Resources"]
	}
	if !ok {
		raw, ok = definition["services"]
	}
	if !ok {
		return diagnostics
	}
	for _, item := range flattenResources(raw) {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		address := ResourceAddr(object)
		for _, key := range SortedKeys(object) {
			if !slices.Contains(IACResourceFields, key) {
				diagnostics = append(diagnostics, unknownField(key, address))
			}
		}
	}
	return diagnostics
}

func unknownField(key, address string) Diagnostic {
	path := "resources." + address + "." + key
	if address == "project" {
		path = "project." + key
	}
	return Diagnostic{
		Severity: "error",
		Path:     path,
		Message:  "Unknown field \"" + key + "\" on " + address + ". This version of " + ProviderName + " does not support it; upgrade the provider.",
	}
}

func flattenResources(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return []any{value}
	}
	var out []any
	for _, item := range items {
		out = append(out, flattenResources(item)...)
	}
	return out
}

func dropAttachmentVolumeConfig(resource map[string]any) {
	attachments, ok := resource["volumeAttachments"].(map[string]any)
	if !ok {
		return
	}
	for _, attachment := range attachments {
		if object, ok := attachment.(map[string]any); ok {
			delete(object, "volumeConfig")
		}
	}
}

// GraphToEnvironmentConfig compiles a graph into the EnvironmentConfig patch
// Railway stores.
func GraphToEnvironmentConfig(graph Graph, options CompileOptions) map[string]any {
	services := map[string]any{}
	config := map[string]any{"services": services}
	namesByID := map[string]string{}
	for _, resource := range graph.Resources {
		namesByID[ResourceAddr(resource)] = ResourceName(resource)
	}
	existing := map[string]bool{}
	for _, id := range options.ExistingServiceIDs {
		existing[id] = true
	}
	section := func(name string) map[string]any {
		if object, ok := config[name].(map[string]any); ok {
			return object
		}
		object := map[string]any{}
		config[name] = object
		return object
	}

	for _, resource := range graph.Resources {
		switch ResourceType(resource) {
		case "service", "database":
			name := ResourceName(resource)
			serviceKey, ok := options.ServiceIDsByName[name].(string)
			if !ok {
				serviceKey = name
			}
			isNew := !existing[serviceKey]
			volumeID, hasVolumeID := options.VolumeIDsByServiceName[name].(string)
			if ResourceType(resource) == "database" {
				services[serviceKey] = databaseToEnvironmentConfig(resource, isNew, volumeID, hasVolumeID)
			} else {
				services[serviceKey] = serviceToEnvironmentConfig(resource, namesByID, isNew, options.VolumeIDsByName)
			}
			if ResourceType(resource) == "database" && hasVolumeID {
				volume := map[string]any{"isCreated": true}
				if region, ok := databaseRegion(resource); ok {
					volume["region"] = region
				}
				section("volumes")[volumeID] = volume
			}
		case "volume":
			volumes := section("volumes")
			existingID, hasExisting := options.VolumeIDsByName[ResourceName(resource)].(string)
			key := ResourceName(resource)
			if hasExisting {
				key = existingID
			}
			volume, ok := resource["config"]
			if ok {
				volume = Clone(volume)
			} else {
				volume = map[string]any{}
			}
			if !hasExisting {
				if object, ok := volume.(map[string]any); ok {
					object["isCreated"] = true
				}
			}
			volumes[key] = volume
		case "bucket":
			buckets := section("buckets")
			existingID, hasExisting := options.BucketIDsByName[ResourceName(resource)].(string)
			key := ResourceName(resource)
			if hasExisting {
				key = existingID
			}
			bucket, ok := resource["config"]
			if ok {
				bucket = Clone(bucket)
			} else {
				bucket = map[string]any{}
			}
			if !hasExisting {
				if object, ok := bucket.(map[string]any); ok {
					object["isCreated"] = true
				}
			}
			if groupID, ok := FieldStrOK(resource, "groupId"); ok {
				bucket = setIndex(bucket, "groupId", groupID)
			}
			buckets[key] = bucket
		case "group":
			section("groups")[ResourceName(resource)] = PruneEmpty(map[string]any{
				"isCreated":   true,
				"name":        ResourceName(resource),
				"color":       Clone(resource["color"]),
				"icon":        Clone(resource["icon"]),
				"isCollapsed": Clone(resource["isCollapsed"]),
			})
		}
	}

	return pruneEmptyMap(config)
}

// databaseRegion is the one region a database's deploy.multiRegionConfig
// places it in, if exactly one.
func databaseRegion(database map[string]any) (string, bool) {
	deploy, _ := get(database, "deploy")
	multi, _ := get(deploy, "multiRegionConfig")
	return singleRegion(multi)
}

func singleRegion(multiRegionConfig any) (string, bool) {
	regions, ok := multiRegionConfig.(map[string]any)
	if !ok {
		return "", false
	}
	var present []string
	for _, key := range SortedKeys(regions) {
		if regions[key] != nil {
			present = append(present, key)
		}
	}
	if len(present) != 1 {
		return "", false
	}
	return present[0], true
}

func databaseDeploy(database map[string]any, requiredMountPath string) map[string]any {
	deploy := map[string]any{"requiredMountPath": requiredMountPath}
	databaseDeployBlock, _ := get(database, "deploy")
	if multi, ok := get(databaseDeployBlock, "multiRegionConfig"); ok {
		deploy["multiRegionConfig"] = Clone(multi)
	}
	return deploy
}

func databaseToEnvironmentConfig(database map[string]any, isNew bool, volumeID string, hasVolumeID bool) any {
	engine, ok := FieldStrOK(database, "engine")
	if !ok {
		engine = "postgres"
	}
	image, ok := FieldStrOK(database, "image")
	if !ok {
		image = "postgres:16"
	}
	if engine != "postgres" {
		config := map[string]any{
			"source": map[string]any{"image": image},
		}
		if isNew {
			config["isCreated"] = true
		}
		if mount, ok := FieldStrOK(database, "defaultMountPath"); ok {
			config["deploy"] = databaseDeploy(database, mount)
			if hasVolumeID {
				config["volumeMounts"] = map[string]any{volumeID: map[string]any{"mountPath": mount}}
			}
		}
		return PruneEmpty(config)
	}

	value := func(text string) map[string]any { return map[string]any{"value": text} }
	config := map[string]any{
		"source": map[string]any{"image": image},
		"deploy": databaseDeploy(database, "/var/lib/postgresql/data"),
		"variables": map[string]any{
			"PGDATA":                              value("/var/lib/postgresql/data/pgdata"),
			"PGHOST":                              value("${{RAILWAY_PRIVATE_DOMAIN}}"),
			"PGPORT":                              value("5432"),
			"PGUSER":                              value("${{POSTGRES_USER}}"),
			"PGDATABASE":                          value("${{POSTGRES_DB}}"),
			"PGPASSWORD":                          value("${{POSTGRES_PASSWORD}}"),
			"POSTGRES_DB":                         value("railway"),
			"DATABASE_URL":                        value("postgresql://${{PGUSER}}:${{POSTGRES_PASSWORD}}@${{RAILWAY_PRIVATE_DOMAIN}}:5432/${{PGDATABASE}}"),
			"POSTGRES_USER":                       value("postgres"),
			"SSL_CERT_DAYS":                       value("820"),
			"POSTGRES_PASSWORD":                   map[string]any{"generator": "secret(32, \"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ\")"},
			"DATABASE_PUBLIC_URL":                 value("postgresql://${{PGUSER}}:${{POSTGRES_PASSWORD}}@${{RAILWAY_TCP_PROXY_DOMAIN}}:${{RAILWAY_TCP_PROXY_PORT}}/${{PGDATABASE}}"),
			"RAILWAY_DEPLOYMENT_DRAINING_SECONDS": value("60"),
		},
		"networking": map[string]any{"tcpProxies": map[string]any{"5432": map[string]any{}}},
	}
	if isNew {
		config["isCreated"] = true
	}
	if hasVolumeID {
		config["volumeMounts"] = map[string]any{volumeID: map[string]any{"mountPath": "/var/lib/postgresql/data"}}
	}
	return PruneEmpty(config)
}

func serviceToEnvironmentConfig(service map[string]any, resourceNamesByID map[string]string, isNew bool, volumeIDsByName map[string]any) any {
	config := map[string]any{}
	if isNew {
		config["isCreated"] = true
	}
	if source, ok := service["source"]; ok {
		sourceType, hasType := FieldStrOK(source, "type")
		out := map[string]any{}
		if hasType && sourceType == "github" {
			if repo, ok := get(source, "repo"); ok {
				out["repo"] = Clone(repo)
			}
			if branch, ok := get(source, "branch"); ok {
				out["branch"] = Clone(branch)
			}
		}
		if hasType && sourceType == "image" {
			if image, ok := get(source, "image"); ok {
				out["image"] = Clone(image)
			}
		}
		for _, key := range []string{"rootDirectory", "commitSha", "upstreamUrl", "checkSuites", "autoUpdates"} {
			if value, ok := get(source, key); ok {
				out[key] = Clone(value)
			}
		}
		if pruned := pruneEmptyMap(out); len(pruned) > 0 {
			config["source"] = pruned
		}
	}
	if build, ok := service["build"]; ok {
		config["build"] = Clone(build)
	}
	if deploy, ok := service["deploy"]; ok {
		config["deploy"] = Clone(deploy)
	}
	if variables, ok := service["variables"]; ok {
		config["variables"] = variablesToEnvironmentConfig(variables, resourceNamesByID)
	}
	if networking, ok := service["networking"]; ok {
		config["networking"] = Clone(networking)
	}
	mounts := map[string]any{}
	if attachments, ok := service["volumeAttachments"].(map[string]any); ok {
		for _, key := range SortedKeys(attachments) {
			attachment := attachments[key]
			volume, ok := FieldStrOK(attachment, "volume")
			if !ok {
				continue
			}
			volumeName, ok := resourceNamesByID[volume]
			if !ok {
				_, volumeName, _ = strings.Cut(volume, ".")
			}
			volumeID, ok := volumeIDsByName[volumeName].(string)
			if !ok {
				volumeID = volumeName
			}
			mountPath, _ := get(attachment, "mountPath")
			mount := map[string]any{"mountPath": Clone(mountPath)}
			if schedules, ok := get(attachment, "backupSchedules"); ok {
				mount["backupSchedules"] = Clone(schedules)
			}
			mounts[volumeID] = PruneEmpty(mount)
		}
	}
	if existing, ok := service["volumeMounts"].(map[string]any); ok {
		for key, value := range existing {
			mounts[key] = Clone(value)
		}
	}
	if len(mounts) > 0 {
		config["volumeMounts"] = mounts
	}
	for _, key := range []string{
		"configFile",
		"parentServiceId",
		"groupId",
		"clusterRole",
		"replicaConfig",
		"clusterDisplay",
		"tracing",
		"cdn",
	} {
		if value, ok := service[key]; ok {
			config[key] = Clone(value)
		}
	}
	return PruneEmpty(config)
}

func variablesToEnvironmentConfig(variables any, resourceNamesByID map[string]string) any {
	object, ok := variables.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	for key, value := range object {
		if kind, ok := FieldStrOK(value, "type"); ok && kind == "preserve" {
			continue
		}
		out[key] = variableToConfig(value, resourceNamesByID)
	}
	return out
}

func variableToConfig(value any, resourceNamesByID map[string]string) any {
	kind, _ := FieldStrOK(value, "type")
	switch kind {
	case "literal":
		copied := Clone(value)
		if object, ok := copied.(map[string]any); ok {
			delete(object, "type")
		}
		return copied
	case "raw":
		if raw, ok := get(value, "value"); ok {
			return Clone(raw)
		}
		return map[string]any{}
	case "sharedReference":
		return map[string]any{"value": "${{shared." + FieldStr(value, "name") + "}}"}
	case "reference":
		resource := FieldStr(value, "resource")
		name, ok := resourceNamesByID[resource]
		if !ok {
			name = resource
		}
		return map[string]any{"value": "${{" + name + "." + FieldStr(value, "output") + "}}"}
	default:
		return Clone(value)
	}
}

func databaseEngineFromImage(image string) (string, bool) {
	ref, ok := parseImageRef(image)
	if !ok {
		return "", false
	}
	switch ref.path[strings.LastIndex(ref.path, "/")+1:] {
	case "postgres", "postgresql", "postgres-ssl", "postgres-patroni":
		return "postgres", true
	case "mysql", "mysql-wrapper":
		return "mysql", true
	case "redis", "redis-sentinel":
		return "redis", true
	case "mongo", "mongodb":
		return "mongo", true
	default:
		return "", false
	}
}

// imageRef is a parsed container image reference. Ported from railwayapp/cli
// src/controllers/database_engines.rs.
type imageRef struct {
	domain string
	path   string
	tag    string
	digest string
}

func parseImageRef(image string) (imageRef, bool) {
	image = strings.TrimSpace(image)
	if image == "" {
		return imageRef{}, false
	}
	remainder, digest := image, ""
	if before, after, found := strings.Cut(image, "@"); found {
		if after == "" {
			return imageRef{}, false
		}
		remainder, digest = before, after
	}
	domain, pathAndTag := "", remainder
	if first, rest, found := strings.Cut(remainder, "/"); found && isRegistryHost(first) {
		domain, pathAndTag = first, rest
	}
	// A tag can only live in the final path segment; a colon anywhere earlier
	// belongs to a registry port, which the host split above already consumed.
	lastSlash := strings.LastIndex(pathAndTag, "/") + 1
	path, tag := pathAndTag, ""
	if colon := strings.Index(pathAndTag[lastSlash:], ":"); colon >= 0 {
		path = pathAndTag[:lastSlash+colon]
		tag = pathAndTag[lastSlash+colon+1:]
	}
	if path == "" {
		return imageRef{}, false
	}
	// docker.io is the implicit registry; normalize it away so a reference
	// written either way compares equal.
	if domain == "docker.io" {
		domain = ""
	}
	return imageRef{domain: domain, path: path, tag: tag, digest: digest}, true
}

func isRegistryHost(segment string) bool {
	return segment == "localhost" || strings.Contains(segment, ".") || strings.Contains(segment, ":")
}

// EnvironmentConfigToGraph imports a live EnvironmentConfig as a graph.
func EnvironmentConfigToGraph(config map[string]any, options ConfigToGraphOptions) Graph {
	var resources []any
	groups, _ := config["groups"].(map[string]any)
	groupNamesByID := map[string]string{}
	for id, group := range groups {
		name, ok := FieldStrOK(group, "name")
		if !ok {
			name = id
		}
		groupNamesByID[id] = name
	}
	groupName := func(id string) string {
		if name, ok := groupNamesByID[id]; ok {
			return name
		}
		return id
	}
	isDeleted := func(value any) bool {
		deleted, ok := Field(value, "isDeleted").(bool)
		return ok && deleted
	}

	services, _ := config["services"].(map[string]any)
	buckets, _ := config["buckets"].(map[string]any)
	referenced := map[string]bool{}
	for _, service := range services {
		if groupID, ok := FieldStrOK(service, "groupId"); ok {
			referenced[groupID] = true
		}
	}
	for _, groupID := range options.VolumeGroupIDsByID {
		if text, ok := groupID.(string); ok {
			referenced[text] = true
		}
	}
	bucketGroupID := func(bucketID string, bucket any) (string, bool) {
		if groupID, ok := options.BucketGroupIDsByID[bucketID].(string); ok {
			return groupID, true
		}
		return FieldStrOK(bucket, "groupId")
	}
	for bucketID, bucket := range buckets {
		if groupID, ok := bucketGroupID(bucketID, bucket); ok {
			referenced[groupID] = true
		}
	}
	for _, groupID := range SortedKeys(referenced) {
		parent, hasParent := FieldStrOK(groups[groupID], "groupId")
		for hasParent {
			if referenced[parent] {
				break
			}
			referenced[parent] = true
			parent, hasParent = FieldStrOK(groups[parent], "groupId")
		}
	}

	for _, groupID := range SortedKeys(groups) {
		group := groups[groupID]
		if isDeleted(group) || !referenced[groupID] {
			continue
		}
		name, ok := FieldStrOK(group, "name")
		if !ok {
			name = groupID
		}
		node := pruneEmptyMap(map[string]any{
			"address":     ResourceAddress("group", name),
			"type":        "group",
			"name":        name,
			"color":       Clone(Field(group, "color")),
			"icon":        Clone(Field(group, "icon")),
			"isCollapsed": Clone(Field(group, "isCollapsed")),
		})
		if parent, ok := FieldStrOK(group, "groupId"); ok {
			node["groupId"] = groupName(parent)
		}
		resources = append(resources, node)
	}

	for _, serviceID := range SortedKeys(services) {
		service := services[serviceID]
		if service == nil || isDeleted(service) {
			continue
		}
		name, ok := options.ServiceNamesByID[serviceID].(string)
		if !ok {
			name = serviceID
		}
		source, hasSource := get(service, "source")
		deploy, hasDeploy := get(service, "deploy")
		imageName, hasImage := FieldStrOK(source, "image")
		templateID, hasTemplate := options.TemplateServiceIDsByID[serviceID]
		fromTemplate := hasTemplate && templateID != nil
		if hasImage && fromTemplate {
			if engine, ok := databaseEngineFromImage(imageName); ok {
				output := "DATABASE_URL"
				switch engine {
				case "redis":
					output = "REDIS_URL"
				case "mysql":
					output = "MYSQL_URL"
				case "mongo":
					output = "MONGO_URL"
				}
				volumeMounts, _ := get(service, "volumeMounts")
				var defaultMountPath any
				if mounts, ok := volumeMounts.(map[string]any); ok && len(mounts) > 0 {
					defaultMountPath = Clone(Field(deploy, "requiredMountPath"))
				}
				node := pruneEmptyMap(map[string]any{
					"address":          ResourceAddress("database", name),
					"type":             "database",
					"kind":             "database",
					"engine":           engine,
					"name":             name,
					"image":            imageName,
					"output":           output,
					"defaultMountPath": defaultMountPath,
					"deploy":           Clone(deploy),
					"volumeMounts":     Clone(volumeMounts),
				})
				if networking, ok := networkingFromEnvironmentConfig(service, serviceID, options); ok {
					node["networking"] = networking
				}
				if groupID, ok := FieldStrOK(service, "groupId"); ok {
					node["groupId"] = groupName(groupID)
				}
				resources = append(resources, node)
				continue
			}
		}

		kind := "empty"
		if _, ok := get(source, "repo"); ok {
			kind = "github"
		} else if hasImage && IsFunctionImage(imageName) {
			kind = "function"
		} else if _, ok := get(source, "image"); ok {
			kind = "docker-image"
		} else if _, ok := get(deploy, "cronSchedule"); ok {
			kind = "function"
		}
		node := map[string]any{
			"address": ResourceAddress("service", name),
			"type":    "service",
			"kind":    kind,
			"name":    name,
		}
		if hasSource {
			src := Clone(source)
			if object, ok := src.(map[string]any); ok {
				if _, ok := get(source, "image"); ok {
					object["type"] = "image"
				} else {
					object["type"] = "github"
				}
			}
			node["source"] = src
		}
		if build, ok := get(service, "build"); ok {
			node["build"] = Clone(build)
		}
		if hasDeploy {
			node["deploy"] = Clone(deploy)
		}
		if variables, ok := get(service, "variables"); ok {
			node["variables"] = variablesFromEnvironmentConfig(variables)
		}
		if networking, ok := networkingFromEnvironmentConfig(service, serviceID, options); ok {
			node["networking"] = networking
		}
		volumeMounts, _ := get(service, "volumeMounts")
		if attachments, ok := volumeAttachmentsFromEnvironmentConfig(volumeMounts, options.VolumeNamesByID); ok {
			node["volumeAttachments"] = attachments
		}
		if configFile, ok := get(service, "configFile"); ok {
			node["configFile"] = Clone(configFile)
		}
		// Per-environment tracing switches. Railway only serialises the ones
		// that are on, so an untraced service has no block at all.
		if tracing, ok := Field(service, "tracing").(map[string]any); ok {
			node["tracing"] = Clone(tracing)
		}
		// Per-environment CDN caching. Railway serialises the block only while
		// caching is on, and only the knobs that differ from the edge defaults.
		if cdn, ok := Field(service, "cdn").(map[string]any); ok {
			node["cdn"] = Clone(cdn)
		}
		if groupID, ok := FieldStrOK(service, "groupId"); ok {
			node["groupId"] = groupName(groupID)
		}
		resources = append(resources, node)
	}

	volumes, _ := config["volumes"].(map[string]any)
	for _, volumeID := range SortedKeys(volumes) {
		volume := volumes[volumeID]
		if volume == nil || isDeleted(volume) {
			continue
		}
		name, ok := options.VolumeNamesByID[volumeID].(string)
		if !ok {
			name = volumeID
		}
		node := map[string]any{
			"address": ResourceAddress("volume", name),
			"type":    "volume",
			"name":    name,
			"config":  Clone(volume),
		}
		if groupID, ok := options.VolumeGroupIDsByID[volumeID].(string); ok {
			node["groupId"] = groupName(groupID)
		}
		resources = append(resources, node)
	}

	for _, bucketID := range SortedKeys(buckets) {
		bucket := buckets[bucketID]
		if bucket == nil || isDeleted(bucket) {
			continue
		}
		name, ok := options.BucketNamesByID[bucketID].(string)
		if !ok {
			name = bucketID
		}
		node := map[string]any{
			"address": ResourceAddress("bucket", name),
			"type":    "bucket",
			"name":    name,
			"config":  Clone(bucket),
		}
		if groupID, ok := bucketGroupID(bucketID, bucket); ok {
			node["groupId"] = groupName(groupID)
		}
		resources = append(resources, node)
	}

	projectName := options.ProjectName
	if projectName == "" {
		projectName = "imported-project"
	}
	if resources == nil {
		resources = []any{}
	}
	return ProjectDefinitionToGraph(map[string]any{
		"name":      projectName,
		"resources": resources,
	})
}

// networkingFromEnvironmentConfig imports a service's live networking (TCP
// proxies, domains) as graph state.
//
// Databases go through this too: a public TCP proxy on a database is exposure
// the plan has to be able to see, and the imported graph is what
// `railway config pull` renders.
func networkingFromEnvironmentConfig(service any, serviceID string, options ConfigToGraphOptions) (any, bool) {
	networking, ok := get(service, "networking")
	if ok {
		networking = Clone(networking)
	} else {
		networking = map[string]any{}
	}
	domains, hasDomains := options.CustomDomainsByServiceID[serviceID]
	if hasDomains {
		networking = setIndex(networking, "customDomains", Clone(domains))
	}
	networking = PruneEmpty(networking)
	if object, ok := networking.(map[string]any); (ok && len(object) > 0) || hasDomains {
		return networking, true
	}
	return nil, false
}

func variablesFromEnvironmentConfig(variables any) any {
	object, ok := variables.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	for key, value := range object {
		if value == nil {
			continue
		}
		sealed, _ := Field(value, "isSealed").(bool)
		literal, isString := FieldStrOK(value, "value")
		masked := !isString || literal == "" || literal == "*****" || sealed
		if masked {
			out[key] = map[string]any{"type": "preserve"}
		} else {
			out[key] = map[string]any{"type": "literal", "value": literal}
		}
	}
	return out
}

func volumeAttachmentsFromEnvironmentConfig(volumeMounts any, volumeNamesByID map[string]any) (any, bool) {
	mounts, ok := volumeMounts.(map[string]any)
	if !ok {
		return nil, false
	}
	attachments := map[string]any{}
	for _, volumeID := range SortedKeys(mounts) {
		mount := mounts[volumeID]
		path, ok := FieldStrOK(mount, "mountPath")
		if !ok {
			continue
		}
		name, ok := volumeNamesByID[volumeID].(string)
		if !ok {
			name = volumeID
		}
		attachment := map[string]any{
			"volume":    ResourceAddress("volume", name),
			"mountPath": path,
		}
		if schedules, ok := get(mount, "backupSchedules"); ok {
			attachment["backupSchedules"] = Clone(schedules)
		}
		attachments[name] = attachment
	}
	if len(attachments) == 0 {
		return nil, false
	}
	return attachments, true
}

// ComposePatch adds deletion markers for every live volume and service the
// desired config drops.
func ComposePatch(currentConfig, desiredConfig map[string]any) map[string]any {
	return pruneEmptyMap(addDeletionMarkers(currentConfig, desiredConfig))
}

func addDeletionMarkers(currentConfig, desiredConfig map[string]any) map[string]any {
	next := CloneMap(desiredConfig)
	if next == nil {
		next = map[string]any{}
	}
	section := func(name string) any {
		if existing, ok := next[name]; ok {
			return existing
		}
		created := map[string]any{}
		next[name] = created
		return created
	}
	isDeleted := func(value any) bool {
		deleted, ok := Field(value, "isDeleted").(bool)
		return ok && deleted
	}
	desiredVolumes, _ := get(desiredConfig, "volumes")
	if volumes, ok := currentConfig["volumes"].(map[string]any); ok {
		for _, volumeID := range SortedKeys(volumes) {
			current := volumes[volumeID]
			if current == nil || isDeleted(current) {
				continue
			}
			if _, ok := get(desiredVolumes, volumeID); ok {
				continue
			}
			next["volumes"] = setIndex(section("volumes"), volumeID, map[string]any{"isDeleted": true})
		}
	}
	desiredServices, _ := get(desiredConfig, "services")
	if services, ok := currentConfig["services"].(map[string]any); ok {
		for _, serviceID := range SortedKeys(services) {
			current := services[serviceID]
			if current == nil || isDeleted(current) {
				continue
			}
			desiredService, ok := get(desiredServices, serviceID)
			if !ok {
				next["services"] = setIndex(section("services"), serviceID, map[string]any{"isDeleted": true})
				continue
			}
			variables, ok := Field(current, "variables").(map[string]any)
			if !ok {
				continue
			}
			desiredVariables, _ := get(desiredService, "variables")
			for _, variableName := range SortedKeys(variables) {
				if variables[variableName] == nil {
					continue
				}
				if _, ok := get(desiredVariables, variableName); ok {
					continue
				}
				nextServices, ok := section("services").(map[string]any)
				if !ok {
					continue
				}
				service, ok := nextServices[serviceID].(map[string]any)
				if !ok {
					if _, present := nextServices[serviceID]; present {
						continue
					}
					service = map[string]any{}
					nextServices[serviceID] = service
				}
				existing, present := service["variables"]
				if !present {
					existing = map[string]any{}
				}
				service["variables"] = setIndex(existing, variableName, nil)
			}
		}
	}
	return next
}
