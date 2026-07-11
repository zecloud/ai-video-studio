package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/provider/foundryprovider"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultEditPlannerAgentName       = "smart-edit-planner"
	defaultEditPlannerModelDeployment = "gpt-5.4"
	editPlannerInstructionsVersion    = "v2"
)

type EditPlanner interface {
	Plan(ctx context.Context, prompt string) (EditPlan, error)
}

type editPlannerRunner interface {
	RunPlan(ctx context.Context, prompt string) (EditPlan, error)
}

type editPlannerRunnerFunc func(ctx context.Context, prompt string) (EditPlan, error)

func (f editPlannerRunnerFunc) RunPlan(ctx context.Context, prompt string) (EditPlan, error) {
	return f(ctx, prompt)
}

type editPlanner struct {
	runner editPlannerRunner
}

func (p *editPlanner) Plan(ctx context.Context, prompt string) (EditPlan, error) {
	if p == nil || p.runner == nil {
		return EditPlan{}, errors.New("edit planner is not configured")
	}
	return p.runner.RunPlan(ctx, prompt)
}

type editPlannerConfig struct {
	FoundryEndpoint   string
	ModelDeployment   string
	AgentName         string
	CredentialFactory func() (azcore.TokenCredential, error)
	AgentFactory      func(endpoint string, credential azcore.TokenCredential, modelDeployment, agentName, instructions string, obs *Observability) (editPlannerRunner, error)
	Observability     *Observability
}

func NewEditPlannerFromEnv(obs *Observability) (EditPlanner, error) {
	return newEditPlanner(editPlannerConfig{
		FoundryEndpoint: getRequiredEditPlannerEnv("FOUNDRY_PROJECT_ENDPOINT", "FOUNDRY_ENDPOINT", "AZURE_FOUNDRY_ENDPOINT"),
		ModelDeployment: getOptionalEditPlannerEnv("FOUNDRY_DEPLOYMENT_NAME", "AZURE_FOUNDRY_DEPLOYMENT_NAME"),
		Observability:   obs,
	})
}

func newEditPlanner(cfg editPlannerConfig) (EditPlanner, error) {
	cfg.FoundryEndpoint = strings.TrimSpace(cfg.FoundryEndpoint)
	cfg.ModelDeployment = strings.TrimSpace(cfg.ModelDeployment)
	cfg.AgentName = strings.TrimSpace(cfg.AgentName)

	if cfg.FoundryEndpoint == "" {
		return nil, errors.New("foundry endpoint is required")
	}
	if cfg.ModelDeployment == "" {
		return nil, errors.New("model deployment is required")
	}
	if err := validateFoundryProjectEndpoint(cfg.FoundryEndpoint); err != nil {
		return nil, err
	}
	if cfg.AgentName == "" {
		cfg.AgentName = defaultEditPlannerAgentName
	}
	if cfg.CredentialFactory == nil {
		cfg.CredentialFactory = defaultManagedIdentityCredentialFactory
	}
	if cfg.AgentFactory == nil {
		cfg.AgentFactory = newFoundryEditPlannerRunner
	}

	credential, err := cfg.CredentialFactory()
	if err != nil {
		return nil, fmt.Errorf("creating managed identity credential: %w", err)
	}
	runner, err := cfg.AgentFactory(cfg.FoundryEndpoint, credential, cfg.ModelDeployment, cfg.AgentName, editPlannerInstructions(), cfg.Observability)
	if err != nil {
		return nil, err
	}
	return &editPlanner{runner: runner}, nil
}

func defaultManagedIdentityCredentialFactory() (azcore.TokenCredential, error) {
	options := &azidentity.ManagedIdentityCredentialOptions{}
	if clientID := strings.TrimSpace(os.Getenv("AZURE_CLIENT_ID")); clientID != "" {
		options.ID = azidentity.ClientID(clientID)
	}
	return azidentity.NewManagedIdentityCredential(options)
}

