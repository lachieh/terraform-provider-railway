package railwayapi

import (
	"context"
	"fmt"
)

const projectQuery = `query IacProject($id: String!) { project(id: $id) { id name workspaceId } }`

const projectEnvironmentsQuery = `query IacProjectEnvironments` + pageArgs + ` {
  project(id: $projectId) { items: environments(first: $first, after: $after) { edges { node { id name isEphemeral } } ` + pageSelection + ` } }
}`

const projectTokenQuery = `query IacProjectToken { projectToken { projectId environmentId } }`

const volumeDeleteMutation = `mutation IacVolumeDelete($volumeId: String!) { volumeDelete(volumeId: $volumeId) }`

// Project is a Railway project.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	WorkspaceID string `json:"workspaceId"`
}

// EnvironmentSummary is one environment in a project listing.
type EnvironmentSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IsEphemeral bool   `json:"isEphemeral"`
}

// ProjectTokenScope is the project and environment a project token grants.
type ProjectTokenScope struct {
	ProjectID     string `json:"projectId"`
	EnvironmentID string `json:"environmentId"`
}

// Project reads a project by ID.
func (c *Client) Project(ctx context.Context, id string) (*Project, error) {
	var data struct {
		Project Project `json:"project"`
	}
	if err := c.Do(ctx, projectQuery, map[string]any{"id": id}, &data); err != nil {
		return nil, fmt.Errorf("read project %s: %w", id, err)
	}
	return &data.Project, nil
}

// Environments lists every environment in a project, including ephemeral ones.
func (c *Client) Environments(ctx context.Context, projectID string) ([]EnvironmentSummary, error) {
	envs, err := projectConnection[EnvironmentSummary](ctx, c, projectEnvironmentsQuery, map[string]any{"projectId": projectID})
	if err != nil {
		return nil, fmt.Errorf("list environments of project %s: %w", projectID, err)
	}
	return envs, nil
}

// ProjectToken returns the scope of the client's project token. It fails for
// clients authenticated with an account or workspace token.
func (c *Client) ProjectToken(ctx context.Context) (*ProjectTokenScope, error) {
	var data struct {
		ProjectToken ProjectTokenScope `json:"projectToken"`
	}
	if err := c.Do(ctx, projectTokenQuery, nil, &data); err != nil {
		return nil, fmt.Errorf("read project token scope: %w", err)
	}
	return &data.ProjectToken, nil
}

// VolumeDelete deletes a volume.
func (c *Client) VolumeDelete(ctx context.Context, volumeID string) error {
	var data struct {
		VolumeDelete bool `json:"volumeDelete"`
	}
	if err := c.Do(ctx, volumeDeleteMutation, map[string]any{"volumeId": volumeID}, &data); err != nil {
		return fmt.Errorf("delete volume %s: %w", volumeID, err)
	}
	if !data.VolumeDelete {
		return fmt.Errorf("delete volume %s: railway API reported the volume was not deleted", volumeID)
	}
	return nil
}
