package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type StagedAsset struct {
	Container string
	BlobName  string
}

type BlobStager interface {
	Stage(ctx context.Context, jobID, sourceName string, body io.Reader) (StagedAsset, error)
	ReadURL(ctx context.Context, asset StagedAsset) (string, error)
	Delete(ctx context.Context, asset StagedAsset) error
	StagingContainer() string
}

type AzureBlobService struct {
	client           *azblob.Client
	serviceClient    *service.Client
	accountURL       string
	stagingContainer string
	jobContainer     string
	sasValidity      time.Duration
	obs              *Observability
}

func NewAzureBlobService(accountURL string, cred azcore.TokenCredential, stagingContainer, jobContainer string, sasValidity time.Duration) (*AzureBlobService, error) {
	accountURL = strings.TrimRight(strings.TrimSpace(accountURL), "/")
	if accountURL == "" {
		return nil, fmt.Errorf("AZURE_STORAGE_URL is required")
	}
	if cred == nil {
		return nil, fmt.Errorf("storage credential is required")
	}
	client, err := azblob.NewClient(accountURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("creating blob client: %w", err)
	}
	svcClient, err := service.NewClient(accountURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("creating blob service client: %w", err)
	}
	return &AzureBlobService{
		client:           client,
		serviceClient:    svcClient,
		accountURL:       accountURL,
		stagingContainer: strings.TrimSpace(stagingContainer),
		jobContainer:     strings.TrimSpace(jobContainer),
		sasValidity:      sasValidity,
	}, nil
}

func NewAzureBlobServiceFromDefaultCredential(cfg Config) (*AzureBlobService, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("creating Azure credential: %w", err)
	}
	return NewAzureBlobService(cfg.StorageURL, cred, cfg.StagingContainer, cfg.JobContainer, cfg.SASValidity)
}

func (s *AzureBlobService) Client() *azblob.Client {
	return s.client
}

func (s *AzureBlobService) StagingContainer() string {
	return s.stagingContainer
}

func (s *AzureBlobService) CheckContainer(ctx context.Context, container string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.client == nil {
		return fmt.Errorf("blob client is not configured")
	}
	container = strings.TrimSpace(container)
	if container == "" {
		return fmt.Errorf("container name is required")
	}
	if _, err := s.client.ServiceClient().NewContainerClient(container).GetProperties(ctx, nil); err != nil {
		return fmt.Errorf("checking container %s: %w", container, err)
	}
	return nil
}

func (s *AzureBlobService) CheckUserDelegationCredential(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.serviceClient == nil {
		return fmt.Errorf("blob service client is not configured")
	}
	now := time.Now().UTC()
	info := service.KeyInfo{
		Start:  ptr(now.Add(-5 * time.Minute).Format(sas.TimeFormat)),
		Expiry: ptr(now.Add(5 * time.Minute).Format(sas.TimeFormat)),
	}
	if _, err := s.serviceClient.GetUserDelegationCredential(ctx, info, nil); err != nil {
		return fmt.Errorf("checking user delegation credential: %w", err)
	}
	return nil
}

func (s *AzureBlobService) Stage(ctx context.Context, jobID, sourceName string, body io.Reader) (asset StagedAsset, err error) {
	asset = StagedAsset{
		Container: s.stagingContainer,
		BlobName:  stageBlobName(jobID, sourceName),
	}
	start := time.Now()
	var span trace.Span
	if s.obs != nil {
		ctx, span = s.obs.StartSpan(ctx, "blob.stage", attribute.String("stage", "blob.stage"))
		defer func() {
			s.obs.FinishSpan(ctx, span, "blob.stage", start, []attribute.KeyValue{attribute.String("stage", "blob.stage")}, err)
		}()
	}
	if _, err = s.client.UploadStream(ctx, asset.Container, asset.BlobName, body, &azblob.UploadStreamOptions{
		BlockSize:   4 << 20,
		Concurrency: 2,
	}); err != nil {
		err = fmt.Errorf("staging blob %s: %w", asset.BlobName, err)
		return StagedAsset{}, err
	}
	return asset, nil
}

func (s *AzureBlobService) ReadURL(ctx context.Context, asset StagedAsset) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	start := now.Add(-5 * time.Minute)
	expiry := now.Add(s.sasValidity)
	info := service.KeyInfo{
		Start:  ptr(start.Format(sas.TimeFormat)),
		Expiry: ptr(expiry.Format(sas.TimeFormat)),
	}
	udc, err := s.serviceClient.GetUserDelegationCredential(ctx, info, nil)
	if err != nil {
		return "", fmt.Errorf("getting user delegation credential: %w", err)
	}
	signed, err := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPS,
		StartTime:     start,
		ExpiryTime:    expiry,
		Permissions:   ptr(sas.BlobPermissions{Read: true}).String(),
		ContainerName: asset.Container,
		BlobName:      asset.BlobName,
	}.SignWithUserDelegation(udc)
	if err != nil {
		return "", fmt.Errorf("signing user delegation SAS: %w", err)
	}
	base, err := url.Parse(s.accountURL)
	if err != nil {
		return "", fmt.Errorf("parsing storage URL: %w", err)
	}
	base.Path = path.Join(base.Path, asset.Container, asset.BlobName)
	base.RawQuery = signed.Encode()
	return base.String(), nil
}

func (s *AzureBlobService) Delete(ctx context.Context, asset StagedAsset) error {
	if asset.Container == "" || asset.BlobName == "" {
		return nil
	}
	_, err := s.client.DeleteBlob(ctx, asset.Container, asset.BlobName, nil)
	if err == nil || isNotFound(err) {
		return nil
	}
	return fmt.Errorf("deleting staged blob %s: %w", asset.BlobName, err)
}

func (s *AzureBlobService) stagedAsset(jobID, sourceName string) StagedAsset {
	return StagedAsset{
		Container: s.StagingContainer(),
		BlobName:  stageBlobName(jobID, sourceName),
	}
}
