package store

import (
	"bufio"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/rclone"

	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"golang.org/x/sync/singleflight"

	"encoding/json"
	_ "time/tzdata"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
)

type WebDavFolderNaming string

const (
	WebDavUseFileName          WebDavFolderNaming = "filename"
	WebDavUseOriginalName      WebDavFolderNaming = "original"
	WebDavUseFileNameNoExt     WebDavFolderNaming = "filename_no_ext"
	WebDavUseOriginalNameNoExt WebDavFolderNaming = "original_no_ext"
	WebDavUseID                WebDavFolderNaming = "id"
	WebdavUseHash              WebDavFolderNaming = "infohash"
)

type CachedTorrent struct {
	*types.Torrent
	AddedOn    time.Time `json:"added_on"`
	IsComplete bool      `json:"is_complete"`
	Bad        bool      `json:"bad"`
}

func (c CachedTorrent) copy() CachedTorrent {
	return CachedTorrent{
		Torrent:    c.Torrent,
		AddedOn:    c.AddedOn,
		IsComplete: c.IsComplete,
		Bad:        c.Bad,
	}
}

type RepairType string

const (
	RepairTypeReinsert RepairType = "reinsert"
	RepairTypeDelete   RepairType = "delete"
)

// linkRetryInfo tracks retry attempts for invalid links with exponential backoff
type linkRetryInfo struct {
	retryCount   int32
	lastAttempt  time.Time
	downloadLink string
}

// validationRetry tracks validation attempts before marking a link as invalid
type validationRetry struct {
	attemptCount int32
	firstAttempt time.Time
}

type RepairRequest struct {
	Type      RepairType
	TorrentID string
	Priority  int
	FileName  string
}

type Cache struct {
	dir    string
	client common.Client
	logger zerolog.Logger

	torrents     *torrentCache
	folderNaming WebDavFolderNaming

	listingDebouncer *utils.Debouncer[bool]
	// monitors
	invalidDownloadLinks *xsync.Map[string, string]
	repairRequest        *xsync.Map[string, *reInsertRequest]
	failedToReinsert     *xsync.Map[string, struct{}]
	failedLinksCounter   *xsync.Map[string, atomic.Int32]     // link -> counter
	linkRetryTracker     *xsync.Map[string, *linkRetryInfo]   // downloadLink -> retry info
	linkValidationRetry  *xsync.Map[string, *validationRetry] // downloadLink -> validation attempts
	deletedTorrents      *xsync.Map[string, string]           // hash -> torrent name (tracks torrents deleted from debrid provider)

	// repair
	repairChan chan RepairRequest

	// readiness
	ready chan struct{}

	// config
	workers                      int
	torrentRefreshInterval       string
	downloadLinksRefreshInterval string

	// refresh mutex
	downloadLinksRefreshMu sync.RWMutex // for refreshing download links
	torrentsRefreshMu      sync.RWMutex // for refreshing torrents

	scheduler    gocron.Scheduler
	cetScheduler gocron.Scheduler

	saveSemaphore chan struct{}

	config        config.Debrid
	customFolders []string
	mounter       *rclone.Mount
	downloadSG    singleflight.Group
	streamClient  *http.Client
}

