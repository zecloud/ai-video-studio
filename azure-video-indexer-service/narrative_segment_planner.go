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

const (
	narrativeSegmentPlannerInstructionsVersion = "v2"
	narrativeSegmentPlannerAttempts            = 2
)

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

// narrativeSegmentPlannerPacket supplies the service-enforced selection bound to Foundry.
// The request itself remains the stable desktop-to-API contract.
type narrativeSegmentPlannerPacket struct {
	MaxSegments int                                                `json:"maxSegments"`
	Request     videoindexerstudio.NarrativeSegmentPlanningRequest `json:"request"`
}

func (p narrativeSegmentPlanner) Plan(ctx context.Context, request videoindexerstudio.NarrativeSegmentPlanningRequest) (videoindexerstudio.NarrativeSegmentPlanningResponse, error) {
	if p.runner == nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, narrativeFailureError(narrativeFailureUnavailable, errors.New("planner not configured"))
	}
	if err := request.Validate(); err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, narrativeFailureError(narrativeFailureInvalidReq, err)
	}
	if len(request.Catalog) > p.maxCatalog {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, narrativeFailureError(narrativeFailureLimit, errors.New("catalog limit exceeded"))
	}
	raw, err := json.Marshal(narrativeSegmentPlannerPacket{MaxSegments: p.maxSegments, Request: request})
	if err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, narrativeFailureError(narrativeFailureInvalidReq, err)
	}
	planCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	start := time.Now()
	var response videoindexerstudio.NarrativeSegmentPlanningResponse
	validationReason := ""
	attempts := 0
	for attempts = 1; attempts <= narrativeSegmentPlannerAttempts; attempts++ {
		response, err = p.runner.RunSegmentPlan(planCtx, string(raw))
		if err == nil {
			validationReason = ""
			if response.SchemaVersion == 0 {
				// The endpoint fixes this contract version; the framework may omit a constant field.
				response.SchemaVersion = videoindexerstudio.NarrativeSegmentPlanningSchemaVersion
				validationReason = "missing_schema_version_normalized"
			}
			if validationErr := response.Validate(); validationErr != nil {
				validationReason = "invalid_response"
				err = narrativeFailureError(narrativeFailureInvalid, validationErr)
			} else if len(response.Segments) > p.maxSegments {
				err = narrativeFailureError(narrativeFailureLimit, errors.New("segment limit exceeded"))
			}
		} else {
			validationReason = narrativePlannerProviderFailureReason(err)
			err = classifyNarrativeProviderError(err)
		}
		if err == nil || !isRetryableNarrativeFailure(err) || planCtx.Err() != nil || attempts == narrativeSegmentPlannerAttempts {
			break
		}
		if p.obs != nil {
			p.obs.RecordRetry(ctx, "narrative.segment.plan", 0, attribute.String("failure_kind", string(narrativeFailureFor(err))))
		}
		select {
		case <-planCtx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	if planCtx.Err() != nil && err != nil {
		err = narrativeFailureError(narrativeFailureTimeout, planCtx.Err())
	}
	if p.obs != nil {
		p.obs.FinishSpan(ctx, nil, "narrative.segment.plan", start, []attribute.KeyValue{attribute.String("prompt_version", narrativeSegmentPlannerInstructionsVersion), attribute.String("narrative_profile", string(request.Profile)), attribute.Int("catalog_count", len(request.Catalog)), attribute.Int("attempt_count", attempts), attribute.String("failure_kind", string(narrativeFailureFor(err))), attribute.String("validation_reason", validationReason), attribute.String("runner_failure_reason", narrativePlannerRunnerFailureReason(validationReason)), attribute.Bool("narrative_intent_present", request.NarrativeIntent != "")}, err)
	}
	if err != nil {
		return videoindexerstudio.NarrativeSegmentPlanningResponse{}, err
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
	return `narrative-segment-planner instructions v2
The input packet contains maxSegments and request. Return schemaVersion 1 and one to maxSegments segments. Select only request.catalog segmentId values, each once. Each segment must cite one or more evidenceIds listed on that exact catalog item. Omit startMs and endMs unless a shorter valid trim is necessary; a supplied trim must use 100ms boundaries, remain entirely inside that item's allowed range, and preserve at least one second. Roles are exactly hook, context, development, payoff, outro. Never invent or alter source IDs, candidate IDs, evidence IDs, timecodes, ranges, descriptors, or fields. Return only the structured response.`
}

func narrativePlannerProviderFailureReason(err error) string {
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "structured output"), strings.Contains(lower, "unmarshal"), strings.Contains(lower, "decode"), strings.Contains(lower, "json"):
		return "structured_output_decode_failure"
	case strings.Contains(lower, "invalid"), strings.Contains(lower, "schema"):
		return "provider_response_rejected"
	default:
		return "provider_transport"
	}
}

func narrativePlannerRunnerFailureReason(validationReason string) string {
	switch validationReason {
	case "structured_output_decode_failure", "provider_response_rejected", "provider_transport":
		return validationReason
	default:
		return ""
	}
}
