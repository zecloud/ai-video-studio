package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
)

type RenderJobStore interface {
	Create(context.Context, RenderJobDocument) error
	Get(context.Context, string) (StoredRenderJob, error)
	List(context.Context) ([]StoredRenderJob, error)
	Update(context.Context, RenderJobDocument, string) error
}
type AzureRenderJobStore struct {
	client    *azblob.Client
	container string
}

func NewAzureRenderJobStore(client *azblob.Client, container string) *AzureRenderJobStore {
	return &AzureRenderJobStore{client: client, container: strings.TrimSpace(container)}
}
func (s *AzureRenderJobStore) blobName(id string) string { return path.Join("render-jobs", id+".json") }
func (s *AzureRenderJobStore) Create(ctx context.Context, job RenderJobDocument) error {
	return s.write(ctx, job, "", true)
}
func (s *AzureRenderJobStore) Update(ctx context.Context, job RenderJobDocument, etag string) error {
	return s.write(ctx, job, etag, false)
}
func (s *AzureRenderJobStore) write(ctx context.Context, job RenderJobDocument, etag string, create bool) error {
	if err := job.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	conditions := &blob.ModifiedAccessConditions{}
	if create {
		conditions.IfNoneMatch = ptr(azcore.ETag("*"))
	} else {
		conditions.IfMatch = ptr(azcore.ETag(etag))
	}
	_, err = s.client.UploadStream(ctx, s.container, s.blobName(job.ID), bytes.NewReader(data), &azblob.UploadStreamOptions{AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: conditions}})
	if create {
		return classifyBlobError(err, http.StatusConflict, "render_job_exists", "render job already exists", false)
	}
	return classifyBlobError(err, http.StatusConflict, "etag_conflict", "render job update conflict", true)
}
func (s *AzureRenderJobStore) Get(ctx context.Context, id string) (StoredRenderJob, error) {
	if err := validateID(id, "jobID"); err != nil {
		return StoredRenderJob{}, newServiceError(http.StatusBadRequest, "validation_failed", err.Error(), false)
	}
	resp, err := s.client.DownloadStream(ctx, s.container, s.blobName(id), nil)
	if err != nil {
		return StoredRenderJob{}, classifyBlobError(err, http.StatusNotFound, "render_job_not_found", "render job not found", false)
	}
	defer resp.Body.Close()
	var job RenderJobDocument
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return StoredRenderJob{}, newServiceError(http.StatusInternalServerError, "render_job_decode_failed", err.Error(), false)
	}
	if err := job.Validate(); err != nil {
		return StoredRenderJob{}, newServiceError(http.StatusInternalServerError, "render_job_document_invalid", err.Error(), false)
	}
	etag := ""
	if resp.ETag != nil {
		etag = string(*resp.ETag)
	}
	return StoredRenderJob{RenderJobDocument: job, ETag: etag}, nil
}
func (s *AzureRenderJobStore) List(ctx context.Context) ([]StoredRenderJob, error) {
	pager := s.client.NewListBlobsFlatPager(s.container, &azblob.ListBlobsFlatOptions{Prefix: ptr("render-jobs/")})
	var jobs []StoredRenderJob
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, newServiceError(http.StatusInternalServerError, "render_job_list_failed", err.Error(), true)
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name == nil || !strings.HasPrefix(*item.Name, "render-jobs/") || !strings.HasSuffix(*item.Name, ".json") {
				continue
			}
			job, err := s.Get(ctx, strings.TrimSuffix(path.Base(*item.Name), ".json"))
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, job)
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].CreatedAt.Before(jobs[j].CreatedAt) })
	return jobs, nil
}
