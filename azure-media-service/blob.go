package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

// sasValidity is how long a generated SAS URL remains usable. Two hours is
// enough headroom for Azure Content Understanding to fetch and analyze the
// staged media.
const sasValidity = 2 * time.Hour

// BlobStore wraps Azure Blob Storage access for the media staging service.
type BlobStore struct {
	client            *azblob.Client
	defaultContainer  string
	sharedKey         *azblob.SharedKeyCredential
	storageAccountURL string
}

// NewBlobStore builds a BlobStore from an Azure Storage connection string.
func NewBlobStore(cfg *Config) (*BlobStore, error) {
	client, err := azblob.NewClientFromConnectionString(cfg.StorageConnectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("creating blob client from connection string: %w", err)
	}

	accountName, accountKey, serviceURL, err := parseStorageConnectionString(cfg.StorageConnectionString)
	if err != nil {
		return nil, err
	}

	sharedKey, err := azblob.NewSharedKeyCredential(accountName, accountKey)
	if err != nil {
		return nil, fmt.Errorf("creating shared key credential: %w", err)
	}

	return &BlobStore{
		client:            client,
		defaultContainer:  cfg.ContainerName,
		sharedKey:         sharedKey,
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

// GenerateSASURL creates a short-lived, read-only shared-key SAS URL for the
// given blob.
func (b *BlobStore) GenerateSASURL(ctx context.Context, container, blobName string) (string, error) {
	_ = ctx
	container = b.containerOrDefault(container)

	now := time.Now().UTC().Add(-5 * time.Minute) // clock skew allowance
	expiry := time.Now().UTC().Add(sasValidity)

	sasParams, err := sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPS,
		StartTime:     now,
		ExpiryTime:    expiry,
		Permissions:   toPtr(sas.BlobPermissions{Read: true}).String(),
		ContainerName: container,
		BlobName:      blobName,
	}.SignWithSharedKey(b.sharedKey)
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

func parseStorageConnectionString(connectionString string) (accountName string, accountKey string, serviceURL string, err error) {
	parts := strings.Split(connectionString, ";")
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}

	accountName = values["accountname"]
	if accountName == "" {
		return "", "", "", fmt.Errorf("STORAGE_CONNECTION_STRING must include AccountName")
	}
	accountKey = values["accountkey"]
	if accountKey == "" {
		return "", "", "", fmt.Errorf("STORAGE_CONNECTION_STRING must include AccountKey")
	}

	if blobEndpoint := values["blobendpoint"]; blobEndpoint != "" {
		u, parseErr := url.Parse(blobEndpoint)
		if parseErr != nil || u.Scheme == "" || u.Host == "" {
			return "", "", "", fmt.Errorf("invalid BlobEndpoint in STORAGE_CONNECTION_STRING")
		}
		return accountName, accountKey, ensureTrailingSlash(blobEndpoint), nil
	}

	protocol := values["defaultendpointsprotocol"]
	if protocol == "" {
		protocol = "https"
	}
	endpointSuffix := values["endpointsuffix"]
	if endpointSuffix == "" {
		endpointSuffix = "core.windows.net"
	}

	return accountName, accountKey, fmt.Sprintf("%s://%s.blob.%s/", protocol, accountName, endpointSuffix), nil
}

func ensureTrailingSlash(value string) string {
	if strings.HasSuffix(value, "/") {
		return value
	}
	return value + "/"
}
