package railwayapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const projectDetailsQuery = `query IacProjectDetails($id: String!) {
  project(id: $id) { id name description workspaceId isPublic prDeploys deletedAt }
}`

const projectCreateMutation = `mutation IacProjectCreate($input: ProjectCreateInput!) {
  projectCreate(input: $input) { id name description workspaceId isPublic prDeploys deletedAt }
}`

const projectUpdateMutation = `mutation IacProjectUpdate($id: String!, $input: ProjectUpdateInput!) {
  projectUpdate(id: $id, input: $input) { id name description workspaceId isPublic prDeploys deletedAt }
}`

const projectDeleteMutation = `mutation IacProjectDelete($id: String!) { projectDelete(id: $id) }`

const environmentDetailsQuery = `query IacEnvironmentDetails($id: String!) {
  environment(id: $id) { id name projectId isEphemeral deletedAt }
}`

const environmentCreateMutation = `mutation IacEnvironmentCreate($input: EnvironmentCreateInput!) {
  environmentCreate(input: $input) { id name projectId isEphemeral deletedAt }
}`

const environmentRenameMutation = `mutation IacEnvironmentRename($id: String!, $input: EnvironmentRenameInput!) {
  environmentRename(id: $id, input: $input) { id name projectId isEphemeral deletedAt }
}`

const environmentDeleteMutation = `mutation IacEnvironmentDelete($id: String!) { environmentDelete(id: $id) }`

// ProjectDetails is a project with the settings railway_project manages.
type ProjectDetails struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	WorkspaceID string  `json:"workspaceId"`
	IsPublic    bool    `json:"isPublic"`
	PRDeploys   bool    `json:"prDeploys"`
	DeletedAt   *string `json:"deletedAt"`
}

// ProjectInput is what projectCreate and projectUpdate set. A nil field is
// left out of the request.
type ProjectInput struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	IsPublic    *bool   `json:"isPublic,omitempty"`
	PRDeploys   *bool   `json:"prDeploys,omitempty"`
	// WorkspaceID and DefaultEnvironmentName apply only to projectCreate.
	WorkspaceID            *string `json:"workspaceId,omitempty"`
	DefaultEnvironmentName *string `json:"defaultEnvironmentName,omitempty"`
}

// EnvironmentDetails is one environment, read by ID.
type EnvironmentDetails struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	ProjectID   string  `json:"projectId"`
	IsEphemeral bool    `json:"isEphemeral"`
	DeletedAt   *string `json:"deletedAt"`
}

// ErrNotFound reports a project or environment Railway does not have, or
// has deleted.
var ErrNotFound = errors.New("not found")

// notFound turns Railway's answer for a missing object into ErrNotFound.
func notFound(err error) error {
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) {
		for _, item := range gqlErr.Errors {
			if item.Code() == "NOT_FOUND" || strings.Contains(strings.ToLower(item.Message), "not found") {
				return fmt.Errorf("%w: %s", ErrNotFound, item.Message)
			}
		}
	}
	return err
}

// ProjectDetails reads a project. It returns ErrNotFound for a project that
// does not exist or is deleted.
func (c *Client) ProjectDetails(ctx context.Context, id string) (*ProjectDetails, error) {
	var data struct {
		Project ProjectDetails `json:"project"`
	}
	if err := c.Do(ctx, projectDetailsQuery, map[string]any{"id": id}, &data); err != nil {
		return nil, fmt.Errorf("read project %s: %w", id, notFound(err))
	}
	if data.Project.DeletedAt != nil {
		return nil, fmt.Errorf("read project %s: %w: deleted at %s", id, ErrNotFound, *data.Project.DeletedAt)
	}
	return &data.Project, nil
}

