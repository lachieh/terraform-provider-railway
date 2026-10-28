package railwayapi

import (
	"context"
	"fmt"
	"strings"
)

const pageArgs = `($projectId: String!, $first: Int, $after: String)`
const pageSelection = `pageInfo { hasNextPage endCursor }`

const projectServicesQuery = `query IacProjectServices` + pageArgs + ` {
  project(id: $projectId) { items: services(first: $first, after: $after) { edges { node { id name templateServiceId } } ` + pageSelection + ` } }
}`

const projectVolumesQuery = `query IacProjectVolumes` + pageArgs + ` {
  project(id: $projectId) { items: volumes(first: $first, after: $after) { edges { node { id name } } ` + pageSelection + ` } }
}`

const projectBucketsQuery = `query IacProjectBuckets` + pageArgs + ` {
  project(id: $projectId) { items: buckets(first: $first, after: $after) { edges { node { id name groupId } } ` + pageSelection + ` } }
}`

const projectNameQuery = `query IacProjectName($projectId: String!) { project(id: $projectId) { name } }`

const githubReposQuery = `query GitHubRepos { githubRepos { fullName defaultBranch } }`

// NameMaps resolves resource IDs in an environment config to the names and
// groups the IaC graph uses.
type NameMaps struct {
	ProjectName            string
	ServiceNamesByID       map[string]any
	TemplateServiceIDsByID map[string]any
	VolumeNamesByID        map[string]any
	VolumeGroupIDsByID     map[string]any
	BucketNamesByID        map[string]any
	BucketGroupIDsByID     map[string]any
}

// FetchNameMaps loads the project's service, volume and bucket names. Volume
// group IDs come from env.CanvasGroupRefs. A failure to read the project name
// leaves ProjectName empty rather than failing.
func (c *Client) FetchNameMaps(ctx context.Context, projectID string, env *Environment) (*NameMaps, error) {
	names := &NameMaps{
		ServiceNamesByID:       map[string]any{},
		TemplateServiceIDsByID: map[string]any{},
		VolumeNamesByID:        map[string]any{},
		VolumeGroupIDsByID:     map[string]any{},
		BucketNamesByID:        map[string]any{},
		BucketGroupIDsByID:     map[string]any{},
	}
	vars := map[string]any{"projectId": projectID}

	var named struct {
		Project struct {
			Name string `json:"name"`
		} `json:"project"`
	}
	if err := c.Do(ctx, projectNameQuery, vars, &named); err == nil {
		names.ProjectName = named.Project.Name
	}

	services, err := projectConnection[struct {
		ID                string  `json:"id"`
		Name              string  `json:"name"`
		TemplateServiceID *string `json:"templateServiceId"`
	}](ctx, c, projectServicesQuery, vars)
	if err != nil {
		return nil, fmt.Errorf("load project services for IaC name names: %w", err)
	}
	for _, s := range services {
		if s.Name != "" {
			names.ServiceNamesByID[s.ID] = s.Name
		}
		if s.TemplateServiceID != nil {
			names.TemplateServiceIDsByID[s.ID] = *s.TemplateServiceID
		}
	}

	volumes, err := projectConnection[struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}](ctx, c, projectVolumesQuery, vars)
	if err != nil {
		return nil, fmt.Errorf("load project volumes for IaC name names: %w", err)
	}
	for _, v := range volumes {
		if v.Name != "" {
			names.VolumeNamesByID[v.ID] = v.Name
		}
		if env != nil {
			if group, ok := env.CanvasGroupRefs[v.ID]; ok {
				names.VolumeGroupIDsByID[v.ID] = group
			}
		}
	}

	buckets, err := projectConnection[struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		GroupID *string `json:"groupId"`
	}](ctx, c, projectBucketsQuery, vars)
	if err != nil {
		return nil, fmt.Errorf("load project buckets for IaC name names: %w", err)
	}
	for _, b := range buckets {
		if b.Name != "" {
			names.BucketNamesByID[b.ID] = b.Name
		}
		if b.GroupID != nil {
			names.BucketGroupIDsByID[b.ID] = *b.GroupID
		}
	}
	return names, nil
}

// GitHubDefaultBranches returns the default branch of every GitHub repository
// the credential can see, keyed by lowercased "owner/name".
func (c *Client) GitHubDefaultBranches(ctx context.Context) (map[string]string, error) {
	var data struct {
		GithubRepos []struct {
			FullName      string `json:"fullName"`
			DefaultBranch string `json:"defaultBranch"`
		} `json:"githubRepos"`
	}
	if err := c.Do(ctx, githubReposQuery, nil, &data); err != nil {
		return nil, fmt.Errorf("list GitHub repositories: %w", err)
	}
	branches := make(map[string]string, len(data.GithubRepos))
	for _, repo := range data.GithubRepos {
		branches[strings.ToLower(repo.FullName)] = repo.DefaultBranch
	}
	return branches, nil
}
