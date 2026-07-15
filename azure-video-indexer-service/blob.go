package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type StagedAsset struct {
	Container string
	BlobName  string
	Size      int64
	MediaType string
}

type StageOptions struct {
	ContentLength int64
	ContentType   string
}

type BlobStager interface {
	Stage(ctx context.Context, jobID, sourceName string, body io.Reader, options StageOptions) (StagedAsset, error)
	ReadURL(ctx context.Context, asset StagedAsset) (string, error)
	Delete(ctx context.Context, asset StagedAsset) error
	StagingContainer() string
}

type RenderBlobStager interface {
	StageNamed(ctx context.Context, blobName string, body io.Reader, options StageOptions) (StagedAsset, error)
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

func (s *AzureBlobService) Stage(ctx context.Context, jobID, sourceName string, body io.Reader, options StageOptions) (asset StagedAsset, err error) {
	return s.StageNamed(ctx, stageBlobName(jobID, sourceName), body, options)
}

func (s *AzureBlobService) StageNamed(ctx context.Context, blobName string, body io.Reader, options StageOptions) (asset StagedAsset, err error) {
	asset = StagedAsset{
		Container: s.stagingContainer,
		BlobName:  strings.TrimSpace(blobName),
	}
	if asset.BlobName == "" {
		return StagedAsset{}, newServiceError(422, "blob_name_required", "staged blob name is required", false)
	}
	if options.ContentLength == 0 {
		return StagedAsset{}, newServiceError(422, "source_media_empty", "OneDrive returned an empty media file", false)
	}
	contentType, err := mediaContentType(asset.BlobName, options.ContentType)
	if err != nil {
		return StagedAsset{}, err
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
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &contentType},
	}); err != nil {
		err = classifyAzureBlobOperation(err, "blob_stage_failed", fmt.Sprintf("staging blob %s failed", asset.BlobName))
		return StagedAsset{}, err
	}
	defer func() {
		if err == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := s.Delete(cleanupCtx, asset); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cleaning invalid staged blob %s: %w", asset.BlobName, cleanupErr))
		}
	}()
	properties, err := s.client.ServiceClient().NewContainerClient(asset.Container).NewBlobClient(asset.BlobName).GetProperties(ctx, nil)
	if err != nil {
		return StagedAsset{}, classifyAzureBlobOperation(err, "blob_properties_failed", fmt.Sprintf("checking staged blob %s failed", asset.BlobName))
	}
	if properties.ContentLength == nil || *properties.ContentLength <= 0 {
		return StagedAsset{}, newServiceError(502, "staged_blob_empty", "staged media blob is empty", true)
	}
	if options.ContentLength > 0 && *properties.ContentLength != options.ContentLength {
		return StagedAsset{}, newServiceError(502, "staged_blob_size_mismatch", fmt.Sprintf("staged media size %d does not match OneDrive size %d", *properties.ContentLength, options.ContentLength), true)
	}
	asset.Size = *properties.ContentLength
	asset.MediaType = contentType
	return asset, nil
}

func mediaContentType(sourceName, contentType string) (string, error) {
	contentType = strings.TrimSpace(contentType)
	if contentType != "" {
		parsed, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return "", newServiceError(422, "source_media_type_invalid", "OneDrive returned an invalid media content type", false)
		}
		if strings.HasPrefix(strings.ToLower(parsed), "video/") {
			return parsed, nil
		}
		if !strings.EqualFold(parsed, "application/octet-stream") {
			return "", newServiceError(422, "source_media_type_unsupported", "OneDrive item is not a supported video", false)
		}
	}
	if inferred := mime.TypeByExtension(strings.ToLower(filepath.Ext(sourceName))); strings.HasPrefix(strings.ToLower(inferred), "video/") {
		parsed, _, _ := mime.ParseMediaType(inferred)
		return parsed, nil
	}
	return "", newServiceError(422, "source_media_type_unsupported", "OneDrive item is not a supported video", false)
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

