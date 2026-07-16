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

const narrativeIntentClassifierPromptVersion = "v1"

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
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, errors.New("narrative intent classifier is not configured")
	}
	if err := request.Validate(); err != nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	result, err := c.runner.RunClassification(ctx, request.NarrativeIntent)
	if err != nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, err
	}
	if err := result.Validate(); err != nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, fmt.Errorf("invalid classifier response: %w", err)
	}
	if c.obs != nil {
		classifiedCtx, span := c.obs.StartSpan(ctx, "narrative.intent.classified", attribute.String("narrative_profile", string(result.Profile)), attribute.Int("narrative_intent_length", len([]rune(request.NarrativeIntent))))
		c.obs.FinishSpan(classifiedCtx, span, "narrative.intent.classified", time.Now(), nil, nil)
	}
	return result, nil
}

type foundryNarrativeIntentClassifierRunner struct {
	agent agentTextRunner
}

func (r foundryNarrativeIntentClassifierRunner) RunClassification(ctx context.Context, intent string) (videoindexerstudio.NarrativeIntentClassificationResponse, error) {
	if r.agent == nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, errors.New("foundry agent is not configured")
	}
	var output videoindexerstudio.NarrativeIntentClassificationResponse
	prompt := "Classify this normalized editorial preference into exactly one profile. Preference:\n" + intent
	_, err := r.agent.RunText(ctx, prompt, agent.WithStructuredOutput(&output), agent.Stream(false)).Collect()
	if err != nil {
		return videoindexerstudio.NarrativeIntentClassificationResponse{}, err
	}
	return output, nil
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
	return `narrative-intent-classifier instructions v1
Classify a user-authored editorial preference in any language into exactly one closed profile: energetic, chronological, calm, or standard.
Energetic means faster, action-forward, or short-form social pacing. Chronological means continuity or time order. Calm means recap, reflective, or relaxed pacing. Use standard when unclear.
Return only the structured response. Do not return clip IDs, source IDs, evidence, timestamps, ranges, ordering, explanations, or user text.`
}
