// Ported from railwayapp/cli src/iac/change_set.rs (MIT, Copyright (c) Railway Corp.)

package iac

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	maskedCredentialValue = "*****"
	redactedVariableValue = "«hidden»"
	// maxSourceDiffLines is the longest source diff a plan prints before
	// eliding the rest.
	maxSourceDiffLines = 200
)

// bucketRegions are the object-storage regions a new bucket may name.
// Ported from railwayapp/cli src/controllers/regions.rs (BucketRegion::all).
var bucketRegions = []string{"sjc", "iad", "ams", "sin"}

var (
	friendlyRegionPath = regexp.MustCompile(`^deploy\.multiRegionConfig\.([^.]+)\.numReplicas$`)
	repoConfigFile     = regexp.MustCompile(`railway\.(json|toml)$`)
	templateImageTag   = regexp.MustCompile(`^(?:railwayapp/|ghcr\.io/railwayapp-templates/)?(redis|mysql|mongo|postgres)(?:-ssl)?:(\p{Nd}+)(?:\.\p{Nd}+)*$`)
)

// DiffOptions selects the graphs to diff. An empty Partial means the file
// declares no partial; nil Owners means no resource has an owner yet.
type DiffOptions struct {
	Current      *Graph
	Desired      *Graph
	RevealValues bool
	Partial      string
	Owners       Partials
}

// BranchFollow carries the PR environment plus repo default branches, used
// when a branch-less `github()` source is created. Updates never consult
// this: an omitted branch is owned by the environment. DefaultBranches is
// keyed by lowercased owner/repo.
type BranchFollow struct {
	InPREnvironment bool
	PRBranch        string
	PRRepo          string
	DefaultBranches map[string]string
}

func DiffGraphs(options DiffOptions) ChangeSet {
	return DiffGraphsFollowing(options, nil)
}

func DiffGraphsFollowing(options DiffOptions, follow *BranchFollow) ChangeSet {
	current := graphOrEmpty(options.Current)
	desired := graphOrEmpty(options.Desired)
	changes := []map[string]any{}
	diagnostics := []Diagnostic{}
	desiredBy := map[string]map[string]any{}
	for _, resource := range desired.Resources {
		desiredBy[ResourceAddr(resource)] = resource
	}
	currentBy := map[string]map[string]any{}
	for _, resource := range current.Resources {
		currentBy[pairedAddress(resource, desiredBy)] = resource
	}
	p := EffectivePartial(options.Partial)
	declared := []string{}
	for _, resource := range desired.Resources {
		declared = append(declared, ResourceAddr(resource))
	}

	if p == ProjectPartial && HasNamedPartials(options.Owners) {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: "error",
			Path:     "partial",
			Message:  NamelessFileMessage(),
		})
		return changeSetResult(changes, diagnostics, options.Partial, declared)
	}

	compiled := compileVariablePolicy(current, desired)
	diagnostics = append(diagnostics, compiled.diagnostics...)

	for _, resource := range desired.Resources {
		address := ResourceAddr(resource)
		previous, hasPrevious := currentBy[address]
		if owner, ok := OwnerOf(options.Owners, address); ok && owner != p {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: "error",
				Path:     "resources." + address,
				Message:  ForeignResourceMessage(address, owner),
			})
			continue
		}
		if hasPrevious && isManagedByRepoConfig(previous) {
			configFile, ok := FieldStrOK(previous, "configFile")
			if !ok {
				configFile = "repo config"
			}
			diagnostics = append(diagnostics, Diagnostic{
				Severity: "error",
				Path:     "resources." + address + ".configFile",
				Message: fmt.Sprintf(
					"%s is already managed by %s. Remove or migrate the repo config before managing this service from .railway/railway.ts.",
					ResourceName(previous), configFile,
				),
			})
			continue
		}
		diagnoseVariableDeclarations(resource, compiled, &diagnostics)
		if !hasPrevious {
			diagnoseUnsupportedCustomDomains(resource, &diagnostics, nil)
			diagnoseNewBucketRegion(resource, &diagnostics)
			created := withoutIgnoredVariables(resource, compiled)
			branchLine := ""
			if follow != nil {
				outcome := resolveCreateBranch(created, follow)
				switch outcome.kind {
				case createBranchSet:
					if source, ok := created["source"].(map[string]any); ok {
						source["branch"] = outcome.branch
					}
					branchLine = outcome.line
				case createBranchError:
					diagnostics = append(diagnostics, Diagnostic{
						Severity: "error",
						Path:     "resources." + address + ".source.branch",
						Message:  outcome.message,
					})
					continue
				}
			}
			summary := fmt.Sprintf("Create %s %s", ResourceType(resource), ResourceName(resource))
			if branchLine != "" {
				summary = fmt.Sprintf("Create %s %s (%s)", ResourceType(resource), ResourceName(resource), branchLine)
			}
			change := map[string]any{
				"kind":         "resource.create",
				"address":      address,
				"resource":     created,
				"path":         "resources." + address,
				"summary":      summary,
				"severity":     "safe",
				"deployEffect": deployEffectForResource(resource),
			}
			var details []string
			if branchLine != "" {
				details = append(details, branchLine)
			}
			if IsFunctionResource(resource) {
				if deploy, ok := resource["deploy"]; ok {
					if code, ok := FunctionCodeOf(deploy); ok {
						details = append(details, fmt.Sprintf("source code (%d lines)", rustLineCount(code)))
					}
				}
			}
			if len(details) > 0 {
				change["details"] = stringsToValues(details)
			}
			changes = append(changes, change)
			continue
		}
		previous = CloneMap(previous)
		if ResourceType(previous) != ResourceType(resource) {
			// Paired across service/database: same service on Railway, only
			// the CLI's classification differs. Diff it as the declared kind so
			// edits still apply; the server never sees a type change.
			diagnostics = append(diagnostics, Diagnostic{
				Severity: "warning",
				Path:     "resources." + address,
				Message: fmt.Sprintf(
					"%s exists on Railway as a %s but is declared as a %s. Changes are still applied; declare it with %s() so the config matches Railway.",
					ResourceName(resource), ResourceType(previous), ResourceType(resource), ResourceType(previous),
				),
			})
			// Database nodes carry the image as top-level `image`; service
			// nodes as `source.image`. Mirror it so the source diff is honest.
			image, ok := FieldStrOK(previous, "image")
			if !ok {
				image, ok = FieldStrOK(previous["source"], "image")
			}
			if ok {
				previous["image"] = image
				if _, hasSource := previous["source"]; !hasSource {
					previous["source"] = map[string]any{"type": "image", "image": image}
				}
			}
			previous["type"] = ResourceType(resource)
			previous["address"] = address
		}
		if follow != nil && follow.InPREnvironment {
			if message, ok := prBranchPinError(previous, resource, follow.PRRepo); ok {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: "error",
					Path:     "resources." + address + ".source.branch",
					Message:  message,
				})
			}
		}
		previousName, previousHasName := FieldStrOK(previous, "name")
		desiredName, desiredHasName := FieldStrOK(resource, "name")
		if previousHasName != desiredHasName || previousName != desiredName {
			changes = append(changes, update(
				address,
				"name",
				optionalString(previousName, previousHasName),
				optionalString(desiredName, desiredHasName),
				fmt.Sprintf("Rename %s %s to %s", ResourceType(resource), ResourceName(previous), ResourceName(resource)),
				nil,
				"safe",
			))
		}
		diffVariables(previous, resource, &changes, desiredBy, options.RevealValues, compiled, &diagnostics)
		diffTopLevelField(previous, resource, "source", &changes)
		diffTopLevelField(previous, resource, "build", &changes)
		switch {
		case ResourceType(previous) == "database" && ResourceType(resource) == "database":
			diffDatabaseDeploy(previous, resource, &changes)
		case ResourceType(previous) == "service" && ResourceType(resource) == "service":
			diffServiceDeploy(previous, resource, &changes, &diagnostics)
		default:
			diffTopLevelField(previous, resource, "deploy", &changes)
		}
		diffTopLevelField(previous, resource, "groupId", &changes)
		if ResourceType(resource) == "service" {
			diffTopLevelField(previous, resource, "tracing", &changes)
			diffTopLevelField(previous, resource, "cdn", &changes)
		}
		diffNetworking(previous, resource, &changes, &diagnostics)
		switch {
		case ResourceType(previous) == "bucket" && ResourceType(resource) == "bucket":
			previousRegion, previousHasRegion := bucketRegion(previous)
			desiredRegion, desiredHasRegion := bucketRegion(resource)
			if previousHasRegion != desiredHasRegion || previousRegion != desiredRegion {
				if !desiredHasRegion {
					desiredRegion = "the desired region"
				}
				diagnostics = append(diagnostics, Diagnostic{
					Severity: "error",
					Path:     "resources." + address + ".config.region",
					Message: fmt.Sprintf(
						"Bucket region cannot be changed after creation. Create a new bucket in %s and migrate data instead.",
						desiredRegion,
					),
				})
			} else {
				diffTopLevelField(previous, resource, "config", &changes)
			}
		case ResourceType(previous) == "volume" && ResourceType(resource) == "volume":
			diffVolumeConfig(previous, resource, &changes)
		default:
			diffTopLevelField(previous, resource, "config", &changes)
		}
		diffVolumeAttachments(previous, resource, &changes)
	}

	for _, resource := range current.Resources {
		address := pairedAddress(resource, desiredBy)
		if _, ok := desiredBy[address]; ok {
			continue
		}
		if p != ProjectPartial {
			if owner, ok := OwnerOf(options.Owners, address); !ok || owner != p {
				continue
			}
		}
		if ResourceType(resource) == "volume" {
			mounted := false
			for _, edge := range current.Edges {
				if _, declared := desiredBy[edge.From]; edge.Kind == "mount" && edge.To == address && declared {
					mounted = true
					break
				}
			}
			if mounted {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: "warning",
					Path:     "resources." + address,
					Message: fmt.Sprintf(
						"Volume %s exists on Railway but is not declared in the config. Volumes are never deleted by config apply; delete it from the dashboard if that is intended.",
						ResourceName(resource),
					),
				})
			}
			continue
		}
		changes = append(changes, map[string]any{
			"kind":         "resource.delete",
			"address":      address,
			"previous":     CloneMap(resource),
			"path":         "resources." + address,
			"summary":      fmt.Sprintf("Delete %s %s", ResourceType(resource), ResourceName(resource)),
			"severity":     "destructive",
			"deployEffect": deployEffectForResource(resource),
		})
	}

	return changeSetResult(changes, diagnostics, options.Partial, declared)
}