func NewDebridCache(dc config.Debrid, client common.Client, mounter *rclone.Mount) *Cache {
	cfg := config.Get()
	cet, err := time.LoadLocation("CET")
	if err != nil {
		cet, err = time.LoadLocation("Europe/Berlin") // Fallback to Berlin if CET fails
		if err != nil {
			cet = time.FixedZone("CET", 1*60*60) // Fallback to a fixed CET zone
		}
	}
	cetSc, err := gocron.NewScheduler(gocron.WithLocation(cet))
	if err != nil {
		// If we can't create a CET scheduler, fallback to local time
		cetSc, _ = gocron.NewScheduler(gocron.WithLocation(time.Local), gocron.WithGlobalJobOptions(
			gocron.WithTags("decypharr-"+dc.Name)))
	}
	scheduler, err := gocron.NewScheduler(
		gocron.WithLocation(time.Local),
		gocron.WithGlobalJobOptions(
			gocron.WithTags("decypharr-"+dc.Name)))
	if err != nil {
		// If we can't create a local scheduler, fallback to CET
		scheduler = cetSc
	}

	var customFolders []string
	dirFilters := map[string][]directoryFilter{}
	for name, value := range dc.Directories {
		for filterType, v := range value.Filters {
			df := directoryFilter{filterType: filterType, value: v}
			switch filterType {
			case filterByRegex, filterByNotRegex:
				df.regex = regexp.MustCompile(v)
			case filterBySizeGT, filterBySizeLT:
				df.sizeThreshold, _ = config.ParseSize(v)
			case filterBLastAdded:
				df.ageThreshold, _ = time.ParseDuration(v)
			}
			dirFilters[name] = append(dirFilters[name], df)
		}
		customFolders = append(customFolders, name)

	}
	_log := logger.New(fmt.Sprintf("%s-webdav", client.Name()))
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		DisableKeepAlives:     false,
		ForceAttemptHTTP2:     false,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   0,
	}

	c := &Cache{
		dir: filepath.Join(cfg.Path, "cache", dc.Name), // path to save cache files

		torrents:                     newTorrentCache(dirFilters),
		client:                       client,
		logger:                       _log,
		workers:                      dc.Workers,
		torrentRefreshInterval:       dc.TorrentsRefreshInterval,
		downloadLinksRefreshInterval: dc.DownloadLinksRefreshInterval,
		folderNaming:                 WebDavFolderNaming(dc.FolderNaming),
		saveSemaphore:                make(chan struct{}, 50),
		cetScheduler:                 cetSc,
		scheduler:                    scheduler,

		config:        dc,
		customFolders: customFolders,
		mounter:       mounter,

		ready:                make(chan struct{}),
		invalidDownloadLinks: xsync.NewMap[string, string](),
		repairRequest:        xsync.NewMap[string, *reInsertRequest](),
		failedToReinsert:     xsync.NewMap[string, struct{}](),
		failedLinksCounter:   xsync.NewMap[string, atomic.Int32](),
		linkRetryTracker:     xsync.NewMap[string, *linkRetryInfo](),
		linkValidationRetry:  xsync.NewMap[string, *validationRetry](),
		deletedTorrents:      xsync.NewMap[string, string](),
		streamClient:         httpClient,
		repairChan:           make(chan RepairRequest, 100), // Initialize the repair channel, max 100 requests buffered
	}

	c.listingDebouncer = utils.NewDebouncer[bool](100*time.Millisecond, func(refreshRclone bool) {
		c.RefreshListings(refreshRclone)
	})
	return c
}

func (c *Cache) IsReady() chan struct{} {
	return c.ready
}

func (c *Cache) StreamWithRclone() bool {
	return c.config.ServeFromRclone
}

