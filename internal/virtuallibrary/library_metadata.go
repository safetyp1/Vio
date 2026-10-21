package virtuallibrary

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/monitor"
)

type LibraryMetadataResolver interface {
	FetchLibraryMetadata(context.Context, int, *models.MediaItem) (*metadata.MetadataResult, []metadata.EpisodeResult, error)
}

func (s *Service) SetLibraryMetadataResolver(resolver LibraryMetadataResolver) {
	s.Monitor.SetMetadataResolver(func(ctx context.Context, item monitor.MonitoredMedia) (monitor.MonitoredMedia, error) {
		if resolver == nil {
			return item, fmt.Errorf("library metadata service is unavailable")
		}
		folderID := item.MediaFolderID
		if folderID <= 0 {
			if item.MediaType == "series" {
				folderID = s.cfg.SeriesLibraryID
			} else {
				folderID = s.cfg.MovieLibraryID
			}
		}
		result, episodes, err := resolver.FetchLibraryMetadata(ctx, folderID, &models.MediaItem{
			Type: item.MediaType, Title: item.Title, Year: int(item.Year),
			TmdbID: item.TMDBID, TvdbID: item.TVDBID, ImdbID: item.IMDbID,
		})
		if err != nil {
			return item, err
		}
		if result == nil {
			return item, metadata.ErrMetadataNotFound
		}
		if result.Title != "" {
			item.Title = result.Title
		}
		if result.Overview != "" {
			item.Overview = result.Overview
		}
		if result.Runtime > 0 {
			item.Runtime = result.Runtime
		}
		if len(result.Genres) > 0 {
			item.Genres = append([]string(nil), result.Genres...)
		}
		if item.TMDBID == "" {
			item.TMDBID = result.ProviderIDs["tmdb"]
		}
		if item.TVDBID == "" {
			item.TVDBID = result.ProviderIDs["tvdb"]
		}
		if item.IMDbID == "" {
			item.IMDbID = result.ProviderIDs["imdb"]
		}
		if item.MediaType == "series" {
			fresh := make([]monitor.VirtualEpisode, 0, len(episodes))
			for _, episode := range episodes {
				if episode.SeasonNumber <= 0 || episode.EpisodeNumber <= 0 {
					continue
				}
				if strings.TrimSpace(episode.Title) == "" {
					return item, fmt.Errorf("episode metadata is missing a title")
				}
				released, err := time.Parse(time.DateOnly, episode.AirDate)
				if err != nil && episode.AirDate != "" {
					released, err = time.Parse(time.RFC3339, episode.AirDate)
					if err != nil {
						return item, fmt.Errorf("invalid episode air date: %w", err)
					}
				}
				fresh = append(fresh, monitor.VirtualEpisode{
					Season: episode.SeasonNumber, Episode: episode.EpisodeNumber,
					Title: episode.Title, Overview: episode.Overview,
					Runtime: episode.Runtime, Thumbnail: episode.StillPath, Released: released,
				})
			}
			if len(fresh) == 0 {
				return item, metadata.ErrMetadataNotFound
			}
			item.Episodes = fresh
		}
		item.MediaFolderID = folderID
		return item, nil
	})
}