func graphOrEmpty(graph *Graph) *Graph {
	if graph != nil {
		return graph
	}
	return &Graph{Version: GraphVersion, Variables: DefaultVariablePolicy()}
}

func deployEffectForResource(resource map[string]any) string {
	switch ResourceType(resource) {
	case "service", "database":
		return "deploy"
	default:
		return "none"
	}
}

func optionalString(text string, ok bool) any {
	if !ok {
		return nil
	}
	return text
}

func stringsToValues(texts []string) []any {
	out := make([]any, len(texts))
	for index, text := range texts {
		out[index] = text
	}
	return out
}

// pairedAddress matches a Railway-managed database and a plain service with
// the same name: they are the same service on Railway; only the CLI's
// classification differs. Matching them by name means the plan never
// proposes deleting one to create the other.
func pairedAddress(resource map[string]any, desiredBy map[string]map[string]any) string {
	address := ResourceAddr(resource)
	if _, ok := desiredBy[address]; ok {
		return address
	}
	var sibling string
	switch ResourceType(resource) {
	case "service":
		sibling = "database"
	case "database":
		sibling = "service"
	default:
		return address
	}
	sibling = ResourceAddress(sibling, ResourceName(resource))
	if _, ok := desiredBy[sibling]; ok {
		return sibling
	}
	return address
}

func changeSetResult(changes []map[string]any, diagnostics []Diagnostic, partial string, declared []string) ChangeSet {
	result := ChangeSet{
		Version:     ChangeSetVersion,
		Changes:     changes,
		Diagnostics: diagnostics,
		Declared:    declared,
	}
	if partial != "" {
		result.Partial = &partial
	}
	return result
}

func RenderChangeSet(changeSet ChangeSet) string {
	if len(changeSet.Changes) == 0 {
		return "No changes."
	}
	lines := make([]string, 0, len(changeSet.Changes))
	for _, change := range changeSet.Changes {
		marker := "~"
		switch FieldStr(change, "kind") {
		case "resource.create", "domain.create":
			marker = "+"
		case "resource.delete":
			marker = "-"
		}
		summary, ok := FieldStrOK(change, "summary")
		if !ok {
			summary = "change"
		}
		lines = append(lines, marker+" "+summary)
	}
	return strings.Join(lines, "\n")
}

type parsedPattern struct {
	raw        string
	service    string
	hasService bool
	glob       string
}

func (p parsedPattern) matches(resourceName, key string) bool {
	if p.hasService && p.service != resourceName {
		return false
	}
	return globMatch(p.glob, key)
}

type compiledVariables struct {
	managed     bool
	parseError  bool
	patterns    []parsedPattern
	diagnostics []Diagnostic
}

type patternFault int

const (
	patternFaultNone patternFault = iota
	patternFaultEmpty
	patternFaultSlash
	patternFaultEmptyKey
)

// VariablePolicyReport summarises the desired graph's variables policy
// against the live environment.
func VariablePolicyReport(current, desired *Graph) VariablePolicyReportResult {
	current = graphOrEmpty(current)
	desired = graphOrEmpty(desired)
	compiled := compileVariablePolicy(current, desired)
	byPattern := map[string]int{}
	for _, pattern := range compiled.patterns {
		byPattern[pattern.raw] = countMatches(pattern, current)
	}
	return VariablePolicyReportResult{
		Managed:      desired.Variables.Managed,
		Ignore:       append([]string{}, desired.Variables.Ignore...),
		Default:      desired.Variables.Default,
		IgnoredCount: countIgnored(compiled.patterns, current),
		ByPattern:    byPattern,
	}
}

func compileVariablePolicy(current, desired *Graph) compiledVariables {
	policy := desired.Variables
	var diagnostics []Diagnostic
	if policy.Error != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: "error",
			Path:     "variables",
			Message:  *policy.Error,
		})
		return compiledVariables{managed: policy.Managed, parseError: true, diagnostics: diagnostics}
	}
	names := resourceNames(current, desired)
	var patterns []parsedPattern
	for _, raw := range policy.Ignore {
		pattern, fault := parsePattern(raw)
		if fault != patternFaultNone {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: "error",
				Path:     "variables.ignore",
				Message:  patternFaultMessage(raw, fault),
			})
			continue
		}
		if pattern.hasService && !names[pattern.service] {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: "error",
				Path:     "variables.ignore",
				Message: fmt.Sprintf(
					"variables.ignore pattern \"%s\" names \"%s\", which is neither in the file nor on Railway.",
					raw, pattern.service,
				),
			})
			continue
		}
		patterns = append(patterns, pattern)
	}
	for _, pattern := range patterns {
		if !patternMatchesAny(pattern, current) && !patternMatchesAny(pattern, desired) {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: "warning",
				Path:     "variables.ignore",
				Message:  fmt.Sprintf("variables.ignore pattern \"%s\" matches no variable.", pattern.raw),
			})
		}
	}
	return compiledVariables{managed: policy.Managed, patterns: patterns, diagnostics: diagnostics}
}

func parsePattern(raw string) (parsedPattern, patternFault) {
	if raw == "" {
		return parsedPattern{}, patternFaultEmpty
	}
	if strings.Count(raw, "/") > 1 {
		return parsedPattern{}, patternFaultSlash
	}
	if service, key, found := strings.Cut(raw, "/"); found {
		if service == "" || key == "" {
			return parsedPattern{}, patternFaultEmptyKey
		}
		return parsedPattern{raw: raw, service: service, hasService: true, glob: key}, patternFaultNone
	}
	return parsedPattern{raw: raw, glob: raw}, patternFaultNone
}