// Reset clears all internal state so the Cache can be reused without leaks.
// Call this after stopping the old Cache (so no goroutines are holding references),
// and before you discard the instance on a restart.
func (c *Cache) Reset() {
	// Save all torrents before cleanup to ensure state is persisted.
	// This is a critical operation that must complete even during shutdown,
	// so we don't pass a cancellable context here. The save operations
	// are designed to be fast and should complete quickly.
	c.logger.Info().
		Str("provider", c.config.Name).
		Str("operation", "reset").
		Msg("Saving torrents before cleanup")
	c.SaveTorrents()

	// Unmount first
	if c.mounter != nil && c.mounter.IsMounted() {
		if err := c.mounter.Unmount(); err != nil {
			c.logger.Error().
				Err(err).
				Str("provider", c.config.Name).
				Str("operation", "unmount").
				Msg("Failed to unmount")
		} else {
			c.logger.Info().
				Str("provider", c.config.Name).
				Str("operation", "unmount").
				Msg("Unmounted successfully")
		}
	}

	if err := c.scheduler.StopJobs(); err != nil {
		c.logger.Error().
			Err(err).
			Str("operation", "stop_scheduler_jobs").
			Msg("Failed to stop scheduler jobs")
	}

	if err := c.scheduler.Shutdown(); err != nil {
		c.logger.Error().
			Err(err).
			Str("operation", "shutdown_scheduler").
			Msg("Failed to stop scheduler")
	}

	// Stop the listing debouncer
	c.listingDebouncer.Stop()

	// Close the repair channel
	if c.repairChan != nil {
		close(c.repairChan)
	}

	// 1. Reset torrent storage
	c.torrents.reset()

	// 3. Clear any sync.Maps
	c.invalidDownloadLinks = xsync.NewMap[string, string]()
	c.repairRequest = xsync.NewMap[string, *reInsertRequest]()
	c.failedToReinsert = xsync.NewMap[string, struct{}]()
	c.linkRetryTracker = xsync.NewMap[string, *linkRetryInfo]()
	c.linkValidationRetry = xsync.NewMap[string, *validationRetry]()

	// 5. Rebuild the listing debouncer
	c.listingDebouncer = utils.NewDebouncer[bool](
		100*time.Millisecond,
		func(refreshRclone bool) {
			c.RefreshListings(refreshRclone)
		},
	)

	// 6. Reset repair channel so the next Start() can spin it up
	c.repairChan = make(chan RepairRequest, 100)

	// Reset the ready channel
	c.ready = make(chan struct{})
}

func (c *Cache) Start(ctx context.Context) error {
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	c.logger.Info().
		Str("operation", "indexing").
		Str("provider", c.client.Name()).
		Msg("Started indexing")

	if err := c.Sync(ctx); err != nil {
		return fmt.Errorf("failed to sync cache: %w", err)
	}
	// Fire the ready channel
	close(c.ready)
	c.logger.Info().
		Str("operation", "indexing").
		Str("provider", c.client.Name()).
		Int("torrent_count", len(c.torrents.getAll())).
		Msg("Indexing complete")

	// initial download links
	go c.refreshDownloadLinks(ctx)
	go c.repairWorker(ctx)

	cfg := config.Get()
	name := c.client.Name()
	addr := cfg.BindAddress + ":" + cfg.Port + cfg.URLBase + "webdav/" + name + "/"
	c.logger.Info().
		Str("provider", name).
		Str("address", addr).
		Str("operation", "webdav_start").
		Msg("WebDAV server running")

	if c.mounter != nil {
		if err := c.mounter.Mount(ctx); err != nil {
			c.logger.Error().
				Err(err).
				Str("provider", c.config.Name).
				Str("operation", "mount").
				Msg("Failed to mount")
		}
	} else {
		c.logger.Warn().
			Str("provider", c.config.Name).
			Str("operation", "mount").
			Msg("Mounting is disabled")
	}
	return nil
}

