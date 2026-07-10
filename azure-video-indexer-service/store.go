package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
)

type JobStore interface {
	Create(ctx context.Context, job JobDocument) error
	Get(ctx context.Context, jobID string) (StoredJob, error)
	List(ctx context.Context) ([]StoredJob, error)
	Update(ctx context.Context, job JobDocument, etag string) error
}

type AzureJobStore struct {
	client    *azblob.Client
	container string
}

func NewAzureJobStore(client *azblob.Client, container string) *AzureJobStore {
	return &AzureJobStore{client: client, container: strings.TrimSpace(container)}
}

func (s *AzureJobStore) Create(ctx context.Context, job JobDocument) error {
	if err := job.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = s.client.UploadStream(ctx, s.container, s.blobName(job.ID), bytes.NewReader(data), &azblob.UploadStreamOptions{
		AccessConditions: &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{
				IfNoneMatch: ptr(azcore.ETag("*")),
			},
		},
	})
	if err != nil {
		return classifyBlobError(err, http.StatusConflict, "job_exists", "job already exists", false)
	}
	return nil
}

func (s *AzureJobStore) Get(ctx context.Context, jobID string) (StoredJob, error) {
	if err := validateID(jobID, "jobID"); err != nil {
		return StoredJob{}, newServiceError(http.StatusBadRequest, "validation_failed", err.Error(), false)
	}
	resp, err := s.client.DownloadStream(ctx, s.container, s.blobName(jobID), nil)
	if err != nil {
		return StoredJob{}, classifyBlobError(err, http.StatusNotFound, "job_not_found", "job not found", false)
	}
	defer resp.Body.Close()
	var job JobDocument
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return StoredJob{}, &ServiceError{Status: http.StatusInternalServerError, Code: "job_decode_failed", Message: err.Error(), Retryable: false, Cause: err}
	}
	if err := job.Validate(); err != nil {
		return StoredJob{}, &ServiceError{Status: http.StatusInternalServerError, Code: "job_document_invalid", Message: err.Error(), Retryable: false, Cause: err}
	}
	etag := ""
	if resp.ETag != nil {
		etag = string(*resp.ETag)
	}
	return StoredJob{JobDocument: job, ETag: etag}, nil
}

func (s *AzureJobStore) List(ctx context.Context) ([]StoredJob, error) {
	pager := s.client.NewListBlobsFlatPager(s.container, nil)
	var jobs []StoredJob
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, &ServiceError{Status: http.StatusInternalServerError, Code: "job_list_failed", Message: err.Error(), Retryable: true, Cause: err}
		}
		for _, item := range page.Segment.BlobItems {
			if item.Name == nil || !strings.HasPrefix(*item.Name, "jobs/") || !strings.HasSuffix(*item.Name, ".json") {
				continue
			}
			job, err := s.Get(ctx, strings.TrimSuffix(path.Base(*item.Name), ".json"))
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, job)
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (s *AzureJobStore) Update(ctx context.Context, job JobDocument, etag string) error {
	if err := job.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	_, err = s.client.UploadStream(ctx, s.container, s.blobName(job.ID), bytes.NewReader(data), &azblob.UploadStreamOptions{
		AccessConditions: &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{
				IfMatch: ptr(azcore.ETag(etag)),
			},
		},
	})
	if err != nil {
		return classifyBlobError(err, http.StatusConflict, "etag_conflict", "job update conflict", true)
	}
	return nil
}

func (s *AzureJobStore) blobName(jobID string) string {
	return path.Join("jobs", jobID+".json")
}

func classifyBlobError(err error, status int, code, message string, retryable bool) error {
	if err == nil {
		return nil
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		if respErr.StatusCode == status {
			return &ServiceError{Status: status, Code: code, Message: message, Retryable: retryable, Cause: err}
		}
		if respErr.StatusCode == http.StatusPreconditionFailed && status == http.StatusConflict {
			return &ServiceError{Status: status, Code: code, Message: message, Retryable: retryable, Cause: err}
		}
		if respErr.StatusCode == http.StatusNotFound && status == http.StatusNotFound {
			return &ServiceError{Status: status, Code: code, Message: message, Retryable: retryable, Cause: err}
		}
	}
	return &ServiceError{Status: http.StatusInternalServerError, Code: "storage_error", Message: err.Error(), Retryable: true, Cause: err}
}

func readAllAndClose(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	return io.ReadAll(rc)
}