func patternFaultMessage(raw string, fault patternFault) string {
	switch fault {
	case patternFaultEmpty:
		return "variables.ignore contains an empty pattern."
	case patternFaultSlash:
		return fmt.Sprintf("variables.ignore pattern \"%s\" has more than one '/'.", raw)
	default:
		return fmt.Sprintf("variables.ignore pattern \"%s\" has an empty key.", raw)
	}
}

func resourceNames(current, desired *Graph) map[string]bool {
	names := map[string]bool{}
	for _, graph := range []*Graph{current, desired} {
		for _, resource := range graph.Resources {
			if name := ResourceName(resource); name != "" {
				names[name] = true
			}
		}
	}
	return names
}

func patternMatchesAny(pattern parsedPattern, graph *Graph) bool {
	for _, resource := range graph.Resources {
		name := ResourceName(resource)
		variables, ok := resource["variables"].(map[string]any)
		if !ok {
			continue
		}
		for key := range variables {
			if pattern.matches(name, key) {
				return true
			}
		}
	}
	return false
}

func countMatches(pattern parsedPattern, graph *Graph) int {
	count := 0
	for _, resource := range graph.Resources {
		name := ResourceName(resource)
		variables, _ := resource["variables"].(map[string]any)
		for key := range variables {
			if pattern.matches(name, key) {
				count++
			}
		}
	}
	return count
}

func countIgnored(patterns []parsedPattern, graph *Graph) int {
	count := 0
	for _, resource := range graph.Resources {
		name := ResourceName(resource)
		variables, _ := resource["variables"].(map[string]any)
		for key := range variables {
			if _, ok := firstMatch(patterns, name, key); ok {
				count++
			}
		}
	}
	return count
}

func firstMatch(patterns []parsedPattern, name, key string) (string, bool) {
	for _, pattern := range patterns {
		if pattern.matches(name, key) {
			return pattern.raw, true
		}
	}
	return "", false
}

func hasVariablesObject(resource map[string]any) bool {
	return isObject(resource["variables"])
}

func filteredVariables(variables any, patterns []parsedPattern, name string) map[string]any {
	out := map[string]any{}
	object, _ := variables.(map[string]any)
	for key, value := range object {
		if _, ignored := firstMatch(patterns, name, key); !ignored {
			out[key] = Clone(value)
		}
	}
	return out
}

func nonIgnoredKeys(variables any, patterns []parsedPattern, name string) []string {
	var keys []string
	object, _ := variables.(map[string]any)
	for key := range object {
		if _, ignored := firstMatch(patterns, name, key); !ignored {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func missingEnvMessage(name string, keys []string) string {
	list := strings.Join(keys, ", ")
	if len(keys) > 8 {
		list = strings.Join(keys[:8], ", ") + "…"
	}
	return fmt.Sprintf(
		"%s declares no env but Railway has %d variables (%s). Declare them, mark them preserve(), or add them to variables.ignore.",
		name, len(keys), list,
	)
}

func diagnoseVariableDeclarations(resource map[string]any, policy compiledVariables, diagnostics *[]Diagnostic) {
	if policy.parseError {
		return
	}
	variables, ok := resource["variables"].(map[string]any)
	if !ok {
		return
	}
	if !policy.managed {
		if ResourceType(resource) == "service" {
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity: "error",
				Path:     "resources." + ResourceAddr(resource) + ".variables",
				Message: fmt.Sprintf(
					"Variables are unmanaged (project variables.managed is false). Remove env from %s or set managed: true.",
					ResourceName(resource),
				),
			})
		}
		return
	}
	for _, key := range SortedKeys(variables) {
		if pattern, ok := firstMatch(policy.patterns, ResourceName(resource), key); ok {
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity: "error",
				Path:     "resources." + ResourceAddr(resource) + ".variables." + key,
				Message:  fmt.Sprintf("%s.%s is declared and also ignored by \"%s\".", ResourceName(resource), key, pattern),
			})
		}
	}
}

func withoutIgnoredVariables(resource map[string]any, policy compiledVariables) map[string]any {
	resource = CloneMap(resource)
	if !policy.managed || policy.parseError {
		return resource
	}
	name := ResourceName(resource)
	variables, ok := resource["variables"].(map[string]any)
	if !ok {
		return resource
	}
	empty := true
	for key := range variables {
		if _, ignored := firstMatch(policy.patterns, name, key); ignored {
			delete(variables, key)
		} else {
			empty = false
		}
	}
	if empty {
		delete(resource, "variables")
	}
	return resource
}

// globMatch treats `*` as the only metacharacter, matching any run of
// characters, including none. The match is case-sensitive and covers the
// whole key.
func globMatch(pattern, text string) bool {
	patternChars := []rune(pattern)
	textChars := []rune(text)
	p, t := 0, 0
	starP, starT := -1, 0
	for t < len(textChars) {
		switch {
		case p < len(patternChars) && patternChars[p] == textChars[t]:
			p++
			t++
		case p < len(patternChars) && patternChars[p] == '*':
			starP = p
			starT = t
			p++
		case starP >= 0:
			p = starP + 1
			starT++
			t = starT
		default:
			return false
		}
	}
	for p < len(patternChars) && patternChars[p] == '*' {
		p++
	}
	return p == len(patternChars)
}

func diffVariables(
	previous, resource map[string]any,
	changes *[]map[string]any,
	resourcesByAddress map[string]map[string]any,
	revealValues bool,
	policy compiledVariables,
	diagnostics *[]Diagnostic,
) {
	if policy.parseError || !policy.managed {
		return
	}
	name := ResourceName(resource)
	// Template databases do not carry variables on the imported graph. Only
	// services are asked to declare env; a database with no variables key
	// keeps today's diff, which sees nothing to delete.
	if ResourceType(resource) == "service" && !hasVariablesObject(resource) {
		if keys := nonIgnoredKeys(previous["variables"], policy.patterns, name); len(keys) > 0 {
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity: "error",
				Path:     "resources." + ResourceAddr(resource) + ".variables",
				Message:  missingEnvMessage(name, keys),
			})
		}
		return
	}
	_, previousHas := previous["variables"]
	_, resourceHas := resource["variables"]
	if !previousHas && !resourceHas {
		return
	}
	before := filteredVariables(previous["variables"], policy.patterns, name)
	after := filteredVariables(resource["variables"], policy.patterns, name)
	address := ResourceAddr(resource)
	for _, key := range SortedKeys(after) {
		value := after[key]
		if isPreservedVariable(value) {
			continue
		}
		current, hasCurrent := before[key]
		if !isUnknownCurrentVariable(current, hasCurrent) &&
			StableStringify(normalizeVariableForDiff(current, hasCurrent, resourcesByAddress)) ==
				StableStringify(normalizeVariableForDiff(value, true, resourcesByAddress)) {
			continue
		}
		verb := "Set"
		if hasCurrent {
			verb = "Update"
		}
		change := map[string]any{
			"kind":     "variable.set",
			"address":  address,
			"variable": key,
			"after":    value,
			"path":     "resources." + address + ".variables." + key,
			"summary":  fmt.Sprintf("%s variable %s.%s", verb, ResourceName(resource), key),
			"details": []any{fmt.Sprintf(
				"%s.%s (%s → %s)",
				ResourceName(resource),
				key,
				formatVariableDiffValue(current, hasCurrent, resourcesByAddress, revealValues),
				formatVariableDiffValue(value, true, resourcesByAddress, revealValues),
			)},
			"severity":     "safe",
			"deployEffect": "deploy",
		}
		if hasCurrent {
			change["before"] = current
		}
		*changes = append(*changes, change)
	}
	for _, key := range SortedKeys(before) {
		if _, ok := after[key]; ok {
			continue
		}
		*changes = append(*changes, map[string]any{
			"kind":         "variable.delete",
			"address":      address,
			"variable":     key,
			"previous":     before[key],
			"path":         "resources." + address + ".variables." + key,
			"summary":      fmt.Sprintf("Delete variable %s.%s", ResourceName(resource), key),
			"severity":     "destructive",
			"deployEffect": "deploy",
		})
	}
}

