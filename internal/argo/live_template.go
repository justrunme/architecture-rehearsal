package argo

import (
	"context"

	"github.com/justrunme/architecture-rehearsal/internal/graph"
	"github.com/justrunme/architecture-rehearsal/internal/loader"
	"github.com/justrunme/architecture-rehearsal/internal/podtemplate"
)

// ApplyLiveTemplateCompare overrides podTemplateChanged on the change envelope
// using server-side dry-run apply against the live cluster. Dry-run failures
// fail closed (Changed=true).
//
// When dry-run reports Changed=false for a workload that was only patched for
// strategy reasons, the explicit false flag is written so detectors treat the
// sync as non-rolling. When dry-run reports Changed=true for a workload that
// subset compare missed, a patch node is added.
func ApplyLiveTemplateCompare(ctx context.Context, ch *loader.ChangeEnvelope, desiredDir string, opts podtemplate.KubectlOptions) error {
	if ch == nil || desiredDir == "" {
		return nil
	}
	results, err := podtemplate.ResolveDesiredDir(ctx, desiredDir, opts)
	if err != nil {
		return err
	}
	byID := map[string]podtemplate.WorkloadTemplateChange{}
	for _, r := range results {
		byID[r.ID] = r
	}

	seen := map[string]bool{}
	for i := range ch.PatchNodes {
		id := ch.PatchNodes[i].ID
		r, ok := byID[id]
		if !ok {
			continue
		}
		if ch.PatchNodes[i].Attributes == nil {
			ch.PatchNodes[i].Attributes = map[string]any{}
		}
		ch.PatchNodes[i].Attributes["podTemplateChanged"] = r.Changed
		ch.PatchNodes[i].Attributes["podTemplateCompare"] = r.Method
		if r.Detail != "" {
			ch.PatchNodes[i].Attributes["podTemplateCompareDetail"] = r.Detail
		}
		seen[id] = true
	}
	for i := range ch.AddedNodes {
		id := ch.AddedNodes[i].ID
		r, ok := byID[id]
		if !ok {
			continue
		}
		if ch.AddedNodes[i].Attributes == nil {
			ch.AddedNodes[i].Attributes = map[string]any{}
		}
		ch.AddedNodes[i].Attributes["podTemplateChanged"] = r.Changed
		ch.AddedNodes[i].Attributes["podTemplateCompare"] = r.Method
		if r.Detail != "" {
			ch.AddedNodes[i].Attributes["podTemplateCompareDetail"] = r.Detail
		}
		seen[id] = true
	}
	for id, r := range byID {
		if seen[id] {
			continue
		}
		if !r.Changed {
			continue
		}
		ch.PatchNodes = append(ch.PatchNodes, graph.Node{
			ID: id,
			Attributes: map[string]any{
				"podTemplateChanged":       true,
				"podTemplateCompare":       r.Method,
				"podTemplateCompareDetail": r.Detail,
			},
		})
		ch.Seeds = appendUnique(ch.Seeds, id)
	}
	return nil
}
