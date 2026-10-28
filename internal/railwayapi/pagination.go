package railwayapi

import (
	"context"
	"fmt"
	"maps"
)

const pageSize = 100

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type connection[T any] struct {
	Edges []struct {
		Node T `json:"node"`
	} `json:"edges"`
	PageInfo pageInfo `json:"pageInfo"`
}

// projectConnection fetches every node of a connection on project(id:).
// query must declare $projectId, $first and $after and select the connection
// under the alias "items".
func projectConnection[T any](ctx context.Context, c *Client, query string, vars map[string]any) ([]T, error) {
	var nodes []T
	after := ""
	for {
		pageVars := maps.Clone(vars)
		pageVars["first"] = pageSize
		if after != "" {
			pageVars["after"] = after
		}
		var data struct {
			Project struct {
				Items connection[T] `json:"items"`
			} `json:"project"`
		}
		if err := c.Do(ctx, query, pageVars, &data); err != nil {
			return nil, err
		}
		for _, edge := range data.Project.Items.Edges {
			nodes = append(nodes, edge.Node)
		}
		page := data.Project.Items.PageInfo
		if !page.HasNextPage {
			return nodes, nil
		}
		if page.EndCursor == "" || page.EndCursor == after {
			return nil, fmt.Errorf("railway API reported another page without advancing the cursor")
		}
		after = page.EndCursor
	}
}
