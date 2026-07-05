package library

import (
	"context"
	"time"
)

type ProjectAsset struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	CloudAssetID   string    `json:"cloudAssetId"`
	SizeBytes      int64     `json:"sizeBytes"`
	ContentType    string    `json:"contentType,omitempty"`
	AnalysisJobID  string    `json:"analysisJobId,omitempty"`
	AnalysisStatus string    `json:"analysisStatus"`
	AnalysisScenes int       `json:"analysisScenes"`
	CreatedAt      time.Time `json:"createdAt"`
}

type ProjectLibrary struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Assets    []ProjectAsset `json:"assets"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

type Service interface {
	Current(context.Context) (ProjectLibrary, error)
	ListAssets(context.Context) ([]ProjectAsset, error)
}
