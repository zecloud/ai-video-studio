package main

import "context"

// JobService is the API-facing contract. Its implementations keep JobDocument
// as the public state projection regardless of the execution engine.
type JobService interface {
	CreateJob(context.Context, CreateIndexJobRequest) (Job, error)
	GetJob(context.Context, string) (Job, error)
	ListJobs(context.Context, JobStatus) ([]Job, error)
	CancelJob(context.Context, string) (Job, error)
}