func (c *Cache) load(ctx context.Context) (map[string]CachedTorrent, error) {
	mu := sync.Mutex{}

	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	files, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache directory: %w", err)
	}

	// Get only json files
	var jsonFiles []os.DirEntry
	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" {
			jsonFiles = append(jsonFiles, file)
		}
	}

	if len(jsonFiles) == 0 {
		return nil, nil
	}

	// Create channels with appropriate buffering
	workChan := make(chan os.DirEntry, min(c.workers, len(jsonFiles)))

	// Create a wait group for workers
	var wg sync.WaitGroup

	torrents := make(map[string]CachedTorrent, len(jsonFiles))

	// Start workers
	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					c.logger.Debug().Err(ctx.Err()).Msg("Load worker cancelled")
					return // Context cancelled, exit goroutine
				case file, ok := <-workChan:
					if !ok {
						return // Channel closed, exit goroutine
					}

					fileName := file.Name()
					filePath := filepath.Join(c.dir, fileName)
					data, err := os.ReadFile(filePath)
					if err != nil {
						c.logger.Error().
							Err(err).
							Str("file_path", filePath).
							Str("operation", "load_cache").
							Msg("Failed to read file")
						continue
					}

					var ct CachedTorrent
					if err := json.Unmarshal(data, &ct); err != nil {
						c.logger.Error().
							Err(err).
							Str("file_path", filePath).
							Str("operation", "load_cache").
							Msg("Failed to unmarshal file")
						continue
					}

					isComplete := true
					if len(ct.GetFiles()) != 0 {
						// Check if all files are valid, if not, delete the file.json and remove from cache.
						fs := make(map[string]types.File, len(ct.GetFiles()))
						for _, f := range ct.GetFiles() {
							if f.Link == "" {
								isComplete = false
								break
							}
							f.TorrentId = ct.Id
							fs[f.Name] = f
						}

						if isComplete {

							if addedOn, err := time.Parse(time.RFC3339, ct.Added); err == nil {
								ct.AddedOn = addedOn
							}
							ct.IsComplete = true
							ct.Files = fs
							ct.Name = path.Clean(ct.Name)
							mu.Lock()
							torrents[ct.Id] = ct
							mu.Unlock()
						}
					}
				}
			}
		}(ctx)
	}

	// Feed work to workers
	for _, file := range jsonFiles {
		select {
		case <-ctx.Done():
			break // Context cancelled
		default:
			workChan <- file
		}
	}

	// Signal workers that no more work is coming
	close(workChan)

	// Wait for all workers to complete
	wg.Wait()

	return torrents, nil
}

func (c *Cache) Sync(ctx context.Context) error {
	cachedTorrents, err := c.load(ctx)
	if err != nil {
		c.logger.Error().
			Err(err).
			Str("operation", "load_cache").
			Msg("Failed to load cache")
	}

	torrents, err := c.client.GetTorrents(ctx)
	if err != nil {
		return fmt.Errorf("failed to sync torrents: %v", err)
	}

	totalTorrents := len(torrents)

	c.logger.Info().
		Int("torrent_count", totalTorrents).
		Str("provider", c.client.Name()).
		Str("operation", "sync").
		Msg("Torrents found from provider")

	newTorrents := make([]*types.Torrent, 0, totalTorrents)
	idStore := make(map[string]struct{}, totalTorrents)
	for _, t := range torrents {
		idStore[t.Id] = struct{}{}
		if _, ok := cachedTorrents[t.Id]; !ok {
			newTorrents = append(newTorrents, t)
		}
	}

	// Check for deleted torrents
	deletedTorrents := make([]string, 0, len(cachedTorrents))
	for _, t := range cachedTorrents {
		if _, ok := idStore[t.Id]; !ok {
			deletedTorrents = append(deletedTorrents, t.Id)
		}
	}

	if len(deletedTorrents) > 0 {
		c.logger.Info().
			Int("deleted_count", len(deletedTorrents)).
			Str("operation", "sync").
			Msg("Found deleted torrents")
		for _, id := range deletedTorrents {
			// Remove from cache and debrid service
			delete(cachedTorrents, id)
			// Remove the json file from disk
			c.removeFile(id, false)

		}
	}

	// Write these torrents to the cache
	c.setTorrents(cachedTorrents, func() {
		c.listingDebouncer.Call(false)
	}) // Initial calls
	c.logger.Info().
		Int("cached_count", len(cachedTorrents)).
		Str("operation", "sync").
		Msg("Loaded torrents from cache")

	if len(newTorrents) > 0 {
		c.logger.Info().
			Int("new_count", len(newTorrents)).
			Str("operation", "sync").
			Msg("Found new torrents")
		if err := c.sync(ctx, newTorrents); err != nil {
			return fmt.Errorf("failed to sync torrents: %v", err)
		}
	}

	return nil
}

