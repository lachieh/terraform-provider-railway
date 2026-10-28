// Ported from railwayapp/cli src/iac/eval.rs (MIT, Copyright (c) Railway Corp.)
// Only normalize_project is ported; the language runners live outside this
// package.

package iac

// NormalizeProject fills the defaults the authoring SDKs leave implicit:
// resource addresses, service kinds, `start` as the deploy start command, a
// string `build` as the build command, and `env` as `variables`. The input is
// not modified.
func NormalizeProject(project map[string]any) map[string]any {
	project = CloneMap(project)
	if project == nil {
		project = map[string]any{}
	}
	if _, ok := project["resources"]; !ok {
		if resources, ok := project["Resources"]; ok {
			project["resources"] = Clone(resources)
		}
	}
	if resources, ok := project["resources"].([]any); ok {
		for _, item := range resources {
			resource, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if _, ok := resource["address"]; !ok {
				kind, ok := FieldStrOK(resource, "type")
				if !ok {
					kind = "service"
				}
				name, ok := FieldStrOK(resource, "name")
				if !ok {
					name = "web"
				}
				resource["address"] = kind + "." + name
			}
			if _, ok := resource["kind"]; !ok {
				if kind, ok := FieldStrOK(resource, "type"); ok && kind == "service" {
					resource["kind"] = "empty"
				}
			}
			if start, ok := resource["start"]; ok {
				if _, ok := resource["deploy"]; !ok {
					resource["deploy"] = map[string]any{}
				}
				if _, ok := get(resource["deploy"], "startCommand"); !ok {
					resource["deploy"] = setIndex(resource["deploy"], "startCommand", Clone(start))
				}
			}
			if build, ok := resource["build"].(string); ok {
				resource["build"] = map[string]any{"buildCommand": build}
			}
			normalizeResourceEnv(resource)
		}
	}
	if _, ok := project["name"]; !ok {
		project["name"] = "app"
	}
	return project
}