func diffNetworking(previous, resource map[string]any, changes *[]map[string]any, diagnostics *[]Diagnostic) {
	before, hasBefore := previous["networking"]
	after, hasAfter := resource["networking"]
	// The database helpers take no networking, and pulled files only author it
	// when a proxy exists, so a database node without a networking block keeps
	// whatever exposure it has.
	if ResourceType(resource) == "database" && !hasAfter {
		return
	}
	beforeDomains, _ := get(before, "customDomains")
	diagnoseUnsupportedCustomDomains(resource, diagnostics, beforeDomains)
	beforeCopy := any(map[string]any{})
	if hasBefore {
		beforeCopy = Clone(before)
	}
	afterCopy := any(map[string]any{})
	if hasAfter {
		afterCopy = Clone(after)
	}
	for _, value := range []any{beforeCopy, afterCopy} {
		if object, ok := value.(map[string]any); ok {
			delete(object, "customDomains")
			delete(object, "serviceDomains")
		}
	}
	diagnoseUnremovableTCPProxies(resource, diagnostics, beforeCopy, afterCopy)
	normalizedBefore := normalizeForDiff("networking", beforeCopy)
	normalizedAfter := normalizeForDiff("networking", afterCopy)
	// Plan the change only when applying the authored block moves something.
	// The block is a sparse patch, so what the plan promises is its effect,
	// while the change set still carries the block as written: that is the
	// payload the apply sends, and a `null` entry is how a proxy is deleted.
	applied := normalizeForDiff("networking", networkingAfterApply(beforeCopy, afterCopy))
	if StableStringify(normalizedBefore) != StableStringify(applied) {
		*changes = append(*changes, update(
			ResourceAddr(resource),
			"networking",
			normalizedBefore,
			normalizedAfter,
			fmt.Sprintf("Update %s networking", ResourceName(resource)),
			nil,
			"safe",
		))
	}
}

// networkingAfterApply is what `networking` looks like once Railway has
// applied the authored block.
//
// environmentApplyChangeSet reads the block as a sparse patch: a key the
// author leaves out stays as Railway has it, and inside `tcpProxies` an entry
// keeps or creates a proxy, a `null` entry deletes one, and an unmentioned port
// is left alone, so an empty map changes nothing. The plan diffs against that
// same effect, because a plan that plans anything else promises work the apply
// will not do and then plans it again after every apply.
func networkingAfterApply(before, after any) any {
	effective := map[string]any{}
	if object, ok := before.(map[string]any); ok {
		effective = CloneMap(object)
	}
	authored, ok := after.(map[string]any)
	if !ok {
		return effective
	}
	for _, key := range SortedKeys(authored) {
		value := authored[key]
		if key != "tcpProxies" {
			effective[key] = Clone(value)
			continue
		}
		proxies := map[string]any{}
		if live, ok := Field(before, "tcpProxies").(map[string]any); ok {
			proxies = CloneMap(live)
		}
		entries, _ := value.(map[string]any)
		for _, port := range SortedKeys(entries) {
			entry := entries[port]
			if entry == nil {
				delete(proxies, port)
			} else if _, exists := proxies[port]; !exists {
				proxies[port] = Clone(entry)
			}
		}
		effective[key] = proxies
	}
	return effective
}

// diagnoseUnremovableTCPProxies warns that an empty `tcpProxies` map, which
// is what `tcp: []` compiles to, cannot take a proxy away, because the apply
// leaves every port the block does not mention alone. Say that out loud
// instead of leaving the author with a file that reads "private" next to a
// proxy that is still serving traffic.
func diagnoseUnremovableTCPProxies(resource map[string]any, diagnostics *[]Diagnostic, before, after any) {
	authored, ok := Field(after, "tcpProxies").(map[string]any)
	if !ok || len(authored) != 0 {
		return
	}
	live, ok := Field(before, "tcpProxies").(map[string]any)
	if !ok || len(live) == 0 {
		return
	}
	ports := SortedKeys(live)
	removals := make([]string, len(ports))
	for index, port := range ports {
		removals[index] = "\"" + port + "\": null"
	}
	*diagnostics = append(*diagnostics, Diagnostic{
		Severity: "warning",
		Path:     "resources." + ResourceAddr(resource) + ".networking.tcpProxies",
		Message: fmt.Sprintf(
			"%s has a public TCP proxy on port %s, and an empty tcpProxies map does not remove it. Author networking.tcpProxies = { %s } to remove the proxy, or remove it from the dashboard.",
			ResourceName(resource), strings.Join(ports, ", "), strings.Join(removals, ", "),
		),
	})
}

func diagnoseUnsupportedCustomDomains(resource map[string]any, diagnostics *[]Diagnostic, existingDomains any) {
	desired, ok := Field(resource["networking"], "customDomains").(map[string]any)
	if !ok {
		return
	}
	existing, hasExisting := existingDomains.(map[string]any)
	for _, domain := range SortedKeys(desired) {
		if hasExisting {
			if _, ok := existing[domain]; ok {
				continue
			}
		}
		*diagnostics = append(*diagnostics, Diagnostic{
			Severity: "error",
			Path:     "resources." + ResourceAddr(resource) + ".domains." + domain,
			Message: fmt.Sprintf(
				"Custom-domain registration is not supported by Railway configuration. Add %s in the dashboard, then run railway config pull.",
				domain,
			),
		})
	}
}

func diffVolumeAttachments(previous, resource map[string]any, changes *[]map[string]any) {
	before, hasBefore := previous["volumeAttachments"]
	after := resource["volumeAttachments"]
	normalizedBefore := normalizeForDiff("volumeAttachments", before)
	normalizedAfter := normalizeForDiff("volumeAttachments", after)
	if StableStringify(normalizedBefore) == StableStringify(normalizedAfter) {
		return
	}
	destructive := false
	if beforeObject, ok := before.(map[string]any); ok && hasBefore {
		afterObject, _ := after.(map[string]any)
		for key := range beforeObject {
			if _, ok := afterObject[key]; !ok {
				destructive = true
				break
			}
		}
	}
	severity := "safe"
	if destructive {
		severity = "destructive"
	}
	*changes = append(*changes, update(
		ResourceAddr(resource),
		"volumeAttachments",
		Clone(before),
		Clone(after),
		summaryForField(resource, "volumeAttachments", normalizedBefore, normalizedAfter),
		changedLeafPaths(normalizedBefore, normalizedAfter, "volumeAttachments"),
		severity,
	))
}

type createBranchKind int

const (
	createBranchKeep createBranchKind = iota
	createBranchSet
	createBranchError
)

type createBranch struct {
	kind    createBranchKind
	branch  string
	line    string
	message string
}

func resolveCreateBranch(resource map[string]any, follow *BranchFollow) createBranch {
	source, ok := resource["source"]
	if !ok {
		return createBranch{kind: createBranchKeep}
	}
	repo, ok := githubRepo(source)
	if !ok {
		return createBranch{kind: createBranchKeep}
	}
	if _, pinned := pinnedBranch(source); pinned {
		return createBranch{kind: createBranchKeep}
	}
	if follow.InPREnvironment && follow.PRRepo != "" && asciiEqualFold(follow.PRRepo, repo) {
		if follow.PRBranch != "" {
			return createBranch{
				kind:   createBranchSet,
				branch: follow.PRBranch,
				line:   "branch: " + follow.PRBranch + " (PR branch)",
			}
		}
		return createBranch{kind: createBranchError, message: missingBranchMessage(resource)}
	}
	if branch := follow.DefaultBranches[asciiLower(repo)]; branch != "" {
		return createBranch{
			kind:   createBranchSet,
			branch: branch,
			line:   "branch: " + branch + " (repo default)",
		}
	}
	return createBranch{kind: createBranchError, message: missingBranchMessage(resource)}
}

