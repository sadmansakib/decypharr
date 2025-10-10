package store

import (
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const (
	MaxLinkFailures       = 10
	MaxValidationRetries  = 3 // Retry same link 3 times before marking invalid
	MaxBackoffDuration    = 30 * time.Minute
	ValidationRetryWindow = 5 * time.Minute // Reset validation counter after 5 minutes
)

func (c *Cache) GetDownloadLink(torrentName, filename, fileLink string) (types.DownloadLink, error) {
	// Check
	counter, ok := c.failedLinksCounter.Load(fileLink)
	if ok && counter.Load() >= MaxLinkFailures {
		return types.DownloadLink{}, fmt.Errorf("file link %s has failed %d times, not retrying", fileLink, counter.Load())
	}

	// Use singleflight to deduplicate concurrent requests
	v, err, _ := c.downloadSG.Do(fileLink, func() (interface{}, error) {
		// Double-check cache inside singleflight (another goroutine might have filled it)
		if dl, err := c.checkDownloadLink(fileLink); err == nil && !dl.Empty() {
			return dl, nil
		}

		// Fetch the download link
		dl, err := c.fetchDownloadLink(torrentName, filename, fileLink)
		if err != nil {
			c.downloadSG.Forget(fileLink)
			return types.DownloadLink{}, err
		}

		if dl.Empty() {
			c.downloadSG.Forget(fileLink)
			err = fmt.Errorf("download link is empty for %s in torrent %s", filename, torrentName)
			return types.DownloadLink{}, err
		}

		return dl, nil
	})

	if err != nil {
		return types.DownloadLink{}, err
	}
	return v.(types.DownloadLink), nil
}

func (c *Cache) fetchDownloadLink(torrentName, filename, fileLink string) (types.DownloadLink, error) {
	emptyDownloadLink := types.DownloadLink{}
	ct := c.GetTorrentByName(torrentName)
	if ct == nil {
		return emptyDownloadLink, fmt.Errorf("torrent not found")
	}
	file, ok := ct.GetFile(filename)
	if !ok {
		return emptyDownloadLink, fmt.Errorf("file %s not found in torrent %s", filename, torrentName)
	}

	if file.Link == "" {
		// file link is empty, refresh the torrent to get restricted links
		ct = c.refreshTorrent(file.TorrentId) // Refresh the torrent from the debrid
		if ct == nil {
			return emptyDownloadLink, fmt.Errorf("failed to refresh torrent")
		} else {
			file, ok = ct.GetFile(filename)
			if !ok {
				return emptyDownloadLink, fmt.Errorf("file %s not found in refreshed torrent %s", filename, torrentName)
			}
		}
	}

	// If file.Link is still empty, return
	if file.Link == "" {
		// Try to reinsert the torrent?
		newCt, err := c.reInsertTorrent(ct)
		if err != nil {
			return emptyDownloadLink, fmt.Errorf("failed to reinsert torrent. %w", err)
		}
		ct = newCt
		file, ok = ct.GetFile(filename)
		if !ok {
			return emptyDownloadLink, fmt.Errorf("file %s not found in reinserted torrent %s", filename, torrentName)
		}
	}

	c.logger.Trace().Msgf("Getting download link for %s(%s)", filename, file.Link)
	downloadLink, err := c.client.GetDownloadLink(ct.Torrent, &file)
	if err != nil {
		if errors.Is(err, utils.TorrentNotFoundError) {
			// Torrent has been deleted from the debrid provider (e.g., Torbox DATABASE_ERROR)
			c.logger.Warn().
				Str("torrent_name", torrentName).
				Str("torrent_id", ct.Id).
				Str("torrent_hash", ct.InfoHash).
				Msg("Torrent deleted from debrid provider - marking for deletion")

			// Mark torrent for deletion - wire package will clean this up
			c.markTorrentDeleted(ct.InfoHash, ct.Name)

			return emptyDownloadLink, fmt.Errorf("torrent not found on debrid provider: %w", err)
		} else if errors.Is(err, utils.HosterUnavailableError) {
			c.logger.Trace().
				Str("token", utils.Mask(downloadLink.Token)).
				Str("filename", filename).
				Str("torrent_id", ct.Id).
				Msg("Hoster unavailable, attempting to reinsert torrent")

			newCt, err := c.reInsertTorrent(ct)
			if err != nil {
				return emptyDownloadLink, fmt.Errorf("failed to reinsert torrent: %w", err)
			}
			ct = newCt
			file, ok = ct.GetFile(filename)
			if !ok {
				return emptyDownloadLink, fmt.Errorf("file %s not found in reinserted torrent %s", filename, torrentName)
			}
			// Retry getting the download link
			downloadLink, err = c.client.GetDownloadLink(ct.Torrent, &file)
			if err != nil {
				return emptyDownloadLink, fmt.Errorf("retry failed to get download link: %w", err)
			}
			if downloadLink.Empty() {
				return emptyDownloadLink, fmt.Errorf("download link is empty after retry")
			}
			return emptyDownloadLink, fmt.Errorf("download link is empty after retry")
		} else if errors.Is(err, utils.TrafficExceededError) {
			// This is likely a fair usage limit error
			return emptyDownloadLink, err
		} else {
			return emptyDownloadLink, fmt.Errorf("failed to get download link: %w", err)
		}
	}
	if downloadLink.Empty() {
		return emptyDownloadLink, fmt.Errorf("download link is empty")
	}
	return downloadLink, nil
}

func (c *Cache) GetFileDownloadLinks(t CachedTorrent) {
	if err := c.client.GetFileDownloadLinks(t.Torrent); err != nil {
		c.logger.Error().Err(err).Str("torrent", t.Name).Msg("Failed to generate download links")
		return
	}
}

func (c *Cache) checkDownloadLink(link string) (types.DownloadLink, error) {
	dl, err := c.client.AccountManager().GetDownloadLink(link)
	if err != nil {
		return dl, err
	}
	if !c.downloadLinkIsInvalid(dl.DownloadLink) {
		return dl, nil
	}
	return types.DownloadLink{}, fmt.Errorf("download link not found for %s", link)
}

func (c *Cache) IncrementFailedLinkCounter(link string) int32 {
	counter, _ := c.failedLinksCounter.LoadOrCompute(link, func() (atomic.Int32, bool) {
		return atomic.Int32{}, true
	})
	return counter.Add(1)
}

func (c *Cache) MarkLinkAsInvalid(downloadLink types.DownloadLink, reason string) {
	// Increment file link error counter
	c.IncrementFailedLinkCounter(downloadLink.Link)

	c.invalidDownloadLinks.Store(downloadLink.DownloadLink, reason)
	// Remove the download api key from active
	if reason == "bandwidth_exceeded" {
		// Disable the account
		accountManager := c.client.AccountManager()
		account, err := accountManager.GetAccount(downloadLink.Token)
		if err != nil {
			c.logger.Error().Err(err).Str("token", utils.Mask(downloadLink.Token)).Msg("Failed to get account to disable")
			return
		}
		if account == nil {
			c.logger.Error().Str("token", utils.Mask(downloadLink.Token)).Msg("Account not found to disable")
			return
		}
		accountManager.Disable(account)
	}
}

func (c *Cache) downloadLinkIsInvalid(downloadLink string) bool {
	if _, ok := c.invalidDownloadLinks.Load(downloadLink); ok {
		return true
	}
	return false
}

func (c *Cache) GetDownloadByteRange(torrentName, filename string) (*[2]int64, error) {
	ct := c.GetTorrentByName(torrentName)
	if ct == nil {
		return nil, fmt.Errorf("torrent not found")
	}
	file := ct.Files[filename]
	return file.ByteRange, nil
}

func (c *Cache) GetTotalActiveDownloadLinks() int {
	total := 0
	allAccounts := c.client.AccountManager().Active()
	for _, acc := range allAccounts {
		total += acc.DownloadLinksCount()
	}
	return total
}

// shouldRetryInvalidLink checks if enough time has passed to retry fetching a new link
// Uses exponential backoff: 1min, 2min, 4min, 8min, 16min, 30min (max)
func (c *Cache) shouldRetryInvalidLink(downloadLink string) bool {
	info, ok := c.linkRetryTracker.Load(downloadLink)
	if !ok {
		return true // First attempt, allow it
	}

	// Calculate exponential backoff duration
	backoffDuration := time.Duration(math.Min(
		float64(time.Minute)*math.Pow(2, float64(info.retryCount)),
		float64(MaxBackoffDuration),
	))

	timeSinceLastAttempt := time.Since(info.lastAttempt)

	if timeSinceLastAttempt < backoffDuration {
		c.logger.Debug().
			Str("downloadLink", downloadLink).
			Int32("retryCount", info.retryCount).
			Dur("backoffDuration", backoffDuration).
			Dur("timeSinceLastAttempt", timeSinceLastAttempt).
			Dur("timeRemaining", backoffDuration-timeSinceLastAttempt).
			Msg("Invalid link retry backoff in effect")
		return false
	}

	return true
}

// recordInvalidLinkRetry records a retry attempt for an invalid link
func (c *Cache) recordInvalidLinkRetry(downloadLink string) {
	info, _ := c.linkRetryTracker.LoadOrCompute(downloadLink, func() (*linkRetryInfo, bool) {
		return &linkRetryInfo{
			retryCount:   0,
			lastAttempt:  time.Now(),
			downloadLink: downloadLink,
		}, true
	})

	info.retryCount++
	info.lastAttempt = time.Now()
	c.linkRetryTracker.Store(downloadLink, info)

	c.logger.Debug().
		Str("downloadLink", downloadLink).
		Int32("retryCount", info.retryCount).
		Msg("Recorded invalid link retry attempt")
}

// shouldValidateLink checks if we should retry the same link before marking it invalid
// Returns true if we should retry, false if we should mark as invalid
func (c *Cache) shouldValidateLink(downloadLink string) bool {
	retry, ok := c.linkValidationRetry.Load(downloadLink)
	if !ok {
		// First validation attempt
		c.linkValidationRetry.Store(downloadLink, &validationRetry{
			attemptCount: 1,
			firstAttempt: time.Now(),
		})
		return true // Allow first attempt
	}

	// Check if validation window has expired (reset counter)
	if time.Since(retry.firstAttempt) > ValidationRetryWindow {
		c.logger.Debug().
			Str("downloadLink", downloadLink).
			Msg("Validation retry window expired, resetting counter")
		c.linkValidationRetry.Store(downloadLink, &validationRetry{
			attemptCount: 1,
			firstAttempt: time.Now(),
		})
		return true
	}

	// Check if we've exceeded max validation retries
	if retry.attemptCount >= MaxValidationRetries {
		c.logger.Debug().
			Str("downloadLink", downloadLink).
			Int32("attemptCount", retry.attemptCount).
			Msg("Max validation retries reached, marking link as invalid")
		return false
	}

	// Increment attempt counter
	retry.attemptCount++
	c.linkValidationRetry.Store(downloadLink, retry)

	c.logger.Debug().
		Str("downloadLink", downloadLink).
		Int32("attemptCount", retry.attemptCount).
		Msg("Retrying link validation")

	return true
}

// clearValidationRetry clears the validation retry counter for a link (called on success)
func (c *Cache) clearValidationRetry(downloadLink string) {
	c.linkValidationRetry.Delete(downloadLink)
}

// markTorrentDeleted marks a torrent as deleted from the debrid provider
// This allows the wire package to clean up torrents that no longer exist on the debrid service
func (c *Cache) markTorrentDeleted(hash, name string) {
	c.deletedTorrents.Store(hash, name)
	c.logger.Info().
		Str("hash", hash).
		Str("name", name).
		Msg("Marked torrent as deleted from debrid provider")
}

// GetAndClearDeletedTorrents returns all torrents that have been marked as deleted
// and clears the internal tracking map. This should be called by the wire package
// to clean up torrents that no longer exist on the debrid provider.
func (c *Cache) GetAndClearDeletedTorrents() map[string]string {
	deleted := make(map[string]string)
	c.deletedTorrents.Range(func(hash string, name string) bool {
		deleted[hash] = name
		c.deletedTorrents.Delete(hash)
		return true
	})
	return deleted
}
