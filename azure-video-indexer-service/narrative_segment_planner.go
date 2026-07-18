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
	narrativeSegmentPlannerInstructionsVersion = "v3"
	narrativeSegmentPlannerAttempts            = 2
)

type NarrativeSegmentPlanner interface {
	Plan(context.Context, videoindexerstudio.NarrativeSegmentPlanningRequest) (videoindexerstudio.NarrativeSegmentPlanningResponse, error)
}
type narrativeSegmentPlannerRunner interface {
	RunSegmentPlan(context.Context, string) (foundryNarrativeSegmentPlan, error)
}

type foundryNarrativeSegmentPlan struct {
	Segments []foundryNarrativeSegmentPlanItem `json:"segments"`
}

type foundryNarrativeSegmentPlanItem struct {
	SegmentID         string   `json:"segmentId"`
	Role              string   `json:"role"`
	AnchorEvidenceIDs []string `json:"anchorEvidenceIds"`
	AnchorMode        string   `json:"anchorMode"`
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
		var providerResponse foundryNarrativeSegmentPlan
		providerResponse, err = p.runner.RunSegmentPlan(planCtx, string(raw))
		if err == nil {
			response = narrativeSegmentPlanningResponse(request.SchemaVersion, providerResponse)
			validationReason = ""
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

func (r foundryNarrativeSegmentPlannerRunner) RunSegmentPlan(ctx context.Context, packet string) (foundryNarrativeSegmentPlan, error) {
	if r.agent == nil {
		return foundryNarrativeSegmentPlan{}, errors.New("foundry agent is not configured")
	}
	var output foundryNarrativeSegmentPlan
	_, err := r.agent.RunText(ctx, "Create a bounded grounded segment plan from this catalog only.\n"+packet, agent.WithStructuredOutput(&output), agent.Stream(false)).Collect()
	return output, err
}

func narrativeSegmentPlanningResponse(schemaVersion int, output foundryNarrativeSegmentPlan) videoindexerstudio.NarrativeSegmentPlanningResponse {
	response := videoindexerstudio.NarrativeSegmentPlanningResponse{SchemaVersion: schemaVersion, Segments: make([]videoindexerstudio.NarrativeSegmentPlanItem, 0, len(output.Segments))}
	for _, item := range output.Segments {
		planned := videoindexerstudio.NarrativeSegmentPlanItem{SegmentID: item.SegmentID, Role: videoindexerstudio.NarrativeSegmentRole(item.Role)}
		if schemaVersion == videoindexerstudio.NarrativeSegmentPlanningLegacySchemaVersion {
			planned.EvidenceIDs = append([]string(nil), item.AnchorEvidenceIDs...)
		} else {
			planned.AnchorEvidenceIDs = append([]string(nil), item.AnchorEvidenceIDs...)
			planned.AnchorMode = videoindexerstudio.NarrativeSegmentAnchorMode(item.AnchorMode)
		}
		response.Segments = append(response.Segments, planned)
	}
	return response
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
	return `narrative-segment-planner instructions v3
The input packet contains maxSegments and request. Return one to maxSegments segments. Select only request.catalog segmentId values, each once. Roles are exactly hook, context, development, payoff, outro. For every selected segment cite one or more anchorEvidenceIds from that exact catalog item. Use anchorMode simultaneous only when all cited evidence must overlap at the chosen moment; use sequence when the complete ordered span of the cited evidence must be preserved. Do not generate timecodes. Prefer evidence whose descriptor is relevant to narrativeIntent. Never invent or alter IDs, sources, ranges, evidence, descriptors, or fields. Return only the structured response.`
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