func (c *Cache) sync(ctx context.Context, torrents []*types.Torrent) error {

	// Create channels with appropriate buffering
	workChan := make(chan *types.Torrent, min(c.workers, len(torrents)))

	// Use an atomic counter for progress tracking
	var processed int64
	var errorCount int64

	// Create a wait group for workers
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case t, ok := <-workChan:
					if !ok {
						return // Channel closed, exit goroutine
					}

					if err := c.ProcessTorrent(t); err != nil {
						c.logger.Error().
							Err(err).
							Str("torrent_name", t.Name).
							Str("torrent_id", t.Id).
							Str("operation", "sync").
							Msg("Failed to process torrent")
						atomic.AddInt64(&errorCount, 1)
					}

					count := atomic.AddInt64(&processed, 1)
					if count%1000 == 0 {
						c.logger.Info().
							Int64("processed", count).
							Int("total", len(torrents)).
							Str("operation", "sync").
							Msg("Sync progress")
					}

				case <-ctx.Done():
					c.logger.Debug().Err(ctx.Err()).Msg("Sync worker cancelled")
					return // Context cancelled, exit goroutine
				}
			}
		}()
	}

	// Feed work to workers
	for _, t := range torrents {
		select {
		case workChan <- t:
			// Work sent successfully
		case <-ctx.Done():
			break // Context cancelled
		}
	}

	// Signal workers that no more work is coming
	close(workChan)

	// Wait for all workers to complete
	wg.Wait()

	c.listingDebouncer.Call(false) // final refresh
	c.logger.Info().
		Int("processed", len(torrents)).
		Int64("errors", errorCount).
		Str("operation", "sync").
		Msg("Sync complete")
	return nil
}

func (c *Cache) GetTorrentFolder(torrent *types.Torrent) string {
	switch c.folderNaming {
	case WebDavUseFileName:
		return path.Clean(torrent.Filename)
	case WebDavUseOriginalName:
		return path.Clean(torrent.OriginalFilename)
	case WebDavUseFileNameNoExt:
		return path.Clean(utils.RemoveExtension(torrent.Filename))
	case WebDavUseOriginalNameNoExt:
		return path.Clean(utils.RemoveExtension(torrent.OriginalFilename))
	case WebDavUseID:
		return torrent.Id
	case WebdavUseHash:
		return strings.ToLower(torrent.InfoHash)
	default:
		return path.Clean(torrent.Filename)
	}
}

func (c *Cache) setTorrent(t CachedTorrent, callback func(torrent CachedTorrent)) {
	torrentName := c.GetTorrentFolder(t.Torrent)
	updatedTorrent := t.copy()
	if o, ok := c.torrents.getByName(torrentName); ok && o.Id != t.Id {
		// If another torrent with the same name exists, merge the files, if the same file exists,
		// keep the one with the most recent added date

		// Save the most recent torrent
		mergedFiles := mergeFiles(o, updatedTorrent) // Useful for merging files across multiple torrents, while keeping the most recent
		updatedTorrent.Files = mergedFiles
	}
	c.torrents.set(torrentName, t)
	go c.SaveTorrent(t)
	if callback != nil {
		go callback(updatedTorrent)
	}
}

func (c *Cache) setTorrents(torrents map[string]CachedTorrent, callback func()) {
	for _, t := range torrents {
		torrentName := c.GetTorrentFolder(t.Torrent)
		updatedTorrent := t.copy()
		if o, ok := c.torrents.getByName(torrentName); ok && o.Id != t.Id {
			// Save the most recent torrent
			mergedFiles := mergeFiles(o, updatedTorrent)
			updatedTorrent.Files = mergedFiles
		}
		c.torrents.set(torrentName, t)
	}
	c.SaveTorrents()
	if callback != nil {
		callback()
	}
}