func missingBranchMessage(resource map[string]any) string {
	return fmt.Sprintf(
		"%s is new and its github() source has no branch. Set branch for the first deploy; it can be removed afterwards.",
		ResourceName(resource),
	)
}

func prBranchPinError(previous, resource map[string]any, prRepo string) (string, bool) {
	desired, ok := resource["source"]
	if !ok {
		return "", false
	}
	pinned, ok := pinnedBranch(desired)
	if !ok {
		return "", false
	}
	repo, ok := githubRepo(desired)
	if !ok {
		return "", false
	}
	// Only the PR's repo is rewritten to the PR branch. Other services keep
	// their own.
	if prRepo == "" || !asciiEqualFold(prRepo, repo) {
		return "", false
	}
	current, ok := pinnedBranch(previous["source"])
	if !ok || pinned == current {
		return "", false
	}
	return fmt.Sprintf(
		"%s pins branch \"%s\" but this PR environment deploys \"%s\". Remove branch to follow the environment.",
		ResourceName(resource), pinned, current,
	), true
}

func githubRepo(source any) (string, bool) {
	object, ok := source.(map[string]any)
	if !ok {
		return "", false
	}
	if kind, ok := object["type"].(string); ok {
		if kind != "github" {
			return "", false
		}
	} else if _, ok := object["image"].(string); ok {
		return "", false
	}
	repo, ok := object["repo"].(string)
	if !ok || repo == "" {
		return "", false
	}
	return repo, true
}

func pinnedBranch(source any) (string, bool) {
	branch, ok := FieldStrOK(source, "branch")
	if !ok || branch == "" {
		return "", false
	}
	return branch, true
}

func sourceOmitsBranch(source any) bool {
	_, isGithub := githubRepo(source)
	_, pinned := pinnedBranch(source)
	return isGithub && !pinned
}

func dropBranchKey(value any) {
	if object, ok := value.(map[string]any); ok {
		delete(object, "branch")
	}
}

func asciiLower(text string) string {
	out := []byte(text)
	for index, char := range out {
		if 'A' <= char && char <= 'Z' {
			out[index] = char + ('a' - 'A')
		}
	}
	return string(out)
}

func asciiEqualFold(left, right string) bool {
	return len(left) == len(right) && asciiLower(left) == asciiLower(right)
}

// NeedsDefaultBranchLookup reports whether DiffGraphsFollowing will need a
// repo default branch: a new service has a branch-less github() source that
// the PR branch does not cover. An empty prBranch or prRepo means none.
func NeedsDefaultBranchLookup(current, desired *Graph, inPREnvironment bool, prBranch, prRepo string) bool {
	current = graphOrEmpty(current)
	desired = graphOrEmpty(desired)
	desiredBy := map[string]map[string]any{}
	for _, resource := range desired.Resources {
		desiredBy[ResourceAddr(resource)] = resource
	}
	currentBy := map[string]bool{}
	for _, resource := range current.Resources {
		currentBy[pairedAddress(resource, desiredBy)] = true
	}
	for _, resource := range desired.Resources {
		if currentBy[ResourceAddr(resource)] {
			continue
		}
		source, ok := resource["source"]
		if !ok {
			continue
		}
		repo, ok := githubRepo(source)
		if !ok {
			continue
		}
		if _, pinned := pinnedBranch(source); pinned {
			continue
		}
		coveredByPR := inPREnvironment && prRepo != "" && asciiEqualFold(prRepo, repo) && prBranch != ""
		if !coveredByPR {
			return true
		}
	}
	return false
}

// PreserveResolvedCreateBranches restores what preview drops when it rewrites
// the change list: the branch chosen for a new service, and the plan line
// that records why, so apply still creates a deploy trigger.
func PreserveResolvedCreateBranches(local, previewed []map[string]any) {
	for _, change := range local {
		line := ""
		for _, detail := range detailsOf(change) {
			if text, ok := detail.(string); ok && strings.HasPrefix(text, "branch: ") {
				line = text
				break
			}
		}
		if line == "" {
			continue
		}
		address, ok := FieldStrOK(change, "address")
		if !ok {
			continue
		}
		var target map[string]any
		for _, item := range previewed {
			itemAddress, addressOK := FieldStrOK(item, "address")
			itemKind, kindOK := FieldStrOK(item, "kind")
			if addressOK && itemAddress == address && kindOK && itemKind == "resource.create" {
				target = item
				break
			}
		}
		if target == nil {
			continue
		}
		if branch, ok := Field(Field(change["resource"], "source"), "branch").(string); ok {
			if source, ok := Field(target["resource"], "source").(map[string]any); ok {
				if _, isString := source["branch"].(string); !isString {
					source["branch"] = branch
				}
			}
		}
		details := detailsOf(target)
		hasLine := false
		for _, detail := range details {
			if text, ok := detail.(string); ok && text == line {
				hasLine = true
				break
			}
		}
		if !hasLine {
			target["details"] = append([]any{line}, details...)
		}
		if summary, ok := FieldStrOK(target, "summary"); !ok || !strings.Contains(summary, line) {
			if summary, ok := FieldStrOK(change, "summary"); ok {
				target["summary"] = summary
			}
		}
	}
}

// detailsOf reads a change's details whether this package built it or it was
// decoded from JSON.
func detailsOf(change map[string]any) []any {
	switch details := change["details"].(type) {
	case []any:
		return append([]any{}, details...)
	case []string:
		return stringsToValues(details)
	default:
		return []any{}
	}
}

func diffTopLevelField(previous, resource map[string]any, fieldName string, changes *[]map[string]any) {
	before := Clone(previous[fieldName])
	after := Clone(resource[fieldName])
	if fieldName == "source" && ResourceType(previous) == "database" && isEquivalentDatabaseSource(previous, after) {
		return
	}
	if fieldName == "source" && ResourceType(resource) == "service" && !sourceSupportsAutoUpdates(resource["source"]) {
		before = withoutAutoUpdates(before)
		after = withoutAutoUpdates(after)
	}
	// Omit the key. `branch: null` clears the branch and deletes the deploy
	// trigger.
	if fieldName == "source" && sourceOmitsBranch(after) {
		dropBranchKey(before)
		dropBranchKey(after)
	}
	if fieldName == "deploy" {
		before, after = stripWriteOnlyRegistryCredentials(before, after)
	}
	normalizedBefore := normalizeForDiff(fieldName, before)
	normalizedAfter := normalizeForDiff(fieldName, after)
	if StableStringify(normalizedBefore) == StableStringify(normalizedAfter) {
		return
	}
	*changes = append(*changes, update(
		ResourceAddr(resource),
		fieldName,
		before,
		after,
		summaryForField(resource, fieldName, normalizedBefore, normalizedAfter),
		changedLeafPaths(normalizedBefore, normalizedAfter, fieldName),
		"safe",
	))
}

func diffServiceDeploy(previous, resource map[string]any, changes *[]map[string]any, diagnostics *[]Diagnostic) {
	desiredDeploy := serviceDeployWithCurrentRegion(previous["deploy"], resource["deploy"])
	previousType, previousHasType := FieldStrOK(previous["source"], "type")
	desiredType, desiredHasType := FieldStrOK(resource["source"], "type")
	switchingAwayFromImage := previousHasType && previousType == "image" && !(desiredHasType && desiredType == "image")
	var before, after any
	if _, hasCredentials := get(previous["deploy"], "registryCredentials"); switchingAwayFromImage && hasCredentials {
		before = withoutRegistryCredentials(previous["deploy"])
		after = Clone(desiredDeploy)
		if after == nil {
			after = map[string]any{}
		}
		after = setIndex(after, "registryCredentials", nil)
	} else {
		before, after = stripWriteOnlyRegistryCredentials(previous["deploy"], desiredDeploy)
	}
	normalizedBefore := normalizeForDiff("deploy", before)
	normalizedAfter := normalizeForDiff("deploy", after)
	if StableStringify(normalizedBefore) == StableStringify(normalizedAfter) {
		return
	}
	details := changedLeafPaths(normalizedBefore, normalizedAfter, "deploy")
	summary := summaryForField(resource, "deploy", normalizedBefore, normalizedAfter)
	if IsFunctionResource(previous) || IsFunctionResource(resource) {
		describeFunctionCodeChange(previous, resource, normalizedBefore, normalizedAfter, &details, &summary, diagnostics)
	}
	*changes = append(*changes, update(
		ResourceAddr(resource),
		"deploy",
		before,
		after,
		summary,
		details,
		"safe",
	))
}