func newFoundryEditPlannerRunner(endpoint string, credential azcore.TokenCredential, modelDeployment, agentName, instructions string, obs *Observability) (editPlannerRunner, error) {
	if credential == nil {
		return nil, errors.New("managed identity credential is required")
	}

	ag := foundryprovider.NewAgent(
		endpoint,
		credential,
		foundryprovider.ModelDeployment(modelDeployment),
		foundryprovider.AgentConfig{
			Instructions: instructions,
			Config: agent.Config{
				Name: agentName,
			},
		},
	)
	return foundryAgentRunner{agent: ag, obs: obs, modelDeployment: modelDeployment, promptVersion: editPlannerInstructionsVersion}, nil
}

type agentTextRunner interface {
	RunText(ctx context.Context, msg string, options ...agent.Option) agent.ResponseStream
}

type foundryAgentRunner struct {
	agent           agentTextRunner
	obs             *Observability
	modelDeployment string
	promptVersion   string
}

func (r foundryAgentRunner) RunPlan(ctx context.Context, prompt string) (plan EditPlan, err error) {
	if r.agent == nil {
		return EditPlan{}, errors.New("foundry agent is not configured")
	}
	start := time.Now()
	var span trace.Span
	if r.obs != nil {
		ctx, span = r.obs.StartSpan(ctx, "agent.run", attribute.String("stage", "agent.run"), attribute.String("model_deployment", r.modelDeployment), attribute.String("prompt_version", r.promptVersion))
		defer func() {
			r.obs.FinishSpan(ctx, span, "agent.run", start, []attribute.KeyValue{
				attribute.String("stage", "agent.run"),
				attribute.String("model_deployment", r.modelDeployment),
				attribute.String("prompt_version", r.promptVersion),
			}, err)
		}()
	}
	resp, err := r.agent.RunText(ctx, prompt, agent.WithStructuredOutput(&plan), agent.Stream(false)).Collect()
	if err != nil {
		return EditPlan{}, err
	}
	if r.obs != nil && resp != nil {
		usage := resp.Usage()
		r.obs.RecordModelUsage(ctx, r.modelDeployment, modelUsage{
			Input:     int64(usage.InputTokenCount),
			Output:    int64(usage.OutputTokenCount),
			Total:     int64(usage.TotalTokenCount),
			Reasoning: int64(usage.ReasoningTokenCount),
		}, attribute.String("prompt_version", r.promptVersion))
	}
	return plan, nil
}

func editPlannerInstructions() string {
	return `smart-edit-planner instructions v2
You are the smart-edit planner for AI Video Studio.
Use only the supplied JSON evidence packet. There is no userInstruction MVP.
Return a grounded EditPlan with titles, reasons, ordered clips, timecodes, and source refs that cite only known facts.
Do not invent facts, do not guess missing timecodes, do not cite unsupported source refs, and do not output anything except the structured EditPlan.`
}

func validateAbsoluteHTTPSURL(raw, field string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", field)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("%s must use https", field)
	}
	return nil
}

func validateFoundryProjectEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("foundry endpoint must be an absolute URL")
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("foundry endpoint must use https")
	}
	host := strings.ToLower(parsed.Host)
	labels := strings.Split(host, ".")
	if len(labels) != 5 || labels[1] != "services" || labels[2] != "ai" || labels[3] != "azure" || labels[4] != "com" || strings.TrimSpace(labels[0]) == "" {
		return fmt.Errorf("foundry endpoint must be a Foundry project endpoint")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("foundry endpoint must not include query parameters or fragments")
	}
	segments := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(segments) != 3 || segments[0] != "api" || segments[1] != "projects" || strings.TrimSpace(segments[2]) == "" {
		return fmt.Errorf("foundry endpoint must match https://<resource>.services.ai.azure.com/api/projects/<project>")
	}
	return nil
}

func getRequiredEditPlannerEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func getOptionalEditPlannerEnv(keys ...string) string {
	if value := getRequiredEditPlannerEnv(keys...); value != "" {
		return value
	}
	return defaultEditPlannerModelDeployment
}