// GetListing returns a sorted list of torrents(READ-ONLY)
func (c *Cache) GetListing(folder string) []os.FileInfo {
	switch folder {
	case "__all__", "torrents":
		return c.torrents.getListing()
	default:
		return c.torrents.getFolderListing(folder)
	}
}

func (c *Cache) GetCustomFolders() []string {
	return c.customFolders
}

func (c *Cache) Close() error {
	return nil
}

func (c *Cache) GetTorrents() map[string]CachedTorrent {
	return c.torrents.getAll()
}

func (c *Cache) TotalTorrents() int {
	return c.torrents.getAllCount()
}

func (c *Cache) GetTorrentByName(name string) *CachedTorrent {
	if torrent, ok := c.torrents.getByName(name); ok {
		return &torrent
	}
	return nil
}

func (c *Cache) GetTorrentsName() map[string]CachedTorrent {
	return c.torrents.getAllByName()
}

func (c *Cache) GetTorrent(torrentId string) *CachedTorrent {
	if torrent, ok := c.torrents.getByID(torrentId); ok {
		return &torrent
	}
	return nil
}

func (c *Cache) SaveTorrents() {
	torrents := c.torrents.getAll()
	for _, torrent := range torrents {
		c.SaveTorrent(torrent)
	}
}

func (c *Cache) SaveTorrent(ct CachedTorrent) {
	marshaled, err := json.MarshalIndent(ct, "", "  ")
	if err != nil {
		c.logger.Error().
			Err(err).
			Str("torrent_id", ct.Id).
			Str("operation", "save_torrent").
			Msg("Failed to marshal torrent")
		return
	}

	// Store just the essential info needed for the file operation
	saveInfo := struct {
		id       string
		jsonData []byte
	}{
		id:       ct.Torrent.Id,
		jsonData: marshaled,
	}

	// Properly use semaphore - block if full to enforce concurrency limit
	c.saveSemaphore <- struct{}{}
	go func() {
		defer func() { <-c.saveSemaphore }()
		if err := c.saveTorrent(saveInfo.id, saveInfo.jsonData); err != nil {
			c.logger.Error().
				Err(err).
				Str("torrent_id", saveInfo.id).
				Str("operation", "save_torrent").
				Msg("Failed to save torrent")
		}
	}()
}

func (c *Cache) saveTorrent(id string, data []byte) error {
	fileName := id + ".json"
	filePath := filepath.Join(c.dir, fileName)

	// Use a unique temporary filename for concurrent safety
	tmpFile := filePath + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)

	f, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// Ensure cleanup on error
	success := false
	defer func() {
		_ = f.Close()
		if !success {
			_ = os.Remove(tmpFile)
		}
	}()

	w := bufio.NewWriter(f)
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("failed to write data: %w", err)
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("failed to flush data: %w", err)
	}

	// Close the file before renaming
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpFile, filePath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	success = true
	return nil
}

func (c *Cache) ProcessTorrent(t *types.Torrent) error {

	isComplete := func(files map[string]types.File) bool {
		_complete := len(files) > 0
		for _, file := range files {
			if file.Link == "" {
				_complete = false
				break
			}
		}
		return _complete
	}

	if !isComplete(t.Files) {
		if err := c.client.UpdateTorrent(context.Background(), t); err != nil {
			return fmt.Errorf("failed to update torrent: %w", err)
		}
	}

	if !isComplete(t.Files) {
		c.logger.Debug().
			Str("torrent_id", t.Id).
			Str("torrent_name", t.Name).
			Int("total_files", len(t.Files)).
			Msg("Torrent still not complete after refresh, marking as bad")
	} else {

		addedOn, err := time.Parse(time.RFC3339, t.Added)
		if err != nil {
			addedOn = time.Now()
		}
		ct := CachedTorrent{
			Torrent:    t,
			IsComplete: len(t.Files) > 0,
			AddedOn:    addedOn,
		}
		c.setTorrent(ct, func(tor CachedTorrent) {
			c.listingDebouncer.Call(false)
		})
	}
	return nil
}

