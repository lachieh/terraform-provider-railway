package railwayapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const defaultCommitMessage = "Apply Railway configuration"

const previewMutation = `mutation IacPreviewChangeSet($environmentId: String!, $input: JSON!) {
  environmentPreviewChangeSet(environmentId: $environmentId, input: $input) { changeSet diagnostics effects }
}`

const applyResultFields = `id status deploymentId stagedPatchId diagnostics changes { kind path summary status outputs }`

const applyMutation = `mutation IacApplyChangeSet($environmentId: String!, $input: JSON!, $commitMessage: String, $baseConfigEtag: String, $waitForCompletion: Boolean) {
  environmentApplyChangeSet(environmentId: $environmentId, input: $input, commitMessage: $commitMessage, baseConfigEtag: $baseConfigEtag, waitForCompletion: $waitForCompletion) { ` + applyResultFields + ` }
}`

const applyStatusQuery = `query IacChangeSetApplyStatus($environmentId: String!, $id: String!) {
  environmentChangeSetApply(environmentId: $environmentId, id: $id) { ` + applyResultFields + ` }
}`

// Apply statuses, from backboard's ChangeSetApplyResultShape.
const (
	ApplyStatusStaged           = "staged"
	ApplyStatusApplying         = "applying"
	ApplyStatusApplied          = "applied"
	ApplyStatusPartiallyApplied = "partially_applied"
	ApplyStatusFailed           = "failed"
)

// Preview is backboard's normalized change set with its diagnostics and
// predicted effects.
type Preview struct {
	ChangeSet   map[string]any  `json:"changeSet"`
	Diagnostics json.RawMessage `json:"diagnostics"`
	Effects     json.RawMessage `json:"effects"`
}

// ApplyChange is the outcome of one operation within an apply.
type ApplyChange struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
	Outputs any    `json:"outputs"`
}

// ApplyResult is the state of a change set apply.
type ApplyResult struct {
	ID            string        `json:"id"`
	Status        string        `json:"status"`
	DeploymentID  string        `json:"deploymentId"`
	StagedPatchID string        `json:"stagedPatchId"`
	Diagnostics   any           `json:"diagnostics"`
	Changes       []ApplyChange `json:"changes"`
}

// Err reports a failed apply. Backboard signals failure through status
// rather than a GraphQL error, so an apply only succeeded when its status is
// applied or staged and no change failed.
func (r ApplyResult) Err() error {
	var failed []string
	for _, change := range r.Changes {
		if change.Status != ApplyStatusFailed {
			continue
		}
		label := change.Summary
		if label == "" {
			label = change.Path
		}
		if label == "" {
			label = change.Kind
		}
		if label == "" {
			label = "change"
		}
		failed = append(failed, label)
	}
	status := r.Status
	if status == "" {
		status = ApplyStatusFailed
	}
	if (status == ApplyStatusApplied || status == ApplyStatusStaged) && len(failed) == 0 {
		return nil
	}
	if len(failed) == 0 {
		return fmt.Errorf("apply finished with status %s", status)
	}
	return fmt.Errorf("apply finished with status %s; failed changes: %s", status, strings.Join(failed, ", "))
}

// PreviewChangeSet asks backboard to normalize and validate changeSet against
// the environment without applying it.
func (c *Client) PreviewChangeSet(ctx context.Context, environmentID string, changeSet any) (*Preview, error) {
	var data struct {
		Preview Preview `json:"environmentPreviewChangeSet"`
	}
	vars := map[string]any{"environmentId": environmentID, "input": changeSet}
	if err := c.Do(ctx, previewMutation, vars, &data); err != nil {
		return nil, fmt.Errorf("preview change set: %w", err)
	}
	return &data.Preview, nil
}

// ApplyChangeSet applies changeSet and polls until the apply leaves the
// applying status or ctx ends. A non-empty baseConfigEtag makes backboard
// reject the apply if the environment changed since that etag; see
// IsConfigEtagConflict. An empty commitMessage uses a default. The returned
// result may still describe a failed apply; check ApplyResult.Err.
func (c *Client) ApplyChangeSet(ctx context.Context, environmentID string, changeSet any, baseConfigEtag, commitMessage string) (*ApplyResult, error) {
	if commitMessage == "" {
		commitMessage = defaultCommitMessage
	}
	vars := map[string]any{
		"environmentId":     environmentID,
		"input":             changeSet,
		"commitMessage":     commitMessage,
		"waitForCompletion": false,
	}
	if baseConfigEtag != "" {
		vars["baseConfigEtag"] = baseConfigEtag
	}
	var applied struct {
		Result ApplyResult `json:"environmentApplyChangeSet"`
	}
	if err := c.Do(ctx, applyMutation, vars, &applied); err != nil {
		return nil, fmt.Errorf("apply change set: %w", err)
	}
	result := applied.Result
	for result.Status == ApplyStatusApplying {
		if err := sleep(ctx, c.pollInterval); err != nil {
			return &result, fmt.Errorf("waiting for change set apply %s: %w", result.ID, err)
		}
		var polled struct {
			Result ApplyResult `json:"environmentChangeSetApply"`
		}
		pollVars := map[string]any{"environmentId": environmentID, "id": result.ID}
		if err := c.Do(ctx, applyStatusQuery, pollVars, &polled); err != nil {
			return &result, fmt.Errorf("poll change set apply %s: %w", result.ID, err)
		}
		result = polled.Result
	}
	return &result, nil
}

// staleEnvironmentCode is the extensions.code backboard is expected to return
// when baseConfigEtag no longer matches the environment.
const staleEnvironmentCode = "STALE_ENVIRONMENT_BASE"

// IsConfigEtagConflict reports whether err is backboard rejecting an apply
// because the environment changed since the base config etag.
func IsConfigEtagConflict(err error) bool {
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		return false
	}
	for _, item := range gqlErr.Errors {
		if item.Code() == staleEnvironmentCode {
			return true
		}
		if strings.Contains(strings.ToLower(item.Message), "changed since") {
			return true
		}
	}
	return false
}