type BlobDownload struct {
	Body          io.ReadCloser
	ContentLength int64
	ContentType   string
	ETag          string
}

func (s *AzureBlobService) OpenDownload(ctx context.Context, asset StagedAsset) (BlobDownload, error) {
	if s == nil || s.client == nil {
		return BlobDownload{}, newServiceError(503, "blob_client_unavailable", "blob client is not configured", true)
	}
	if strings.TrimSpace(asset.Container) == "" || strings.TrimSpace(asset.BlobName) == "" {
		return BlobDownload{}, newServiceError(422, "blob_identity_invalid", "blob container and name are required", false)
	}
	response, err := s.client.DownloadStream(ctx, asset.Container, asset.BlobName, nil)
	if err != nil {
		return BlobDownload{}, classifyAzureBlobOperation(err, "blob_download_failed", fmt.Sprintf("downloading blob %s failed", asset.BlobName))
	}
	download := BlobDownload{Body: response.Body}
	if response.ContentLength != nil {
		download.ContentLength = *response.ContentLength
	}
	if response.ContentType != nil {
		download.ContentType = *response.ContentType
	}
	if response.ETag != nil {
		download.ETag = string(*response.ETag)
	}
	return download, nil
}

func (s *AzureBlobService) Delete(ctx context.Context, asset StagedAsset) error {
	if asset.Container == "" || asset.BlobName == "" {
		return nil
	}
	_, err := s.client.DeleteBlob(ctx, asset.Container, asset.BlobName, nil)
	if err == nil || isNotFound(err) {
		return nil
	}
	return classifyAzureBlobOperation(err, "blob_delete_failed", fmt.Sprintf("deleting blob %s failed", asset.BlobName))
}

func (s *AzureBlobService) DownloadToFile(ctx context.Context, asset StagedAsset, destination string) (err error) {
	download, err := s.OpenDownload(ctx, asset)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := download.Body.Close(); err == nil && closeErr != nil {
			err = classifyAzureBlobOperation(closeErr, "blob_download_failed", fmt.Sprintf("closing blob %s download failed", asset.BlobName))
		}
	}()
	file, err := os.Create(destination)
	if err != nil {
		return newServiceError(500, "render_input_create_failed", "creating render input file failed", false)
	}
	complete := false
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = newServiceError(500, "render_input_close_failed", "closing render input file failed", false)
		}
		if !complete || err != nil {
			_ = os.Remove(destination)
		}
	}()
	if _, err = io.Copy(file, download.Body); err != nil {
		return classifyAzureBlobOperation(err, "blob_download_failed", fmt.Sprintf("reading blob %s failed", asset.BlobName))
	}
	complete = true
	return nil
}

func (s *AzureBlobService) UploadFile(ctx context.Context, asset StagedAsset, source, contentType string) (size int64, err error) {
	if s == nil || s.client == nil {
		return 0, newServiceError(503, "blob_client_unavailable", "blob client is not configured", true)
	}
	file, err := os.Open(source)
	if err != nil {
		return 0, newServiceError(500, "render_output_open_failed", "opening render output file failed", false)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = newServiceError(500, "render_output_close_failed", "closing render output file failed", false)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return 0, newServiceError(500, "render_output_stat_failed", "stating render output file failed", false)
	}
	if info.Size() <= 0 {
		return 0, newServiceError(422, "render_output_empty", "render output file is empty", false)
	}
	_, err = s.client.UploadStream(ctx, asset.Container, asset.BlobName, file, &azblob.UploadStreamOptions{
		BlockSize:   4 << 20,
		Concurrency: 2,
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: ptr(contentType)},
	})
	if err != nil {
		return 0, classifyAzureBlobOperation(err, "blob_upload_failed", fmt.Sprintf("uploading render output %s failed", asset.BlobName))
	}
	return info.Size(), nil
}
func (s *AzureBlobService) stagedAsset(jobID, sourceName string) StagedAsset {
	return StagedAsset{
		Container: s.StagingContainer(),
		BlobName:  stageBlobName(jobID, sourceName),
	}
}
