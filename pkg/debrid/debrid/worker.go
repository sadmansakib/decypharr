package debrid

import (
	"github.com/sirrobot01/decypharr/internal/utils"
	"time"
)

func (c *Cache) StartSchedule() error {
	// For now, we just want to refresh the listing and download links

	if _, err := utils.ScheduleJob(c.ctx, c.downloadLinksRefreshInterval, nil, c.refreshDownloadLinks); err != nil {
		c.logger.Error().Err(err).Msg("Failed to add download link refresh job")
	} else {
		c.logger.Debug().Msgf("Download link refresh job scheduled for every %s", c.downloadLinksRefreshInterval)
	}

	if _, err := utils.ScheduleJob(c.ctx, c.torrentRefreshInterval, nil, c.refreshTorrents); err != nil {
		c.logger.Error().Err(err).Msg("Failed to add torrent refresh job")
	} else {
		c.logger.Debug().Msgf("Torrent refresh job scheduled for every %s", c.torrentRefreshInterval)
	}

	// Schedule the reset invalid links job
	// This job will run every at 00:00 CET
	// and reset the invalid links in the cache
	cet, _ := time.LoadLocation("CET")
	if _, err := utils.ScheduleJob(c.ctx, "00:00", cet, c.resetInvalidLinks); err != nil {
		c.logger.Error().Err(err).Msg("Failed to add reset invalid links job")
	}

	return nil
}