// describeFunctionCodeChange swaps the opaque `startCommand (... → ...)`
// detail for a decoded source diff, since a function's source travels
// base64-encoded inside `deploy.startCommand`, and warns when the authored
// side would drop the code altogether.
func describeFunctionCodeChange(
	previous, resource map[string]any,
	before, after any,
	details *[]string,
	summary *string,
	diagnostics *[]Diagnostic,
) {
	beforeCommand, hasBeforeCommand := FieldStrOK(before, "startCommand")
	afterCommand, hasAfterCommand := FieldStrOK(after, "startCommand")
	if hasBeforeCommand == hasAfterCommand && beforeCommand == afterCommand {
		return
	}
	beforeCode, hasBeforeCode := "", false
	if hasBeforeCommand {
		beforeCode, hasBeforeCode = DecodeFunctionCode(beforeCommand)
	}
	afterCode, hasAfterCode := "", false
	if hasAfterCommand {
		afterCode, hasAfterCode = DecodeFunctionCode(afterCommand)
	}
	if !hasBeforeCode && !hasAfterCode {
		return
	}
	index := slices.IndexFunc(*details, func(detail string) bool {
		return strings.HasPrefix(detail, "deploy.startCommand (")
	})
	if index < 0 {
		return
	}

	var headline string
	var diffLines []string
	switch {
	case hasBeforeCode && hasAfterCode:
		lines, insertions, deletions := SourceCodeDiffLines(beforeCode, afterCode)
		headline = fmt.Sprintf("source code (+%d -%d)", insertions, deletions)
		diffLines = lines
	case hasBeforeCode:
		rest := "removed"
		if hasAfterCommand {
			rest = "replaced by " + formatDiffValue(afterCommand)
		}
		headline = fmt.Sprintf("source code (%d lines, %s)", rustLineCount(beforeCode), rest)
	default:
		rest := "added"
		if hasBeforeCommand {
			rest = "replaces " + formatDiffValue(beforeCommand)
		}
		headline = fmt.Sprintf("source code (%d lines, %s)", rustLineCount(afterCode), rest)
	}
	(*details)[index] = headline

	// Paths first, so the summary reads the same way summaryForField does.
	*summary = updateSummary(resource, *details)

	if hasBeforeCode && !hasAfterCommand {
		*diagnostics = append(*diagnostics, Diagnostic{
			Severity: "warning",
			Path:     "resources." + ResourceAddr(resource) + ".deploy.startCommand",
			Message: fmt.Sprintf(
				"Function %s is declared without code. Applying removes its start command, and with it the source deployed on Railway. Pass `codeFile` (`code_file` in Python) to fn() to keep it, or run `railway config pull` to import the deployed source into `functions/`.",
				ResourceName(previous),
			),
		})
	}

	elided := max(len(diffLines)-maxSourceDiffLines, 0)
	inserted := diffLines[:min(len(diffLines), maxSourceDiffLines)]
	if elided > 0 {
		inserted = append(append([]string{}, inserted...), fmt.Sprintf("... (%d more lines)", elided))
	}
	*details = slices.Insert(*details, index+1, inserted...)
}

func serviceDeployWithCurrentRegion(previous, desired any) any {
	desired = Clone(desired)
	_, hasReplicas := get(desired, "numReplicas")
	_, hasMultiRegion := get(desired, "multiRegionConfig")
	if !hasReplicas || hasMultiRegion {
		return desired
	}
	multi, _ := get(previous, "multiRegionConfig")
	region, ok := singleRegion(multi)
	if !ok {
		return desired
	}
	rest := Clone(desired).(map[string]any)
	replicas := rest["numReplicas"]
	delete(rest, "numReplicas")
	rest["multiRegionConfig"] = map[string]any{region: map[string]any{"numReplicas": replicas}}
	return rest
}

func diffVolumeConfig(previous, resource map[string]any, changes *[]map[string]any) {
	desiredConfig := resource["config"]
	previousConfig := dropUnauthoredPlatformFields(previous["config"], desiredConfig, []string{"alerts", "allowOnlineResize"})
	before := normalizeForDiff("config", previousConfig)
	after := normalizeForDiff("config", desiredConfig)
	if StableStringify(before) == StableStringify(after) {
		return
	}
	previousSize, hasPreviousSize := asI64(Field(previousConfig, "sizeMB"))
	desiredSize, hasDesiredSize := asI64(Field(desiredConfig, "sizeMB"))
	previousRegion, previousHasRegion := get(previousConfig, "region")
	desiredRegion, desiredHasRegion := get(desiredConfig, "region")
	regionChanged := previousHasRegion != desiredHasRegion || (previousHasRegion && !valueEqual(previousRegion, desiredRegion))
	severity := "safe"
	if regionChanged || (hasPreviousSize && hasDesiredSize && desiredSize < previousSize) {
		severity = "destructive"
	}
	summary := summaryForField(resource, "config", before, after)
	if hasPreviousSize && hasDesiredSize && desiredSize > previousSize {
		summary = fmt.Sprintf("Resize volume %s from %dMB to %dMB", ResourceName(resource), previousSize, desiredSize)
	}
	*changes = append(*changes, update(
		ResourceAddr(resource),
		"config",
		previousConfig,
		Clone(desiredConfig),
		summary,
		changedLeafPaths(before, after, "config"),
		severity,
	))
}

func diffDatabaseDeploy(previous, resource map[string]any, changes *[]map[string]any) {
	previousDeploy, resourceDeploy := stripWriteOnlyRegistryCredentials(previous["deploy"], resource["deploy"])
	previousDeploy = dropPlatformStartCommand(previousDeploy, resourceDeploy)
	previousRegion, hasPreviousRegion := databaseRegion(previous)
	desiredRegion, hasDesiredRegion := databaseRegion(resource)
	if hasDesiredRegion && (!hasPreviousRegion || desiredRegion != previousRegion) {
		*changes = append(*changes, update(
			ResourceAddr(resource),
			"deploy",
			previousDeploy,
			resourceDeploy,
			fmt.Sprintf("Move database %s to %s", ResourceName(resource), desiredRegion),
			changedLeafPaths(normalizeDatabaseDeploy(previousDeploy), normalizeDatabaseDeploy(resourceDeploy), "deploy"),
			"destructive",
		))
		return
	}
	before := normalizeDatabaseDeploy(previousDeploy)
	after := normalizeDatabaseDeploy(resourceDeploy)
	if StableStringify(before) == StableStringify(after) {
		return
	}
	*changes = append(*changes, update(
		ResourceAddr(resource),
		"deploy",
		previousDeploy,
		resourceDeploy,
		summaryForField(resource, "deploy", before, after),
		changedLeafPaths(before, after, "deploy"),
		"safe",
	))
}

func dropUnauthoredPlatformFields(previousValue, desiredValue any, fields []string) any {
	object, ok := previousValue.(map[string]any)
	if !ok {
		return Clone(previousValue)
	}
	desired, _ := desiredValue.(map[string]any)
	copied := CloneMap(object)
	changed := false
	for _, fieldName := range fields {
		if _, present := copied[fieldName]; !present {
			continue
		}
		if value, authored := desired[fieldName]; !authored || value == nil {
			delete(copied, fieldName)
			changed = true
		}
	}
	if !changed {
		return Clone(previousValue)
	}
	if len(copied) == 0 {
		return nil
	}
	return copied
}

func dropPlatformStartCommand(previousDeploy, resourceDeploy any) any {
	return dropUnauthoredPlatformFields(previousDeploy, resourceDeploy, []string{"startCommand"})
}

