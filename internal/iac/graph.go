// Ported from railwayapp/cli src/iac/graph.rs (MIT, Copyright (c) Railway Corp.)

// Package iac is a Go port of the Railway CLI's Infrastructure as Code
// engine (railwayapp/cli src/iac). It converts between the project
// definition the railway-go-sdk emits, the RailwayGraph the engine diffs, and
// the live EnvironmentConfig Railway stores, and builds the ChangeSet that
// backboard's environmentPreviewChangeSet and environmentApplyChangeSet
// mutations accept.
//
// Values are the decoded form of JSON: map[string]any, []any, string,
// float64, bool and nil. Every walk over a map iterates keys in sorted order
// so the output matches the CLI, whose serde_json maps are BTreeMaps.
package iac

import (
	"fmt"
	"strings"
)

const (
	GraphVersion     = 1
	ChangeSetVersion = 1
)

type Graph struct {
	Version      int               `json:"version"`
	Project      ProjectNode       `json:"project"`
	Environments []EnvironmentNode `json:"environments"`
	Resources    []map[string]any  `json:"resources"`
	Edges        []Edge            `json:"edges"`
	Variables    VariablePolicy    `json:"variables,omitzero"`
}

type ProjectNode struct {
	Name string `json:"name"`
}

type EnvironmentNode struct {
	Name string `json:"name"`
}

type Edge struct {
	From string  `json:"from"`
	To   string  `json:"to"`
	Kind string  `json:"type"`
	Key  *string `json:"key,omitempty"`
}

// VariablePolicy says which variable keys IaC owns. The zero value is not the
// default; use DefaultVariablePolicy.
type VariablePolicy struct {
	Managed bool     `json:"managed"`
	Ignore  []string `json:"ignore"`
	Default bool     `json:"default"`
	Error   *string  `json:"error,omitempty"`
}

func DefaultVariablePolicy() VariablePolicy {
	return VariablePolicy{Managed: true, Ignore: []string{}, Default: true}
}

func (p VariablePolicy) IsUnspecified() bool {
	return p.Default && p.Error == nil && p.Managed && len(p.Ignore) == 0
}

func (p VariablePolicy) IsZero() bool { return p.IsUnspecified() }

// VariablePolicyReportResult is the plan/apply summary of the policy against
// the live environment.
type VariablePolicyReportResult struct {
	Managed      bool           `json:"managed"`
	Ignore       []string       `json:"ignore"`
	Default      bool           `json:"default"`
	IgnoredCount int            `json:"ignoredCount"`
	ByPattern    map[string]int `json:"byPattern"`
}

// FormatVariablePolicyLine renders the plan header line. railwayVariables is
// every variable key on the current graph; database template variables are
// not on that graph.
func FormatVariablePolicyLine(report VariablePolicyReportResult, railwayVariables int) string {
	if !report.Managed {
		return fmt.Sprintf("variables: unmanaged · %d on Railway not managed", railwayVariables)
	}
	if report.IgnoredCount > 0 {
		var listed []string
		for _, pattern := range report.Ignore {
			if count := report.ByPattern[pattern]; count > 0 {
				listed = append(listed, fmt.Sprintf("%s %d", pattern, count))
			}
		}
		return fmt.Sprintf("variables: managed · %d ignored on Railway (%s)", report.IgnoredCount, strings.Join(listed, ", "))
	}
	if report.Default {
		return "variables: managed (default)"
	}
	return "variables: managed"
}

type Diagnostic struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type ChangeSetTelemetry struct {
	Language string `json:"language"`
}

type ChangeSet struct {
	Version     int                 `json:"version"`
	Changes     []map[string]any    `json:"changes"`
	Diagnostics []Diagnostic        `json:"diagnostics"`
	Partial     *string             `json:"partial,omitempty"`
	Declared    []string            `json:"declared,omitempty"`
	Telemetry   *ChangeSetTelemetry `json:"telemetry,omitempty"`
}

func ResourceAddress(resourceType, name string) string {
	return resourceType + "." + name
}

func ResourceType(resource map[string]any) string { return FieldStr(resource, "type") }

func ResourceName(resource map[string]any) string { return FieldStr(resource, "name") }

func ResourceAddr(resource map[string]any) string {
	if address, ok := resource["address"].(string); ok {
		return address
	}
	return ResourceAddress(ResourceType(resource), ResourceName(resource))
}

func ValidateGraph(graph Graph) []string {
	var errors []string
	if graph.Version != GraphVersion {
		errors = append(errors, fmt.Sprintf("Unsupported graph version: %d", graph.Version))
	}
	addresses := map[string]bool{}
	for _, resource := range graph.Resources {
		address := ResourceAddr(resource)
		if addresses[address] {
			errors = append(errors, "Duplicate resource address: "+address)
		}
		addresses[address] = true
	}
	for _, edge := range graph.Edges {
		if !addresses[edge.From] {
			errors = append(errors, "Edge references missing source: "+edge.From)
		}
		if !addresses[edge.To] {
			errors = append(errors, "Edge references missing target: "+edge.To)
		}
	}
	return errors
}

// FindResource returns the resource at address, or nil.
func (g Graph) FindResource(address string) map[string]any {
	for _, resource := range g.Resources {
		if ResourceAddr(resource) == address {
			return resource
		}
	}
	return nil
}
