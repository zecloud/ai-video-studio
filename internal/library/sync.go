package library

import (
	"context"
	"fmt"
	"strings"

	"github.com/zecloud/ai-video-studio/internal/onedrive"
)

// DriveItemProvider abstracts the OneDrive client for folder scanning.
type DriveItemProvider interface {
	ListFolderItems(ctx context.Context, folderPath string) ([]onedrive.DriveItem, error)
}

// SyncAssetsWithOneDrive compares the store against OneDrive folder contents and
// adds any assets that exist in OneDrive but are not yet registered locally.
// Deduplication is by DriveItemID.
func SyncAssetsWithOneDrive(ctx context.Context, store Store, client DriveItemProvider, folderPath string) (added int, err error) {
	if store == nil {
		return 0, fmt.Errorf("store is nil")
	}
	if client == nil {
		return 0, fmt.Errorf("onedrive client is nil")
	}

	normalized := normalizeFolderPath(folderPath)
	items, err := client.ListFolderItems(ctx, normalized)
	if err != nil {
		return 0, fmt.Errorf("sync: list folder: %w", err)
	}

	existing, err := store.LoadAssets(ctx)
	if err != nil {
		return 0, fmt.Errorf("sync: load store: %w", err)
	}

	knownIDs := make(map[string]bool, len(existing))
	for _, a := range existing {
		if a.CloudAssetID != "" {
			knownIDs[a.CloudAssetID] = true
		}
	}

	for _, item := range items {
		if item.File == nil {
			continue // skip folders
		}
		if knownIDs[item.ID] {
			continue // already registered
		}
		asset := ProjectAsset{
			Name:         item.Name,
			CloudAssetID: item.ID,
			SizeBytes:    item.Size,
			ContentType:  item.File.MimeType,
		}
		if err := store.AddAsset(ctx, asset); err != nil {
			return added, fmt.Errorf("sync: add asset %q: %w", item.Name, err)
		}
		added++
	}

	// Remove assets whose CloudAssetID is no longer present in OneDrive.
	liveIDs := make(map[string]bool, len(items))
	for _, item := range items {
		if item.File != nil {
			liveIDs[item.ID] = true
		}
	}
	pruned := make([]ProjectAsset, 0, len(existing))
	for _, a := range existing {
		if a.CloudAssetID == "" || liveIDs[a.CloudAssetID] {
			pruned = append(pruned, a)
		}
	}
	if len(pruned) != len(existing) {
		if saveErr := store.SaveAssets(ctx, pruned); saveErr != nil {
			return added, fmt.Errorf("sync: prune removed assets: %w", saveErr)
		}
	}

	return added, nil
}

func normalizeFolderPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	return strings.ReplaceAll(p, "\\", "/")
}
