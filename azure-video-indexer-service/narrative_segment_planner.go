package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/provider/foundryprovider"
	"github.com/zecloud/ai-video-studio/internal/videoindexerstudio"
	"go.opentelemetry.io/otel/attribute"
)

const narrativeSegmentPlannerInstructionsVersion = "v1"

type NarrativeSegmentPlanner interface {
	Plan(context.Context, videoindexerstudio.NarrativeSegmentPlanningRequest) (videoindexerstudio.NarrativeSegmentPlanningResponse, error)
}
type narrativeSegmentPlannerRunner interface {
	RunSegmentPlan(context.Context, string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error)
}
type narrativeSegmentPlanner struct {
	runner                  narrativeSegmentPlannerRunner
	timeout                 time.Duration
	maxCatalog, maxSegments int
	obs                     *Observability
}

func (p narrativeSegmentPlanner) Plan(ctx context.Context, request videoindexerstudio.NarrativeSegmentPlanningRequest) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
	if p.runner == nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, errors.New("narrative segment planner is not configured")
	}
	if err := request.Validate(); err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, err
	}
	if len(request.Catalog) > p.maxCatalog {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, errors.New("narrative segment catalog limit exceeded")
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	raw, err := json.Marshal(request)
	if err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, err
	}
	start := time.Now()
	response, err := p.runner.RunSegmentPlan(ctx, string(raw))
	if p.obs != nil {
		p.obs.FinishSpan(ctx, nil, "narrative.segment.plan", start, []attribute.KeyValue{attribute.String("narrative_profile", string(request.Profile)), attribute.Int("catalog_count", len(request.Catalog)), attribute.Bool("narrative_intent_present", request.NarrativeIntent != "")}, err)
	}
	if err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, fmt.Errorf("invalid segment planner response: %w", err)
	}
	if len(response.Segments) > p.maxSegments {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, errors.New("narrative segment plan limit exceeded")
	}
	return response, nil
}

type foundryNarrativeSegmentPlannerRunner struct{ agent agentTextRunner }

func (r foundryNarrativeSegmentPlannerRunner) RunSegmentPlan(ctx context.Context, packet string) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
	if r.agent == nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, errors.New("foundry agent is not configured")
	}
	var output videoindexerstudio.NarrativeSegmentPlanningResponse
	_, err := r.agent.RunText(ctx, "Create a bounded grounded segment plan from this catalog only.\n"+packet, agent.WithStructuredOutput(&output), agent.Stream(false)).Collect()
	return output, err
}
func newNarrativeSegmentPlanner(cfg editPlannerConfig, timeout time.Duration, maxCatalog, maxSegments int, obs *Observability) (NarrativeSegmentPlanner, error) {
	if timeout <= 0 {
		return nil, errors.New("narrative segment planner timeout must be positive")
	}
	if maxCatalog <= 0 || maxCatalog > 48 || maxSegments <= 0 || maxSegments > 48 {
		return nil, errors.New("invalid narrative segment planner limits")
	}
	if cfg.CredentialFactory == nil {
		cfg.CredentialFactory = defaultManagedIdentityCredentialFactory
	}
	if strings.TrimSpace(cfg.FoundryEndpoint) == "" || strings.TrimSpace(cfg.ModelDeployment) == "" {
		return nil, errors.New("foundry endpoint and model deployment are required")
	}
	if err := validateFoundryProjectEndpoint(cfg.FoundryEndpoint); err != nil {
		return nil, err
	}
	credential, err := cfg.CredentialFactory()
	if err != nil {
		return nil, fmt.Errorf("creating managed identity credential: %w", err)
	}
	ag := foundryprovider.NewAgent(cfg.FoundryEndpoint, credential, foundryprovider.ModelDeployment(cfg.ModelDeployment), foundryprovider.AgentConfig{Instructions: narrativeSegmentPlannerInstructions(), Config: agent.Config{Name: "narrative-segment-planner"}})
	return narrativeSegmentPlanner{runner: foundryNarrativeSegmentPlannerRunner{agent: ag}, timeout: timeout, maxCatalog: maxCatalog, maxSegments: maxSegments, obs: obs}, nil
}
func narrativeSegmentPlannerInstructions() string {
	return `narrative-segment-planner instructions v1
Use only catalog segmentId values. Each output segment must cite only its catalog evidenceIds. Output each segment at most once. Omit startMs/endMs to retain the catalog range; if supplied, use only 100ms boundaries strictly inside the allowed range. Never invent or alter source IDs, candidate IDs, evidence IDs, timecodes, ranges, or descriptors. Roles are exactly hook, context, development, payoff, outro. Return only the structured response.`
}
