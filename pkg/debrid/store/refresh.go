package store

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

type fileInfo struct {
	id      string
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *fileInfo) ModTime() time.Time { return fi.modTime }
func (fi *fileInfo) IsDir() bool        { return fi.isDir }
func (fi *fileInfo) ID() string         { return fi.id }
func (fi *fileInfo) Sys() interface{}   { return nil }

func (c *Cache) RefreshListings(refreshRclone bool) {
	// Copy the torrents to a string|time map
	c.torrents.refreshListing() // refresh torrent listings

	if refreshRclone {
		if err := c.refreshRclone(); err != nil {
			c.logger.Error().Err(err).Msg("Failed to refresh rclone") // silent error
		}
	}
}

func (c *Cache) refreshTorrents(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	if !c.torrentsRefreshMu.TryLock() {
		return
	}
	defer c.torrentsRefreshMu.Unlock()

	// Get all torrents from the debrid service
	debTorrents, err := c.client.GetTorrents(ctx)
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to get torrents")
		return
	}

	if len(debTorrents) == 0 {
		// Maybe an error occurred
		return
	}

	currentTorrentIds := make(map[string]struct{}, len(debTorrents))
	for _, t := range debTorrents {
		currentTorrentIds[t.Id] = struct{}{}
	}

	// Let's implement deleting torrents removed from debrid
	cachedTorrents := c.torrents.getIdMaps()
	deletedTorrents := make([]string, 0, len(cachedTorrents))
	for id := range cachedTorrents {
		if _, exists := currentTorrentIds[id]; !exists {
			deletedTorrents = append(deletedTorrents, id)
		}
	}

	if len(deletedTorrents) > 0 {
		c.logger.Debug().
			Int("count", len(deletedTorrents)).
			Msg("Torrents detected as missing from API response - scheduling validation")
		go c.validateAndDeleteTorrents(ctx, deletedTorrents)
	}

	newTorrents := make([]*types.Torrent, 0, len(debTorrents))
	for _, t := range debTorrents {
		if _, exists := cachedTorrents[t.Id]; !exists {
			newTorrents = append(newTorrents, t)
		}
	}

	if len(newTorrents) == 0 {
		return
	}

	c.logger.Trace().Msgf("Found %d new torrents", len(newTorrents))

	workChan := make(chan *types.Torrent, min(100, len(newTorrents)))
	errChan := make(chan error, len(newTorrents))
	var wg sync.WaitGroup
	counter := 0

	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case t, ok := <-workChan:
					if !ok {
						return
					}
					if err := c.ProcessTorrent(t); err != nil {
						c.logger.Error().Err(err).Msgf("Failed to process new torrent %s", t.Id)
						errChan <- err
					}
					counter++
				}
			}
		}(ctx)
	}

	for _, t := range newTorrents {
		workChan <- t
	}
	close(workChan)
	wg.Wait()

	c.listingDebouncer.Call(true)

	c.logger.Debug().Msgf("Processed %d new torrents", counter)
}

func (c *Cache) refreshRclone() error {
	cfg := c.config
	dirs := strings.FieldsFunc(cfg.RcRefreshDirs, func(r rune) bool {
		return r == ',' || r == '&'
	})
	if len(dirs) == 0 {
		dirs = []string{"__all__"}
	}
	if c.mounter != nil {
		return c.mounter.RefreshDir(dirs)
	} else {
		return c.refreshRcloneWithRC(dirs)
	}
}

func (c *Cache) refreshRcloneWithRC(dirs []string) error {
	cfg := c.config

	if cfg.RcUrl == "" {
		return nil
	}

	client := http.DefaultClient
	// Create form data
	data := c.buildRcloneRequestData(dirs)

	if err := c.sendRcloneRequest(client, "vfs/forget", data); err != nil {
		c.logger.Error().Err(err).Msg("Failed to send rclone vfs/forget request")
	}

	if err := c.sendRcloneRequest(client, "vfs/refresh", data); err != nil {
		c.logger.Error().Err(err).Msg("Failed to send rclone vfs/refresh request")
	}

	return nil
}

func (c *Cache) buildRcloneRequestData(dirs []string) string {
	// Pre-allocate capacity: estimate ~20 chars per dir entry
	var data strings.Builder
	data.Grow(len(dirs) * 20)

	for index, dir := range dirs {
		if dir != "" {
			if index == 0 {
				data.WriteString("dir=")
				data.WriteString(dir)
			} else {
				data.WriteString("&dir")
				data.WriteString(strconv.Itoa(index + 1))
				data.WriteString("=")
				data.WriteString(dir)
			}
		}
	}
	return data.String()
}

func (c *Cache) sendRcloneRequest(client *http.Client, endpoint, data string) error {
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/%s", c.config.RcUrl, endpoint), strings.NewReader(data))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if c.config.RcUser != "" && c.config.RcPass != "" {
		req.SetBasicAuth(c.config.RcUser, c.config.RcPass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("failed to perform %s: %s - %s", endpoint, resp.Status, string(body))
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Cache) refreshTorrent(torrentId string) *CachedTorrent {

	if torrentId == "" {
		c.logger.Error().Msg("Torrent ID is empty")
		return nil
	}

	torrent, err := c.client.GetTorrent(context.Background(), torrentId)
	if err != nil {
		c.logger.Error().Err(err).Msgf("Failed to get torrent %s", torrentId)
		return nil
	}
	addedOn, err := time.Parse(time.RFC3339, torrent.Added)
	if err != nil {
		addedOn = time.Now()
	}
	ct := CachedTorrent{
		Torrent:    torrent,
		AddedOn:    addedOn,
		IsComplete: len(torrent.Files) > 0,
	}
	c.setTorrent(ct, func(torrent CachedTorrent) {
		go c.listingDebouncer.Call(true)
	})

	return &ct
}

func (c *Cache) refreshDownloadLinks(ctx context.Context) {

	select {
	case <-ctx.Done():
		return
	default:
	}

	if !c.downloadLinksRefreshMu.TryLock() {
		return
	}
	defer c.downloadLinksRefreshMu.Unlock()

	if err := c.client.RefreshDownloadLinks(ctx); err != nil {
		c.logger.Error().Err(err).Msg("Failed to get download links")
		return
	}

	c.logger.Debug().Msgf("Refreshed download links")
}