func isMaskedRegistryCredentials(value any, present bool) bool {
	if !present {
		return false
	}
	if value == nil {
		return true
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	username, _ := object["username"].(string)
	password, _ := object["password"].(string)
	return username == maskedCredentialValue || password == maskedCredentialValue
}

func stripWriteOnlyRegistryCredentials(before, after any) (any, any) {
	beforeCredentials, hasBeforeCredentials := get(before, "registryCredentials")
	afterCredentials, hasAfterCredentials := get(after, "registryCredentials")
	desiredCredentials, hasDesiredCredentials := realCredentialFields(afterCredentials)
	withCredentials := func(value any, credentials any, hasCredentials bool) any {
		object, ok := value.(map[string]any)
		if !ok {
			return Clone(value)
		}
		copied := CloneMap(object)
		if hasCredentials {
			copied["registryCredentials"] = Clone(credentials)
		} else {
			delete(copied, "registryCredentials")
		}
		if len(copied) == 0 {
			return nil
		}
		return copied
	}
	desiredSide := Clone(after)
	if hasAfterCredentials {
		desiredSide = withCredentials(after, desiredCredentials, hasDesiredCredentials)
	}
	if hasDesiredCredentials && isMaskedRegistryCredentials(afterCredentials, hasAfterCredentials) {
		return withCredentials(before, nil, false), desiredSide
	}
	if hasDesiredCredentials && !isMaskedRegistryCredentials(beforeCredentials, hasBeforeCredentials) {
		return Clone(before), desiredSide
	}
	strippedBefore := Clone(before)
	if hasBeforeCredentials {
		strippedBefore = withCredentials(before, nil, false)
	}
	strippedAfter := desiredSide
	if hasDesiredCredentials {
		strippedAfter = withCredentials(desiredSide, nil, false)
	}
	return strippedBefore, strippedAfter
}

func sourceSupportsAutoUpdates(source any) bool {
	if kind, ok := FieldStrOK(source, "type"); !ok || kind != "image" {
		return false
	}
	image, ok := FieldStrOK(source, "image")
	if !ok {
		return false
	}
	image = strings.ToLower(strings.TrimSpace(image))
	if image == "" {
		return false
	}
	if !strings.Contains(image, "/") {
		return true
	}
	registry, _, _ := strings.Cut(image, "/")
	return (!strings.Contains(registry, ".") && !strings.Contains(registry, ":") && registry != "localhost") ||
		registry == "docker.io" ||
		registry == "ghcr.io"
}

func withoutKey(value any, key string) any {
	object, ok := value.(map[string]any)
	if !ok {
		return Clone(value)
	}
	copied := CloneMap(object)
	delete(copied, key)
	if len(copied) == 0 {
		return nil
	}
	return copied
}

func withoutAutoUpdates(source any) any { return withoutKey(source, "autoUpdates") }

func withoutRegistryCredentials(deploy any) any { return withoutKey(deploy, "registryCredentials") }

func realCredentialFields(credentials any) (map[string]any, bool) {
	object, ok := credentials.(map[string]any)
	if !ok {
		return nil, false
	}
	entries := map[string]any{}
	for key, value := range object {
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && text == maskedCredentialValue {
			continue
		}
		entries[key] = Clone(value)
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

func normalizeDatabaseDeploy(value any) any {
	normalized := normalizeForDiff("deploy", value)
	if object, ok := normalized.(map[string]any); ok {
		delete(object, "requiredMountPath")
		if len(object) == 0 {
			return nil
		}
	}
	return normalized
}

func summaryForField(resource map[string]any, fieldName string, before, after any) string {
	if details := changedLeafPaths(before, after, fieldName); len(details) > 0 {
		return updateSummary(resource, details)
	}
	return fmt.Sprintf("Update %s %s", ResourceName(resource), fieldName)
}

func updateSummary(resource map[string]any, details []string) string {
	shown := make([]string, 0, 3)
	for _, detail := range details[:min(len(details), 3)] {
		shown = append(shown, detailPathForSummary(detail))
	}
	more := ""
	if remaining := len(details) - 3; remaining > 0 {
		more = fmt.Sprintf(" and %d more", remaining)
	}
	return fmt.Sprintf("Update %s %s%s", ResourceName(resource), strings.Join(shown, ", "), more)
}

func detailPathForSummary(detail string) string {
	path, _, _ := strings.Cut(detail, " (")
	return path
}

func changedLeafPaths(before, after any, prefix string) []string {
	beforeFlat := flattenForDiff(before, "")
	afterFlat := flattenForDiff(after, "")
	keySet := map[string]bool{}
	for key := range beforeFlat {
		keySet[key] = true
	}
	for key := range afterFlat {
		keySet[key] = true
	}
	var changed []string
	for _, key := range SortedKeys(keySet) {
		if StableStringify(beforeFlat[key]) != StableStringify(afterFlat[key]) {
			changed = append(changed, key)
		}
	}
	var out []string
	for _, key := range changed {
		if key == "" && len(changed) != 1 {
			continue
		}
		if key != "" && slices.ContainsFunc(changed, func(other string) bool {
			return other != key && strings.HasPrefix(other, key+".")
		}) {
			continue
		}
		path := prefix
		if key != "" {
			path = prefix + "." + key
		}
		if strings.Contains(path, "registryCredentials") {
			out = append(out, fmt.Sprintf("%s (%s → %s)", friendlyPath(path), redactedVariableValue, redactedVariableValue))
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s (%s → %s)",
			friendlyPath(path),
			formatDiffValue(beforeFlat[key]),
			formatDiffValue(afterFlat[key]),
		))
	}
	return out
}

func flattenForDiff(value any, prefix string) map[string]any {
	out := map[string]any{}
	object, ok := value.(map[string]any)
	if !ok || len(object) == 0 {
		out[prefix] = value
		return out
	}
	for _, key := range SortedKeys(object) {
		next := key
		if prefix != "" {
			next = prefix + "." + key
		}
		for flatKey, child := range flattenForDiff(object[key], next) {
			out[flatKey] = child
		}
	}
	return out
}

func friendlyPath(path string) string {
	if match := friendlyRegionPath.FindStringSubmatch(path); match != nil {
		return "regions." + match[1]
	}
	return path
}

func formatDiffValue(value any) string {
	return StableStringify(value)
}

func update(address, fieldName string, before, after any, summary string, details []string, severity string) map[string]any {
	change := map[string]any{
		"kind":         "resource.update",
		"address":      address,
		"field":        fieldName,
		"before":       before,
		"after":        after,
		"path":         "resources." + address + "." + fieldName,
		"summary":      summary,
		"severity":     severity,
		"deployEffect": deployEffectForUpdate(fieldName, details),
	}
	if len(details) > 0 {
		change["details"] = stringsToValues(details)
	}
	return change
}

// deployEffectForUpdate mirrors backboard's changesThatSkipRedeploy.
// Auto-instrumentation reaches running containers live; only
// `tracing.enabled` needs a deploy, for the OpenTelemetry variables. CDN
// settings live on the edge and never reach the container.
func deployEffectForUpdate(fieldName string, details []string) string {
	if fieldName == "config" || fieldName == "groupId" || fieldName == "cdn" {
		return "none"
	}
	if fieldName == "tracing" && len(details) > 0 && !slices.ContainsFunc(details, func(detail string) bool {
		return !strings.HasPrefix(detail, "tracing.autoInstrumentation")
	}) {
		return "none"
	}
	return "deploy"
}

func formatVariableDiffValue(value any, present bool, resourcesByAddress map[string]map[string]any, revealValues bool) string {
	if !present {
		return "unset"
	}
	kind, hasKind := FieldStrOK(value, "type")
	switch {
	case hasKind && kind == "preserve":
		return "preserve()"
	case hasKind && kind == "reference":
		return referenceName(value, resourcesByAddress) + "." + FieldStr(value, "output")
	case hasKind && kind == "sharedReference":
		return "shared." + FieldStr(value, "name")
	case !revealValues:
		return redactedVariableValue
	case hasKind && kind == "literal":
		return formatDiffValue(Field(value, "value"))
	default:
		return formatDiffValue(normalizeVariableForDiff(value, true, resourcesByAddress))
	}
}

// referenceName is the name of the resource a reference variable points at:
// the declared resource's name, else the second segment of its address.
func referenceName(value any, resourcesByAddress map[string]map[string]any) string {
	resource := FieldStr(value, "resource")
	if target, ok := resourcesByAddress[resource]; ok {
		return ResourceName(target)
	}
	if parts := strings.Split(resource, "."); len(parts) > 1 {
		return parts[1]
	}
	return resource
}

func normalizeVariableForDiff(value any, present bool, resourcesByAddress map[string]map[string]any) any {
	if !present {
		return nil
	}
	kind, hasKind := FieldStrOK(value, "type")
	if hasKind && kind == "sharedReference" {
		return map[string]any{
			"type":  "literal",
			"value": "${{shared." + FieldStr(value, "name") + "}}",
		}
	}
	if !hasKind || kind != "reference" {
		return Clone(value)
	}
	return map[string]any{
		"type":  "literal",
		"value": "${{" + referenceName(value, resourcesByAddress) + "." + FieldStr(value, "output") + "}}",
	}
}

func isManagedByRepoConfig(resource map[string]any) bool {
	switch ResourceType(resource) {
	case "service", "database":
	default:
		return false
	}
	path, ok := FieldStrOK(resource, "configFile")
	return ok && repoConfigFile.MatchString(path)
}

func bucketRegion(resource map[string]any) (string, bool) {
	return FieldStrOK(resource["config"], "region")
}

func diagnoseNewBucketRegion(resource map[string]any, diagnostics *[]Diagnostic) {
	if ResourceType(resource) != "bucket" {
		return
	}
	region, present := get(resource["config"], "region")
	if code, ok := region.(string); ok && slices.Contains(bucketRegions, code) {
		return
	}
	validRegions := strings.Join(bucketRegions, ", ")
	name := ResourceName(resource)
	message := fmt.Sprintf("Bucket %s has an invalid region %s. Valid object-storage regions are %s.", name, StableStringify(region), validRegions)
	if !present || region == nil {
		message = fmt.Sprintf("Bucket %s is missing a region. Set one of: %s.", name, validRegions)
	}
	*diagnostics = append(*diagnostics, Diagnostic{
		Severity: "error",
		Path:     "resources." + ResourceAddr(resource) + ".config.region",
		Message:  message,
	})
}

func isEquivalentDatabaseSource(previous map[string]any, after any) bool {
	if ResourceType(previous) != "database" || !isObject(after) {
		return false
	}
	kind, ok := FieldStrOK(after, "type")
	return ok && kind == "image" &&
		normalizeImageTag(FieldStr(after, "image")) == normalizeImageTag(FieldStr(previous, "image"))
}

func isPreservedVariable(value any) bool {
	kind, ok := FieldStrOK(value, "type")
	return ok && kind == "preserve"
}

func isUnknownCurrentVariable(value any, present bool) bool {
	if !present {
		return false
	}
	kind, ok := FieldStrOK(value, "type")
	if !ok {
		return false
	}
	literal, hasLiteral := FieldStrOK(value, "value")
	return kind == "preserve" || (kind == "literal" && hasLiteral && literal == "")
}

// cdnDefault is what the edge applies for every CDN knob the block leaves
// out. Mirrors SERVICE_CDN_DEFAULTS in @railway/models.
func cdnDefault(key string) (any, bool) {
	switch key {
	case "defaultTtlSeconds":
		return float64(7200), true
	case "htmlCaching":
		return "auto", true
	case "staleWhileRevalidate":
		return true, true
	case "purgeOnDeploy":
		return "html", true
	default:
		return nil, false
	}
}

// NormalizeCdn reduces a cdn block to what Railway serialises: the block only
// while caching is on, and only the knobs that differ from the edge defaults.
// So `enabled: false`, null and no block are all the same thing, and
// `{ enabled: true }` is a service caching with the defaults.
func NormalizeCdn(value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	normalized := normalizeCdnObject(object)
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeCdnObject(object map[string]any) map[string]any {
	out := map[string]any{}
	if enabled, ok := object["enabled"].(bool); !ok || !enabled {
		return out
	}
	for key, child := range object {
		if child == nil {
			continue
		}
		if fallback, ok := cdnDefault(key); ok && valueEqual(fallback, child) {
			continue
		}
		out[key] = Clone(child)
	}
	return out
}

func normalizeForDiff(fieldName string, value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		return Clone(value)
	}
	copied := CloneMap(object)
	equals := func(key string, want any) bool {
		got, ok := copied[key]
		return ok && valueEqual(got, want)
	}
	switch fieldName {
	case "source":
		if equals("checkSuites", false) {
			delete(copied, "checkSuites")
		}
		if equals("branch", "main") {
			delete(copied, "branch")
		}
		delete(copied, "commitSha")
		delete(copied, "upstreamUrl")
		if equals("rootDirectory", "") {
			delete(copied, "rootDirectory")
		}
		if image, ok := copied["image"].(string); ok {
			copied["image"] = normalizeImageTag(image)
		}
	case "build":
		if equals("builder", "RAILPACK") {
			delete(copied, "builder")
		}
		if equals("buildEnvironment", "V3") {
			delete(copied, "buildEnvironment")
		}
		if equals("buildCommand", "") {
			delete(copied, "buildCommand")
		}
		if equals("dockerfilePath", "Dockerfile") {
			delete(copied, "dockerfilePath")
		}
	case "tracing":
		// Railway serialises only the switches that are on, so a false or null
		// switch is the same as no switch, and no switches is no block.
		for key, child := range copied {
			if on, ok := child.(bool); !ok || !on {
				delete(copied, key)
			}
		}
	case "cdn":
		copied = normalizeCdnObject(copied)
	case "networking":
		// `tcp: []` compiles to `tcpProxies: {}`; Railway serializes a service
		// with no proxy as no `tcpProxies` at all. Both mean the same thing.
		for key, child := range copied {
			if inner, isMap := child.(map[string]any); child == nil || (isMap && len(inner) == 0) {
				delete(copied, key)
			}
		}
	case "deploy":
		if equals("useLegacyStacker", false) {
			delete(copied, "useLegacyStacker")
		}
		if equals("ipv6EgressEnabled", false) {
			delete(copied, "ipv6EgressEnabled")
		}
		if equals("runtime", "V2") {
			delete(copied, "runtime")
		}
		if multi, ok := copied["multiRegionConfig"]; ok {
			delete(copied, "multiRegionConfig")
			normalized := normalizeMultiRegionConfig(multi)
			if inner, isMap := normalized.(map[string]any); isMap && len(inner) > 0 && !isDefaultMultiRegionConfig(normalized) {
				copied["multiRegionConfig"] = normalized
			}
		}
	}
	if len(copied) == 0 {
		return nil
	}
	return copied
}

func normalizeMultiRegionConfig(value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		return Clone(value)
	}
	out := map[string]any{}
	for key, config := range object {
		if configObject, isMap := config.(map[string]any); isMap {
			if replicas, ok := configObject["numReplicas"]; !ok || replicas == nil {
				continue
			}
		}
		out[key] = Clone(config)
	}
	return out
}

func isDefaultMultiRegionConfig(value any) bool {
	object, ok := value.(map[string]any)
	if !ok || len(object) != 1 {
		return false
	}
	var config map[string]any
	for _, child := range object {
		config, ok = child.(map[string]any)
	}
	if !ok {
		return false
	}
	for key, child := range config {
		if !(key == "numReplicas" && valueEqual(child, float64(1))) && !(key == "stackerAssignment" && child == nil) {
			return false
		}
	}
	return true
}

func normalizeImageTag(image string) string {
	if match := templateImageTag.FindStringSubmatch(image); match != nil {
		return match[1] + ":" + match[2]
	}
	return image
}
