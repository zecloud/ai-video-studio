package main

import (
	"context"
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
	narrativeIntentClassifierPromptVersion = "v4"
	narrativeClassifierAttempts            = 2
)

type NarrativeIntentClassifier interface {
	Classify(context.Context, videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error)
}

type narrativeIntentClassifierRunner interface {
	RunClassification(context.Context, string, bool) (videoindexerstudio.NarrativeIntentClassificationResponse, error)
}

type narrativeIntentClassifier struct {
	runner  narrativeIntentClassifierRunner
	timeout time.Duration
	obs     *Observability
}

func (c narrativeIntentClassifier) Classify(ctx context.Context, request videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
	if c.runner == nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, narrativeFailureError(narrativeFailureUnavailable, errors.New("classifier not configured"))
	}
	if err := request.Validate(); err != nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, narrativeFailureError(narrativeFailureInvalidReq, err)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	start := time.Now()
	var result videoindexerstudio.NarrativeIntentClassificationResponse
	var err error
	validationReason := ""
	attempts := 0
	for attempts = 1; attempts <= narrativeClassifierAttempts; attempts++ {
		result, err = c.runner.RunClassification(attemptCtx, request.NarrativeIntent, attempts > 1)
		if err == nil {
			if result.SchemaVersion == 0 {
				// The endpoint fixes this contract version; the framework may omit a constant field.
				result.SchemaVersion = videoindexerstudio.NarrativeRankingSchemaVersion
				validationReason = "missing_schema_version_normalized"
			} else if result.SchemaVersion != videoindexerstudio.NarrativeRankingSchemaVersion {
				validationReason = "incompatible_schema_version"
				err = narrativeFailureError(narrativeFailureInvalid, errors.New("incompatible narrative intent schema version"))
			}
			if err == nil && !result.Profile.Valid() {
				validationReason = "invalid_profile_or_contract"
				err = narrativeFailureError(narrativeFailureInvalid, errors.New("invalid narrative intent profile"))
			} else if err == nil && result.Query != nil {
				if queryErr := result.Query.Validate(); queryErr != nil {
					validationReason = "invalid_query_contract"
					err = narrativeFailureError(narrativeFailureInvalid, fmt.Errorf("invalid narrative intent query: %w", queryErr))
				}
			}
		} else {
			err = classifyNarrativeProviderError(err)
		}
		if err == nil || (narrativeFailureFor(err) != narrativeFailureTransient && narrativeFailureFor(err) != narrativeFailureInvalid) || attemptCtx.Err() != nil || attempts == narrativeClassifierAttempts {
			break
		}
		if c.obs != nil {
			c.obs.RecordRetry(ctx, "narrative.intent.classify", 0, attribute.String("failure_kind", string(narrativeFailureFor(err))))
		}
		select {
		case <-attemptCtx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	if attemptCtx.Err() != nil && err != nil {
		err = narrativeFailureError(narrativeFailureTimeout, attemptCtx.Err())
	}
	if c.obs != nil {
		c.obs.FinishSpan(ctx, nil, "narrative.intent.classify", start, []attribute.KeyValue{
			attribute.String("prompt_version", narrativeIntentClassifierPromptVersion),
			attribute.Int("attempt_count", attempts),
			attribute.String("failure_kind", string(narrativeFailureFor(err))),
			attribute.String("validation_reason", validationReason),
			attribute.Int("narrative_intent_length", len([]rune(request.NarrativeIntent))),
		}, err)
	}
	if err != nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, err
	}
	return result, nil
}

type foundryNarrativeIntentClassifierRunner struct{ agent agentTextRunner }

func (r foundryNarrativeIntentClassifierRunner) RunClassification(ctx context.Context, intent string, correction bool) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
	if r.agent == nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, errors.New("foundry agent is not configured")
	}
	var output videoindexerstudio.NarrativeIntentClassificationResponse
	prompt := "Interpret this normalized narrative request. Request:\n" + intent
	if correction {
		prompt = "Your previous structured response did not satisfy the closed contract. Retry from the request below. Return schemaVersion 1, a valid closed profile, and either no query or a query that exactly satisfies every enum, normalization, count, and term-length rule. Do not guess or emit partial query objects. Request:\n" + intent
	}
	_, err := r.agent.RunText(ctx, prompt, agent.WithStructuredOutput(&output), agent.Stream(false)).Collect()
	return output, err
}

func newNarrativeIntentClassifier(plannerConfig editPlannerConfig, timeout time.Duration, obs *Observability) (NarrativeIntentClassifier, error) {
	plannerConfig.Observability = obs
	if timeout <= 0 {
		return nil, errors.New("narrative intent classifier timeout must be positive")
	}
	if plannerConfig.CredentialFactory == nil {
		plannerConfig.CredentialFactory = defaultManagedIdentityCredentialFactory
	}
	if strings.TrimSpace(plannerConfig.FoundryEndpoint) == "" || strings.TrimSpace(plannerConfig.ModelDeployment) == "" {
		return nil, errors.New("foundry endpoint and model deployment are required")
	}
	if err := validateFoundryProjectEndpoint(plannerConfig.FoundryEndpoint); err != nil {
		return nil, err
	}
	credential, err := plannerConfig.CredentialFactory()
	if err != nil {
		return nil, fmt.Errorf("creating managed identity credential: %w", err)
	}
	ag := foundryprovider.NewAgent(plannerConfig.FoundryEndpoint, credential, foundryprovider.ModelDeployment(plannerConfig.ModelDeployment), foundryprovider.AgentConfig{Instructions: narrativeIntentClassifierInstructions(), Config: agent.Config{Name: "narrative-intent-classifier"}})
	return narrativeIntentClassifier{runner: foundryNarrativeIntentClassifierRunner{agent: ag}, timeout: timeout, obs: obs}, nil
}

func narrativeIntentClassifierInstructions() string {
	return `narrative-intent-classifier instructions v4
Interpret a user-authored narrative request in any language into editorial style and independently verifiable content constraints.
Return schemaVersion 1 and exactly one closed profile: standard, energetic, chronological, calm, cinematic, social_short_form, tutorial, highlight_reel, recap, storytelling, travel, interview, or product_showcase.
When the request contains content requirements, return query schemaVersion 1, coverage best_subset unless the user explicitly asks for every occurrence or one result per source, and at most 8 clauses. Clause IDs are stable c1, c2, etc. Importance is must for required content, prefer for optional content, and avoid only for explicitly excluded content. Predicates are only visible_entity (objects, actions, or labels visible in the image), spoken_text, visible_text (OCR), or unsupported. Terms are lowercase normalized phrases in the evidence language or explicit multilingual alternatives, at most 8 per clause and 80 characters each. Use matchMode any for alternatives and all for conjunctions. Use relation overlap when all terms must coexist and sequence only when the user explicitly requires ordered events.
Do not encode pacing, tone, mood, chronology, aesthetics, platform style, duration, quality, emotion, intent, causality, exact identity, precise object position, or visual absence as verifiable content; keep style in profile and use unsupported only when such a requirement is mandatory and cannot be represented. Non-detection never proves absence. Do not invent synonyms unrelated to the request.
Return only structured output. If a content query cannot be represented exactly, omit query rather than returning an invalid query. Never return clip IDs, source IDs, evidence IDs, timestamps, ranges, ordering, explanations, confidence prose, or the original user text.`
}
