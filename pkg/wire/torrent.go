package wire

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"time"

	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func (s *Store) AddTorrent(ctx context.Context, importReq *ImportRequest) error {
	torrent := createTorrentFromMagnet(importReq)
	debridTorrent, err := debridTypes.Process(ctx, s.debrid, importReq.SelectedDebrid, importReq.Magnet, importReq.Arr, importReq.Action, importReq.DownloadUncached)

	if err != nil {
		var httpErr *utils.HTTPError
		if ok := errors.As(err, &httpErr); ok {
			switch httpErr.Code {
			case "too_many_active_downloads":
				// Handle too much active downloads error
				s.logger.Warn().Msgf("Too many active downloads for %s, adding to queue", importReq.Magnet.Name)

				if err := s.addToQueue(importReq); err != nil {
					s.logger.Error().Err(err).Msgf("Failed to add %s to queue", importReq.Magnet.Name)
					return err
				}
				torrent.State = "queued"
			default:
				// Unhandled error, return it, caller logs it
				return err
			}
		} else {
			// Unhandled error, return it, caller logs it
			return err
		}
	}
	torrent = s.partialTorrentUpdate(torrent, debridTorrent)
	s.torrents.AddOrUpdate(ctx, torrent)
	go s.processFiles(ctx, torrent, debridTorrent, importReq) // We can send async for file processing not to delay the response
	return nil
}

