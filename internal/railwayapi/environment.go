package railwayapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const environmentFields = `id name projectId isEphemeral meta { branch prNumber prRepo } config(decryptVariables: $decryptVariables) configEtag canvasGroupRefs`

// sourceEnvironmentQuery is separate from the config query because a project
// token scoped to one environment may not read the environment it was forked
// from, and Railway fails the whole query when one field is refused.
const sourceEnvironmentQuery = `query IacSourceEnvironment($environmentId: String!) {
  environment(id: $environmentId) { sourceEnvironment { name } }
}`

const environmentQueryWithPartials = `query IacEnvironmentConfig($environmentId: String!, $decryptVariables: Boolean) {
  environment(id: $environmentId) { ` + environmentFields + ` iacPartials }
}`

const environmentQuery = `query IacEnvironmentConfig($environmentId: String!, $decryptVariables: Boolean) {
  environment(id: $environmentId) { ` + environmentFields + ` }
}`

// EnvironmentMeta holds the branch and pull request an environment tracks.
type EnvironmentMeta struct {
	Branch   string `json:"branch"`
	PRNumber *int64 `json:"prNumber"`
	PRRepo   string `json:"prRepo"`
}

// Environment is an environment's IaC-relevant state.
type Environment struct {
	ID                    string
	Name                  string
	ProjectID             string
	IsEphemeral           bool
	Meta                  EnvironmentMeta
	SourceEnvironmentName string
	Config                map[string]any
	ConfigEtag            string
	CanvasGroupRefs       map[string]any
	// IACPartials maps a resource key to the partial that owns it. It is nil
	// when the API does not expose iacPartials.
	IACPartials map[string]string
}

// PRContext describes the pull request a PR environment was created for.
type PRContext struct {
	Number int64
	Branch string
	Base   string
}

// PRContext returns the environment's pull request, or nil unless the
// environment is ephemeral and carries a PR number.
func (e *Environment) PRContext() *PRContext {
	if !e.IsEphemeral || e.Meta.PRNumber == nil {
		return nil
	}
	return &PRContext{Number: *e.Meta.PRNumber, Branch: e.Meta.Branch, Base: e.SourceEnvironmentName}
}

type environmentWire struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	ProjectID       string           `json:"projectId"`
	IsEphemeral     bool             `json:"isEphemeral"`
	Meta            *EnvironmentMeta `json:"meta"`
	Config          map[string]any   `json:"config"`
	ConfigEtag      string           `json:"configEtag"`
	CanvasGroupRefs map[string]any   `json:"canvasGroupRefs"`
	IACPartials     map[string]any   `json:"iacPartials"`
}

// FetchEnvironment reads an environment's config, etag and ownership. When
// the API rejects the iacPartials field, it retries without it. For a pull
// request environment it also reads the environment it was forked from, which
// stays empty when the credential may not see that environment.
func (c *Client) FetchEnvironment(ctx context.Context, environmentID string, decryptVariables bool) (*Environment, error) {
	vars := map[string]any{"environmentId": environmentID, "decryptVariables": decryptVariables}
	var data struct {
		Environment environmentWire `json:"environment"`
	}
	err := c.Do(ctx, environmentQueryWithPartials, vars, &data)
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) && strings.Contains(gqlErr.Error(), "iacPartials") {
		err = c.Do(ctx, environmentQuery, vars, &data)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch environment %s: %w", environmentID, err)
	}

	w := data.Environment
	env := &Environment{
		ID:              w.ID,
		Name:            w.Name,
		ProjectID:       w.ProjectID,
		IsEphemeral:     w.IsEphemeral,
		Config:          w.Config,
		ConfigEtag:      w.ConfigEtag,
		CanvasGroupRefs: w.CanvasGroupRefs,
	}
	if w.Meta != nil {
		env.Meta = *w.Meta
	}
	for key, value := range w.IACPartials {
		owner, ok := value.(string)
		if !ok {
			continue
		}
		if env.IACPartials == nil {
			env.IACPartials = map[string]string{}
		}
		env.IACPartials[key] = owner
	}
	if env.IsEphemeral && env.Meta.PRNumber != nil {
		name, err := c.sourceEnvironmentName(ctx, environmentID)
		if err != nil {
			return nil, fmt.Errorf("fetch source environment of %s: %w", environmentID, err)
		}
		env.SourceEnvironmentName = name
	}
	return env, nil
}

func (c *Client) sourceEnvironmentName(ctx context.Context, environmentID string) (string, error) {
	var data struct {
		Environment struct {
			SourceEnvironment *struct {
				Name string `json:"name"`
			} `json:"sourceEnvironment"`
		} `json:"environment"`
	}
	err := c.Do(ctx, sourceEnvironmentQuery, map[string]any{"environmentId": environmentID}, &data)
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) && strings.Contains(strings.ToLower(gqlErr.Error()), "not authorized") {
		return "", nil
	}
	if err != nil || data.Environment.SourceEnvironment == nil {
		return "", err
	}
	return data.Environment.SourceEnvironment.Name, nil
}