func (c *Cache) Add(t *types.Torrent) error {
	if len(t.Files) == 0 {
		c.logger.Warn().
			Str("torrent_id", t.Id).
			Str("torrent_name", t.Name).
			Str("operation", "add_torrent").
			Msg("Torrent has no files, refreshing")
		if err := c.client.UpdateTorrent(context.Background(), t); err != nil {
			return fmt.Errorf("failed to update torrent: %w", err)
		}
	}
	addedOn, err := time.Parse(time.RFC3339, t.Added)
	if err != nil {
		addedOn = time.Now()
	}
	ct := CachedTorrent{
		Torrent:    t,
		IsComplete: len(t.Files) > 0,
		AddedOn:    addedOn,
	}
	c.setTorrent(ct, func(tor CachedTorrent) {
		c.RefreshListings(true)
	})
	go c.GetFileDownloadLinks(ct)
	return nil

}

func (c *Cache) Client() common.Client {
	return c.client
}

func (c *Cache) DeleteTorrent(id string) error {
	c.torrentsRefreshMu.Lock()
	defer c.torrentsRefreshMu.Unlock()

	if c.deleteTorrent(id, true) {
		go c.RefreshListings(true)
		return nil
	}
	return nil
}

func (c *Cache) validateAndDeleteTorrents(ctx context.Context, torrents []string) {
	if len(torrents) == 0 {
		return
	}

	c.logger.Info().
		Int("count", len(torrents)).
		Strs("torrent_ids", torrents).
		Str("operation", "validate_deletion").
		Msg("Detected torrents missing from API response")

	wg := sync.WaitGroup{}
	var deletedCount atomic.Int32

	for _, torrent := range torrents {
		wg.Add(1)
		go func(ctx context.Context, t string) {
			defer wg.Done()

			select {
			case <-ctx.Done():
				c.logger.Debug().
					Err(ctx.Err()).
					Str("torrent_id", t).
					Str("operation", "validate_deletion").
					Msg("Validation cancelled")
				return
			default:
			}

			// Get torrent details before validation for better logging
			cachedTorrent, exists := c.torrents.getByID(t)
			torrentName := "unknown"
			if exists {
				torrentName = cachedTorrent.Name
			}

			// Check if torrent is truly deleted
			_, err := c.client.GetTorrent(ctx, t)
			if err != nil {
				c.logger.Warn().
					Str("torrent_id", t).
					Str("torrent_name", torrentName).
					Str("operation", "validate_deletion").
					Err(err).
					Msg("Torrent confirmed deleted from provider")

				c.deleteTorrent(t, false) // Since it's removed from debrid already
				deletedCount.Add(1)
			} else {
				c.logger.Debug().
					Str("torrent_id", t).
					Str("torrent_name", torrentName).
					Str("operation", "validate_deletion").
					Msg("Torrent still exists on provider")
			}
		}(ctx, torrent)
	}
	wg.Wait()

	c.logger.Info().
		Int("validated", len(torrents)).
		Int("deleted", int(deletedCount.Load())).
		Str("operation", "validate_deletion").
		Msg("Completed torrent deletion validation")

	c.listingDebouncer.Call(true)
}

