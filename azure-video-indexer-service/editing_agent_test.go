package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/microsoft/agent-framework-go/agent"
)

func TestNewEditPlannerRejectsMissingEndpointAndModel(t *testing.T) {
	credentialCalls := 0
	cfg := editPlannerConfig{
		CredentialFactory: func() (azcore.TokenCredential, error) {
			credentialCalls++
			return fakeCredential{}, nil
		},
		AgentFactory: func(string, azcore.TokenCredential, string, string, string, *Observability) (editPlannerRunner, error) {
			t.Fatal("agent factory should not be called for invalid config")
			return nil, nil
		},
	}

	_, err := newEditPlanner(cfg)
	if err == nil || !strings.Contains(err.Error(), "foundry endpoint") {
		t.Fatalf("expected missing endpoint error, got %v", err)
	}
	if credentialCalls != 0 {
		t.Fatalf("credential factory should not be called, got %d calls", credentialCalls)
	}

	credentialCalls = 0
	cfg.FoundryEndpoint = "https://example.com"
	_, err = newEditPlanner(cfg)
	if err == nil || !strings.Contains(err.Error(), "model deployment") {
		t.Fatalf("expected missing model error, got %v", err)
	}
	if credentialCalls != 0 {
		t.Fatalf("credential factory should not be called, got %d calls", credentialCalls)
	}
}

func TestNewEditPlannerBuildsFoundryRunnerWithDefaults(t *testing.T) {
	t.Setenv("FOUNDRY_ENDPOINT", "")
	t.Setenv("AZURE_FOUNDRY_ENDPOINT", "")
	t.Setenv("FOUNDRY_PROJECT_ENDPOINT", "")

	var gotEndpoint, gotModel, gotAgentName, gotInstructions string
	var credentialCalled bool
	planner, err := newEditPlanner(editPlannerConfig{
		FoundryEndpoint: "https://contoso.services.ai.azure.com/api/projects/smart-edit/",
		ModelDeployment: "gpt-5.4",
		CredentialFactory: func() (azcore.TokenCredential, error) {
			credentialCalled = true
			return fakeCredential{}, nil
		},
		AgentFactory: func(endpoint string, credential azcore.TokenCredential, modelDeployment, agentName, instructions string, obs *Observability) (editPlannerRunner, error) {
			gotEndpoint = endpoint
			gotModel = modelDeployment
			gotAgentName = agentName
			gotInstructions = instructions
			return editPlannerRunnerFunc(func(ctx context.Context, prompt string) (EditPlan, error) {
				if prompt != "draft an edit plan" {
					t.Fatalf("unexpected prompt: %q", prompt)
				}
				return EditPlan{Title: "plan-ready"}, nil
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("newEditPlanner: %v", err)
	}
	if !credentialCalled {
		t.Fatal("expected credential factory call")
	}
	if gotEndpoint != "https://contoso.services.ai.azure.com/api/projects/smart-edit/" {
		t.Fatalf("endpoint = %q", gotEndpoint)
	}
	if gotModel != "gpt-5.4" {
		t.Fatalf("model = %q", gotModel)
	}
	if gotAgentName != defaultEditPlannerAgentName {
		t.Fatalf("agent name = %q", gotAgentName)
	}
	if !strings.Contains(gotInstructions, editPlannerInstructionsVersion) {
		t.Fatalf("instructions missing version marker: %q", gotInstructions)
	}

	got, err := planner.Plan(context.Background(), "draft an edit plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got.Title != "plan-ready" {
		t.Fatalf("plan output = %#v", got)
	}
}

func TestValidateFoundryProjectEndpoint(t *testing.T) {
	valid := "https://contoso.services.ai.azure.com/api/projects/smart-edit"
	if err := validateFoundryProjectEndpoint(valid); err != nil {
		t.Fatalf("expected valid endpoint, got %v", err)
	}

	for _, raw := range []string{
		"https://contoso.openai.azure.com/",
		"https://contoso.services.ai.azure.com/",
		"https://contoso.services.ai.azure.com/api/",
		"https://contoso.services.ai.azure.com/api/projects/",
		"http://contoso.services.ai.azure.com/api/projects/smart-edit",
	} {
		if err := validateFoundryProjectEndpoint(raw); err == nil {
			t.Fatalf("expected invalid endpoint: %s", raw)
		}
	}
}

type fakeCredential struct{}

func (fakeCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "fake", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

var _ azcore.TokenCredential = fakeCredential{}

func TestEditPlannerPropagatesContextCancellation(t *testing.T) {
	started := make(chan struct{})
	planner := &editPlanner{
		runner: editPlannerRunnerFunc(func(ctx context.Context, prompt string) (EditPlan, error) {
			if prompt != "cancel me" {
				t.Fatalf("unexpected prompt: %q", prompt)
			}
			close(started)
			<-ctx.Done()
			return EditPlan{}, ctx.Err()
		}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := planner.Plan(ctx, "cancel me")
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("planner did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("planner did not finish after cancellation")
	}
}

func TestFoundryAgentRunnerUsesStructuredOutputAndBatchMode(t *testing.T) {
	fake := &fakeTextRunner{}
	runner := foundryAgentRunner{agent: fake}

	plan, err := runner.RunPlan(context.Background(), "build a plan")
	if err != nil {
		t.Fatalf("run plan: %v", err)
	}
	if plan.Title != "" {
		t.Fatalf("unexpected returned plan: %#v", plan)
	}
	if fake.prompt != "build a plan" {
		t.Fatalf("unexpected prompt: %q", fake.prompt)
	}
	if out, ok := agent.GetOption[any](fake.options, agent.WithStructuredOutput); !ok || out == nil {
		t.Fatalf("structured output option missing: %#v", fake.options)
	}
	if stream, ok := agent.GetOption[bool](fake.options, agent.Stream); !ok || stream {
		t.Fatalf("expected non-streaming agent call, got %#v", fake.options)
	}
}

type fakeTextRunner struct {
	prompt  string
	options []agent.Option
}

func (f *fakeTextRunner) RunText(_ context.Context, prompt string, options ...agent.Option) agent.ResponseStream {
	f.prompt = prompt
	f.options = append([]agent.Option(nil), options...)
	return agent.ResponseStream(func(func(*agent.ResponseUpdate, error) bool) {})
}