func (s *Store) processFiles(ctx context.Context, torrent *Torrent, debridTorrent *types.Torrent, importReq *ImportRequest) {

	if debridTorrent == nil {
		// Early return if debridTorrent is nil
		return
	}

	deb := s.debrid.Debrid(debridTorrent.Debrid)
	client := deb.Client()
	downloadingStatuses := client.GetDownloadingStatus()
	_arr := importReq.Arr
	backoff := time.NewTimer(s.refreshInterval)
	defer backoff.Stop()
	for debridTorrent.Status != "downloaded" {
		select {
		case <-ctx.Done():
			s.logger.Warn().
				Str("torrent_id", debridTorrent.Id).
				Err(ctx.Err()).
				Msg("Context cancelled during file processing")
			s.markTorrentAsFailed(ctx, torrent)
			return
		case <-backoff.C:
			s.logger.Debug().Msgf("%s <- (%s) Download Progress: %.2f%%", debridTorrent.Debrid, debridTorrent.Name, debridTorrent.Progress)
			dbT, err := client.CheckStatus(ctx, debridTorrent)
			if err != nil {
				s.logger.Error().
					Str("torrent_id", debridTorrent.Id).
					Str("torrent_name", debridTorrent.Name).
					Err(err).
					Msg("Error checking torrent status")
				if dbT != nil && dbT.Id != "" {
					// Delete the torrent if it was not downloaded
					go func(ctx context.Context, id string) {
						// Use context with timeout for cleanup operations
						cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
						defer cancel()

						if err := client.DeleteTorrent(cleanupCtx, id); err != nil {
							s.logger.Warn().
								Err(err).
								Str("torrent_id", id).
								Msg("Failed to delete failed torrent")
						}
					}(ctx, dbT.Id)
				}
				s.logger.Error().Msgf("Error checking status: %v", err)
				s.markTorrentAsFailed(ctx, torrent)
				go func() {
					_arr.Refresh()
				}()
				importReq.markAsFailed(err, torrent, debridTorrent)
				return
			}

			debridTorrent = dbT
			torrent = s.partialTorrentUpdate(torrent, debridTorrent)

			// Exit the loop for downloading statuses to prevent memory buildup
			if debridTorrent.Status == "downloaded" || !utils.Contains(downloadingStatuses, debridTorrent.Status) {
				goto processComplete
			}

			// Reset the backoff timer
			nextInterval := min(s.refreshInterval*2, 30*time.Second)
			backoff.Reset(nextInterval)
		}
	}
processComplete:
	var torrentSymlinkPath, torrentRclonePath string
	debridTorrent.Arr = _arr

	// Check if debrid supports webdav by checking cache
	timer := time.Now()

	onFailed := func(err error) {
		s.markTorrentAsFailed(ctx, torrent)
		go func(ctx context.Context) {
			// Use context with timeout for cleanup
			cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			if deleteErr := client.DeleteTorrent(cleanupCtx, debridTorrent.Id); deleteErr != nil {
				s.logger.Warn().Err(deleteErr).Msgf("Failed to delete torrent %s", debridTorrent.Id)
			}
		}(ctx)
		s.logger.Error().Err(err).Msgf("Error occured while processing torrent %s", debridTorrent.Name)
		importReq.markAsFailed(err, torrent, debridTorrent)
	}

	onSuccess := func(torrentSymlinkPath string) {
		torrent.TorrentPath = torrentSymlinkPath
		s.updateTorrent(ctx, torrent, debridTorrent)
		s.logger.Info().Msgf("Adding %s took %s", debridTorrent.Name, time.Since(timer))

		go importReq.markAsCompleted(torrent, debridTorrent) // Mark the import request as completed, send callback if needed
		go func(ctx context.Context) {
			select {
			case <-ctx.Done():
				return
			default:
				if err := request.SendDiscordMessage("download_complete", "success", torrent.discordContext()); err != nil {
					s.logger.Error().Msgf("Error sending discord message: %v", err)
				}
			}
		}(ctx)
		go func(ctx context.Context) {
			select {
			case <-ctx.Done():
				return
			default:
				_arr.Refresh()
			}
		}(ctx)
	}

	// Check for multi-season torrent support
	isMultiSeason, seasons, err := s.detectMultiSeason(debridTorrent)
	if err != nil {
		s.logger.Warn().Msgf("Error detecting multi-season for %s: %v", debridTorrent.Name, err)
		// Continue with normal processing if detection fails
		isMultiSeason = false
	}

	switch importReq.Action {
	case "symlink":
		// Symlink action, we will create a symlink to the torrent
		s.logger.Debug().Msgf("Post-Download Action: Symlink")
		cache := deb.Cache()

		if cache != nil {
			s.logger.Info().Msgf("Using internal webdav for %s", debridTorrent.Debrid)
			// Use webdav to download the file
			if err := cache.Add(debridTorrent); err != nil {
				onFailed(err)
				return
			}
		}

		if isMultiSeason {
			s.logger.Info().Msgf("Processing multi-season torrent with %d seasons", len(seasons))

			// Remove any torrent already added
			err := s.processMultiSeasonSymlinks(ctx, torrent, debridTorrent, seasons, importReq)
			if err == nil {
				// If an error occurred during multi-season processing, send it to normal processing
				s.logger.Info().Msgf("Adding %s took %s", debridTorrent.Name, time.Since(timer))

				go importReq.markAsCompleted(torrent, debridTorrent) // Mark the import request as completed, send callback if needed
				go func() {
					if err := request.SendDiscordMessage("download_complete", "success", torrent.discordContext()); err != nil {
						s.logger.Error().Msgf("Error sending discord message: %v", err)
					}
				}()
				go func() {
					_arr.Refresh()
				}()
				return
			}
		}

		if cache != nil {
			torrentRclonePath = filepath.Join(debridTorrent.MountPath, cache.GetTorrentFolder(debridTorrent)) // /mnt/remote/realdebrid/MyTVShow
			torrentSymlinkPath = filepath.Join(torrent.SavePath, utils.RemoveExtension(debridTorrent.Name))   // /mnt/symlinks/{category}/MyTVShow/

		} else {
			// User is using either zurg or debrid webdav
			torrentRclonePath, torrentSymlinkPath, err = s.getTorrentPaths(torrent.SavePath, debridTorrent)
			if err != nil {
				onFailed(err)
				return
			}
		}

		torrentSymlinkPath, err = s.processSymlink(debridTorrent, torrentRclonePath, torrentSymlinkPath)

		if err != nil {
			onFailed(err)
			return
		}
		if torrentSymlinkPath == "" {
			err = fmt.Errorf("symlink path is empty for %s", debridTorrent.Name)
			onFailed(err)
		}
		onSuccess(torrentSymlinkPath)
		return
	case "download":
		// Download action, we will download the torrent to the specified folder
		// Generate download links
		s.logger.Debug().Msgf("Post-Download Action: Download")

		if isMultiSeason {
			s.logger.Info().Msgf("Processing multi-season download with %d seasons", len(seasons))
			err := s.processMultiSeasonDownloads(ctx, torrent, debridTorrent, seasons, importReq)
			if err != nil {
				onFailed(err)
				return
			}
			// Multi-season processing completed successfully
			onSuccess(torrent.SavePath)
			return
		}

		if err := client.GetFileDownloadLinks(ctx, debridTorrent); err != nil {
			onFailed(err)
			return
		}
		torrentSymlinkPath, err = s.processDownload(torrent, debridTorrent)
		if err != nil {
			onFailed(err)
			return
		}
		if torrentSymlinkPath == "" {
			err = fmt.Errorf("download path is empty for %s", debridTorrent.Name)
			onFailed(err)
			return
		}
		onSuccess(torrentSymlinkPath)
	case "none":
		s.logger.Debug().Msgf("Post-Download Action: None")
		// No action, just update the torrent and mark it as completed
		onSuccess(torrent.TorrentPath)
	default:
		// Action is none, do nothing, fallthrough
	}
}

