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
	narrativeIntentClassifierPromptVersion = "v2"
	narrativeClassifierAttempts            = 2
)

type NarrativeIntentClassifier interface {
	Classify(context.Context, videoindexerstudio.NarrativeIntentClassificationRequest) (videoindexerstudio.NarrativeIntentClassificationResponse, error)
}

type narrativeIntentClassifierRunner interface {
	RunClassification(context.Context, string) (videoindexerstudio.NarrativeIntentClassificationResponse, error)
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
		result, err = c.runner.RunClassification(attemptCtx, request.NarrativeIntent)
		if err == nil {
			if result.SchemaVersion == 0 {
				// The endpoint fixes this contract version; the framework may omit a constant field.
				result.SchemaVersion = videoindexerstudio.NarrativeRankingSchemaVersion
				validationReason = "missing_schema_version_normalized"
			}
			if validationErr := result.Validate(); validationErr != nil {
				validationReason = "invalid_profile_or_contract"
				err = narrativeFailureError(narrativeFailureInvalid, validationErr)
			}
		} else {
			err = classifyNarrativeProviderError(err)
		}
		if err == nil || !isRetryableNarrativeFailure(err) || attemptCtx.Err() != nil || attempts == narrativeClassifierAttempts {
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

func (r foundryNarrativeIntentClassifierRunner) RunClassification(ctx context.Context, intent string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
	if r.agent == nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, errors.New("foundry agent is not configured")
	}
	var output videoindexerstudio.NarrativeIntentClassificationResponse
	prompt := "Classify this normalized editorial preference into exactly one profile. Preference:\n" + intent
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
	return `narrative-intent-classifier instructions v2
Classify a user-authored editorial preference in any language into exactly one closed profile: standard, energetic, chronological, calm, cinematic, social_short_form, tutorial, highlight_reel, recap, storytelling, travel, interview, or product_showcase.
Energetic means fast action. Social_short_form means social or TikTok-style pacing, including multilingual requests such as "robots dansants en mode video TikTok". Chronological means continuity or time order. Calm and recap mean reflective coverage. Cinematic emphasizes measured visual moments. Tutorial prioritizes explanatory continuity. Highlight_reel prioritizes concise best moments. Storytelling prioritizes narrative development. Travel, interview, and product_showcase select their corresponding editorial approach. Use standard when unclear.
Return only the structured response with schemaVersion 1 and one valid profile. Do not return clip IDs, source IDs, evidence, timestamps, ranges, ordering, explanations, or user text.`
}
