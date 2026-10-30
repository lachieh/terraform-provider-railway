package railwayapi

import (
	"context"
	"fmt"
)

const latestDeploymentQuery = `query IacLatestDeployment($input: DeploymentListInput!) {
  deployments(first: 1, input: $input) { edges { node { id status url staticUrl createdAt updatedAt } } }
}`

const bucketCredentialsQuery = `query IacBucketCredentials($projectId: String!, $environmentId: String!, $bucketId: String!) {
  bucketS3Credentials(projectId: $projectId, environmentId: $environmentId, bucketId: $bucketId) {
    accessKeyId secretAccessKey endpoint bucketName region urlStyle createdAt
  }
}`

// Deployment is one deployment of a service in an environment.
type Deployment struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	URL       string `json:"url"`
	StaticURL string `json:"staticUrl"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// LatestDeployment returns a service's newest deployment in an environment,
// or nil when it has none.
func (c *Client) LatestDeployment(ctx context.Context, environmentID, serviceID string) (*Deployment, error) {
	var data struct {
		Deployments struct {
			Edges []struct {
				Node Deployment `json:"node"`
			} `json:"edges"`
		} `json:"deployments"`
	}
	input := map[string]any{"environmentId": environmentID, "serviceId": serviceID}
	if err := c.Do(ctx, latestDeploymentQuery, map[string]any{"input": input}, &data); err != nil {
		return nil, fmt.Errorf("read deployments of service %s: %w", serviceID, err)
	}
	if len(data.Deployments.Edges) == 0 {
		return nil, nil
	}
	return &data.Deployments.Edges[0].Node, nil
}

// BucketCredentials are S3 credentials for a bucket in one environment.
type BucketCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	Endpoint        string `json:"endpoint"`
	BucketName      string `json:"bucketName"`
	Region          string `json:"region"`
	URLStyle        string `json:"urlStyle"`
	CreatedAt       string `json:"createdAt"`
}

// BucketCredentials returns the S3 credentials of a bucket in an
// environment. Railway returns a list; this is its first entry.
func (c *Client) BucketCredentials(ctx context.Context, projectID, environmentID, bucketID string) (*BucketCredentials, error) {
	var data struct {
		Credentials []BucketCredentials `json:"bucketS3Credentials"`
	}
	vars := map[string]any{"projectId": projectID, "environmentId": environmentID, "bucketId": bucketID}
	if err := c.Do(ctx, bucketCredentialsQuery, vars, &data); err != nil {
		return nil, fmt.Errorf("read credentials of bucket %s: %w", bucketID, err)
	}
	if len(data.Credentials) == 0 {
		return nil, fmt.Errorf("read credentials of bucket %s: Railway returned none for environment %s", bucketID, environmentID)
	}
	return &data.Credentials[0], nil
}
