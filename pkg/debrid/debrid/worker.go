package debrid

import (
	"context"
	"github.com/sirrobot01/decypharr/internal/utils"
	"time"
)

func (c *Cache) StartSchedule() error {
	// For now, we just want to refresh the listing and download links
	ctx := context.Background()
	downloadLinkJob, err := utils.ScheduleJob(ctx, c.downloadLinksRefreshInterval, nil, c.refreshDownloadLinks)
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to add download link refresh job")
	}
	if t, err := downloadLinkJob.NextRun(); err == nil {
		c.logger.Trace().Msgf("Next download link refresh job: %s", t.Format("2006-01-02 15:04:05"))
	}

	torrentJob, err := utils.ScheduleJob(ctx, c.torrentRefreshInterval, nil, c.refreshTorrents)
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to add torrent refresh job")
	}
	if t, err := torrentJob.NextRun(); err == nil {
		c.logger.Trace().Msgf("Next torrent refresh job: %s", t.Format("2006-01-02 15:04:05"))
	}

	// Schedule the reset invalid links job
	// This job will run every 24 hours
	// and reset the invalid links in the cache
	cet, _ := time.LoadLocation("CET")
	resetLinksJob, err := utils.ScheduleJob(ctx, "00:00", cet, c.resetInvalidLinks)
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to add reset invalid links job")
	}
	if t, err := resetLinksJob.NextRun(); err == nil {
		c.logger.Trace().Msgf("Next reset invalid download links job at: %s", t.Format("2006-01-02 15:04:05"))
	}

	// Schedule the cleanup job

	cleanupJob, err := utils.ScheduleJob(ctx, "1h", nil, c.cleanupWorker)
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to add cleanup job")
	}
	if t, err := cleanupJob.NextRun(); err == nil {
		c.logger.Trace().Msgf("Next cleanup job at: %s", t.Format("2006-01-02 15:04:05"))
	}

	return nil
}

func (c *Cache) cleanupWorker() {
	// Cleanup every hour
	torrents, err := c.client.GetTorrents()
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to get torrents")
		return
	}

	idStore := make(map[string]struct{})
	for _, t := range torrents {
		idStore[t.Id] = struct{}{}
	}

	deletedTorrents := make([]string, 0)
	c.torrents.Range(func(key string, _ *CachedTorrent) bool {
		if _, exists := idStore[key]; !exists {
			deletedTorrents = append(deletedTorrents, key)
		}
		return true
	})

	if len(deletedTorrents) > 0 {
		c.DeleteTorrents(deletedTorrents)
		c.logger.Info().Msgf("Deleted %d torrents", len(deletedTorrents))
	}
}
