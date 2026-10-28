package provider

import (
	"encoding/json"
	"fmt"

	railway "github.com/railwayapp/railway-go-sdk"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/iac"
)

// graphNode evaluates one SDK resource the way the Railway CLI evaluates a
// railway.go file: the SDK builds the project definition, which round-trips
// through JSON, is normalized, and compiles to a graph. It returns the graph
// node at address. The SDK validates by panicking; the panic becomes an
// error.
func graphNode(address string, build func() any) (node map[string]any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	project := railway.ProjectNamed("terraform", []any{build()})

	raw, err := json.Marshal(project.Graph())
	if err != nil {
		return nil, err
	}
	var definition map[string]any
	if err := json.Unmarshal(raw, &definition); err != nil {
		return nil, err
	}
	definition = iac.NormalizeProject(definition)
	if diagnostics := iac.UnknownFieldDiagnostics(definition); len(diagnostics) > 0 {
		if err := errorDiagnostics(diagnostics); err != nil {
			return nil, err
		}
	}
	graph := iac.ProjectDefinitionToGraph(definition)
	node = graph.FindResource(address)
	if node == nil {
		return nil, fmt.Errorf("the Railway SDK did not produce %s", address)
	}
	return node, nil
}

// variableEnv builds the SDK env map: literal values, preserved keys, and
// references to shared variables and other resources' outputs.
func variableEnv(literals map[string]string, preserve []string, shared map[string]string, references map[string]referenceModel) map[string]any {
	env := map[string]any{}
	for key, value := range literals {
		env[key] = value
	}
	for _, key := range preserve {
		env[key] = railway.Preserve()
	}
	for key, name := range shared {
		env[key] = railway.Shared(name)
	}
	for key, reference := range references {
		env[key] = map[string]any{"type": "reference", "resource": reference.Resource, "output": reference.Output}
	}
	return env
}

// putString, putInt and putBool set a key only for a value the configuration
// sets, so an omitted attribute stays out of the node, as in a railway.go file.
func putString(out map[string]any, key string, value *string) {
	if value != nil {
		out[key] = *value
	}
}

func putInt(out map[string]any, key string, value *int64) {
	if value != nil {
		out[key] = int(*value)
	}
}

func putBool(out map[string]any, key string, value *bool) {
	if value != nil {
		out[key] = *value
	}
}

func putFloat(out map[string]any, key string, value *float64) {
	if value != nil {
		out[key] = *value
	}
}