// ProjectCreate creates a project. Railway creates its first environment
// with it.
func (c *Client) ProjectCreate(ctx context.Context, input ProjectInput) (*ProjectDetails, error) {
	var data struct {
		ProjectCreate ProjectDetails `json:"projectCreate"`
	}
	if err := c.Do(ctx, projectCreateMutation, map[string]any{"input": input}, &data); err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return &data.ProjectCreate, nil
}

// ProjectUpdate changes a project's settings.
func (c *Client) ProjectUpdate(ctx context.Context, id string, input ProjectInput) (*ProjectDetails, error) {
	input.WorkspaceID, input.DefaultEnvironmentName = nil, nil
	var data struct {
		ProjectUpdate ProjectDetails `json:"projectUpdate"`
	}
	if err := c.Do(ctx, projectUpdateMutation, map[string]any{"id": id, "input": input}, &data); err != nil {
		return nil, fmt.Errorf("update project %s: %w", id, err)
	}
	return &data.ProjectUpdate, nil
}

// ProjectDelete deletes a project and everything in it.
func (c *Client) ProjectDelete(ctx context.Context, id string) error {
	return c.deleteByID(ctx, projectDeleteMutation, "projectDelete", "project", id)
}

// EnvironmentDetails reads an environment. It returns ErrNotFound for an
// environment that does not exist or is deleted.
func (c *Client) EnvironmentDetails(ctx context.Context, id string) (*EnvironmentDetails, error) {
	var data struct {
		Environment EnvironmentDetails `json:"environment"`
	}
	if err := c.Do(ctx, environmentDetailsQuery, map[string]any{"id": id}, &data); err != nil {
		return nil, fmt.Errorf("read environment %s: %w", id, notFound(err))
	}
	if data.Environment.DeletedAt != nil {
		return nil, fmt.Errorf("read environment %s: %w: deleted at %s", id, ErrNotFound, *data.Environment.DeletedAt)
	}
	return &data.Environment, nil
}

// EnvironmentCreate creates an environment without deploying anything in
// it. With a sourceEnvironmentID it copies that environment's services.
func (c *Client) EnvironmentCreate(ctx context.Context, projectID, name, sourceEnvironmentID string) (*EnvironmentDetails, error) {
	input := map[string]any{"projectId": projectID, "name": name, "skipInitialDeploys": true}
	if sourceEnvironmentID != "" {
		input["sourceEnvironmentId"] = sourceEnvironmentID
	}
	var data struct {
		EnvironmentCreate EnvironmentDetails `json:"environmentCreate"`
	}
	if err := c.Do(ctx, environmentCreateMutation, map[string]any{"input": input}, &data); err != nil {
		return nil, fmt.Errorf("create environment %s: %w", name, err)
	}
	return &data.EnvironmentCreate, nil
}

// EnvironmentRename renames an environment.
func (c *Client) EnvironmentRename(ctx context.Context, id, name string) (*EnvironmentDetails, error) {
	var data struct {
		EnvironmentRename EnvironmentDetails `json:"environmentRename"`
	}
	if err := c.Do(ctx, environmentRenameMutation, map[string]any{"id": id, "input": map[string]any{"name": name}}, &data); err != nil {
		return nil, fmt.Errorf("rename environment %s: %w", id, err)
	}
	return &data.EnvironmentRename, nil
}

// EnvironmentDelete deletes an environment and everything in it.
func (c *Client) EnvironmentDelete(ctx context.Context, id string) error {
	return c.deleteByID(ctx, environmentDeleteMutation, "environmentDelete", "environment", id)
}

func (c *Client) deleteByID(ctx context.Context, mutation, field, kind, id string) error {
	var data map[string]bool
	if err := c.Do(ctx, mutation, map[string]any{"id": id}, &data); err != nil {
		if errors.Is(notFound(err), ErrNotFound) {
			return nil
		}
		return fmt.Errorf("delete %s %s: %w", kind, id, err)
	}
	if !data[field] {
		return fmt.Errorf("delete %s %s: railway API reported the %s was not deleted", kind, id, kind)
	}
	return nil
}