// deleteTorrent deletes the torrent from the cache and debrid service
// It also handles torrents with the same name but different IDs
func (c *Cache) deleteTorrent(id string, removeFromDebrid bool) bool {

	if torrent, ok := c.torrents.getByID(id); ok {
		c.torrents.removeId(id) // Delete id from cache
		defer func() {
			c.removeFile(id, false)
			if removeFromDebrid {
				_ = c.client.DeleteTorrent(context.Background(), id) // Skip error handling, we don't care if it fails
			}
		}() // defer delete from debrid

		torrentName := c.GetTorrentFolder(torrent.Torrent)

		if t, ok := c.torrents.getByName(torrentName); ok {

			newFiles := map[string]types.File{}
			newId := ""
			for _, file := range t.GetFiles() {
				if file.TorrentId != "" && file.TorrentId != id {
					if newId == "" && file.TorrentId != "" {
						newId = file.TorrentId
					}
					newFiles[file.Name] = file
				}
			}
			if len(newFiles) == 0 {
				// Delete the torrent since no files are left
				c.torrents.remove(torrentName)
			} else {
				t.Files = newFiles
				newId = cmp.Or(newId, t.Id)
				t.Id = newId
				c.setTorrent(t, nil) // This gets called after calling deleteTorrent
			}
		}
		return true
	}
	return false
}

func (c *Cache) DeleteTorrents(ids []string) {
	c.logger.Info().
		Int("count", len(ids)).
		Str("operation", "delete_torrents").
		Msg("Deleting torrents")
	for _, id := range ids {
		_ = c.deleteTorrent(id, true)
	}
	c.listingDebouncer.Call(true)
}

func (c *Cache) removeFile(torrentId string, moveToTrash bool) {
	// Moves the torrent file to the trash
	filePath := filepath.Join(c.dir, torrentId+".json")

	// Check if the file exists
	if _, err := os.Stat(filePath); errors.Is(err, os.ErrNotExist) {
		return
	}

	if !moveToTrash {
		// If not moving to trash, delete the file directly
		if err := os.Remove(filePath); err != nil {
			c.logger.Error().
				Err(err).
				Str("file_path", filePath).
				Str("operation", "remove_file").
				Msg("Failed to remove file")
			return
		}
		return
	}
	// Move the file to the trash
	trashPath := filepath.Join(c.dir, "trash", torrentId+".json")
	if err := os.MkdirAll(filepath.Dir(trashPath), 0755); err != nil {
		return
	}
	if err := os.Rename(filePath, trashPath); err != nil {
		return
	}
}

func (c *Cache) OnRemove(torrentId string) {
	c.logger.Debug().
		Str("torrent_id", torrentId).
		Str("operation", "on_remove").
		Msg("OnRemove triggered")
	err := c.DeleteTorrent(torrentId)
	if err != nil {
		c.logger.Error().
			Err(err).
			Str("torrent_id", torrentId).
			Str("operation", "on_remove").
			Msg("Failed to delete torrent")
		return
	}
}

// RemoveFile removes a file from the torrent cache
// TODO sends a re-insert that removes the file from debrid
func (c *Cache) RemoveFile(torrentId string, filename string) error {
	c.logger.Debug().
		Str("torrent_id", torrentId).
		Str("filename", filename).
		Str("operation", "remove_file").
		Msg("Removing file from torrent")
	torrent, ok := c.torrents.getByID(torrentId)
	if !ok {
		return fmt.Errorf("torrent %s not found", torrentId)
	}
	file, ok := torrent.GetFile(filename)
	if !ok {
		return fmt.Errorf("file %s not found in torrent %s", filename, torrentId)
	}
	file.Deleted = true
	torrent.Files[filename] = file

	// If the torrent has no files left, delete it
	if len(torrent.GetFiles()) == 0 {
		c.logger.Debug().
			Str("torrent_id", torrentId).
			Str("operation", "remove_file").
			Msg("Torrent has no files left, deleting")
		if err := c.DeleteTorrent(torrentId); err != nil {
			return fmt.Errorf("failed to delete torrent %s: %w", torrentId, err)
		}
		return nil
	}

	c.setTorrent(torrent, func(torrent CachedTorrent) {
		c.listingDebouncer.Call(true)
	}) // Update the torrent in the cache
	return nil
}

func (c *Cache) Logger() zerolog.Logger {
	return c.logger
}

func (c *Cache) GetConfig() config.Debrid {
	return c.config
}
