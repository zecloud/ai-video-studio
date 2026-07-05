package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
)

// sasValidity is how long a generated SAS URL remains usable. Two hours is
// enough headroom for Azure Content Understanding to fetch and analyze the
// staged media.
const sasValidity = 2 * time.Hour

// BlobStore wraps Azure Blob Storage access for the media staging service.
// It authenticates exclusively via Managed Identity / DefaultAzureCredential
// so no storage account keys ever need to live in configuration.
type BlobStore struct {
	client            *azblob.Client
	defaultContainer  string
	credential        azcore.TokenCredential
	storageAccountURL string
}

// NewBlobStore builds a BlobStore for the given configuration, authenticating
// with DefaultAzureCredential (Managed Identity in Azure, developer
// credentials locally).
func NewBlobStore(cfg *Config) (*BlobStore, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("creating Azure credential: %w", err)
	}

	serviceURL := cfg.ServiceURL()
	client, err := azblob.NewClient(serviceURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("creating blob client: %w", err)
	}

	return &BlobStore{
		client:            client,
		defaultContainer:  cfg.ContainerName,
		credential:        cred,
		storageAccountURL: serviceURL,
	}, nil
}

// containerOrDefault returns the requested container name, falling back to
// the configured default when the caller did not specify one.
func (b *BlobStore) containerOrDefault(container string) string {
	if container == "" {
		return b.defaultContainer
	}
	return container
}

// UploadStream streams reader content directly into a block blob without
// buffering the full payload on local disk. size may be -1/0 if unknown;
// the SDK will still perform a streaming upload using internal buffering in
// memory.
func (b *BlobStore) UploadStream(ctx context.Context, container, blobName string, reader io.Reader) error {
	container = b.containerOrDefault(container)

	_, err := b.client.UploadStream(ctx, container, blobName, reader, nil)
	if err != nil {
		return fmt.Errorf("uploading blob %q/%q: %w", container, blobName, err)
	}
	return nil
}

// DeleteBlob removes a blob from the given container (or the default
// container when none is specified).
func (b *BlobStore) DeleteBlob(ctx context.Context, container, blobName string) error {
	container = b.containerOrDefault(container)

	_, err := b.client.DeleteBlob(ctx, container, blobName, nil)
	if err != nil {
		return fmt.Errorf("deleting blob %q/%q: %w", container, blobName, err)
	}
	return nil
}

// BlobURL returns the non-SAS canonical URL for a blob.
func (b *BlobStore) BlobURL(container, blobName string) string {
	container = b.containerOrDefault(container)
	return fmt.Sprintf("%s%s/%s", b.storageAccountURL, container, blobName)
}

// GenerateSASURL creates a short-lived, read-only user delegation SAS URL
// for the given blob. User delegation SAS is used instead of an account key
// SAS because the service authenticates purely via Managed Identity.
func (b *BlobStore) GenerateSASURL(ctx context.Context, container, blobName string) (string, error) {
	container = b.containerOrDefault(container)

	serviceClient, err := service.NewClient(b.storageAccountURL, b.credential, nil)
	if err != nil {
		return "", fmt.Errorf("creating service client for SAS: %w", err)
	}

	now := time.Now().UTC().Add(-5 * time.Minute) // clock skew allowance
	expiry := time.Now().UTC().Add(sasValidity)

	udc, err := serviceClient.GetUserDelegationCredential(ctx, service.KeyInfo{
		Start:  toPtr(now.Format(sas.TimeFormat)),
		Expiry: toPtr(expiry.Format(sas.TimeFormat)),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("requesting user delegation key: %w", err)
	}

	sasParams, err := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPS,
		StartTime:     now,
		ExpiryTime:    expiry,
		Permissions:   toPtr(sas.BlobPermissions{Read: true}).String(),
		ContainerName: container,
		BlobName:      blobName,
	}.SignWithUserDelegation(udc)
	if err != nil {
		return "", fmt.Errorf("signing SAS: %w", err)
	}

	blobURL := b.BlobURL(container, blobName)
	return fmt.Sprintf("%s?%s", blobURL, sasParams.Encode()), nil
}

// blobClient returns a client scoped to a specific blob, useful for
// operations that need blob-level APIs beyond what azblob.Client exposes
// directly.
func (b *BlobStore) blobClient(container, blobName string) *blob.Client {
	container = b.containerOrDefault(container)
	return b.client.ServiceClient().NewContainerClient(container).NewBlobClient(blobName)
}

func toPtr[T any](v T) *T {
	return &v
}