func (s *Store) markTorrentAsFailed(ctx context.Context, t *Torrent) *Torrent {
	t.State = "error"
	s.torrents.AddOrUpdate(ctx, t)
	go func() {
		if err := request.SendDiscordMessage("download_failed", "error", t.discordContext()); err != nil {
			s.logger.Error().Msgf("Error sending discord message: %v", err)
		}
	}()
	return t
}

func (s *Store) partialTorrentUpdate(t *Torrent, debridTorrent *types.Torrent) *Torrent {
	if debridTorrent == nil {
		return t
	}

	addedOn, err := time.Parse(time.RFC3339, debridTorrent.Added)
	if err != nil {
		addedOn = time.Now()
	}
	totalSize := debridTorrent.Bytes
	progress := (cmp.Or(debridTorrent.Progress, 0.0)) / 100.0
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		progress = 0
	}
	sizeCompleted := int64(float64(totalSize) * progress)

	var speed int64
	if debridTorrent.Speed != 0 {
		speed = debridTorrent.Speed
	}
	var eta int
	if speed != 0 {
		eta = int((totalSize - sizeCompleted) / speed)
	}
	files := make([]*File, 0, len(debridTorrent.Files))
	for index, file := range debridTorrent.GetFiles() {
		files = append(files, &File{
			Index: index,
			Name:  file.Path,
			Size:  file.Size,
		})
	}
	t.DebridID = debridTorrent.Id
	t.Name = debridTorrent.Name
	t.AddedOn = addedOn.Unix()
	t.Files = files
	t.Debrid = debridTorrent.Debrid
	t.Size = totalSize
	t.TotalSize = totalSize
	t.Completed = sizeCompleted
	t.NumSeeds = debridTorrent.Seeders
	t.Downloaded = sizeCompleted
	t.DownloadedSession = sizeCompleted
	t.Uploaded = sizeCompleted
	t.UploadedSession = sizeCompleted
	t.AmountLeft = totalSize - sizeCompleted
	t.Progress = progress
	t.Eta = eta
	t.Dlspeed = speed
	t.Upspeed = speed
	return t
}

func (s *Store) updateTorrent(ctx context.Context, t *Torrent, debridTorrent *types.Torrent) *Torrent {
	if debridTorrent == nil {
		return t
	}

	if debridClient := s.debrid.Clients()[debridTorrent.Debrid]; debridClient != nil {
		if debridTorrent.Status != "downloaded" {
			_ = debridClient.UpdateTorrent(ctx, debridTorrent)
		}
	}
	t = s.partialTorrentUpdate(t, debridTorrent)
	t.ContentPath = t.TorrentPath

	if t.IsReady() {
		t.State = "pausedUP"
		s.torrents.Update(ctx, t)
		return t
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Debug().Msg("Update torrent cancelled")
			return t
		case <-ticker.C:
			if t.IsReady() {
				t.State = "pausedUP"
				s.torrents.Update(ctx, t)
				return t
			}
			updatedT := s.updateTorrent(ctx, t, debridTorrent)
			t = updatedT

		case <-time.After(10 * time.Minute): // Add a timeout
			return t
		}
	}
}
