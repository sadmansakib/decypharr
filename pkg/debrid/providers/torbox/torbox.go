package torbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	gourl "net/url"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/version"
	"go.uber.org/ratelimit"
)

const (
	// userMeCacheDuration is the duration for which user data is cached (7 days)
	userMeCacheDuration = 7 * 24 * time.Hour

	// torrentsListCacheDuration is the duration for which torrents list is cached (45 seconds)
	// This value must be ≤ worker refresh interval to prevent stale data
	// The worker refresh interval is configured in pkg/wire/worker.go and defaults to 45 seconds
	// Aligns with the default torrent refresh interval to minimize duplicate API calls
	torrentsListCacheDuration = 45 * time.Second

	// maxPaginationIterations prevents infinite loops when fetching torrents
	// At 100 items per page, this allows up to 10,000 torrents
	maxPaginationIterations = 100
)

// Torbox slot configuration based on plan type
// These values determine the maximum number of concurrent active torrents
var torboxSlotConfig = map[int]int{
	1: 3,  // Plan 1 (Essential): 3 slots
	2: 10, // Plan 2 (Pro): 10 slots
	3: 5,  // Plan 3 (Standard): 5 slots
}

// getTotalSlots calculates the total number of slots available for a user
// based on their plan and additional concurrent slots
func getTotalSlots(plan int, additionalSlots int) int {
	// Validate plan ID
	if plan < 0 {
		// Negative plan IDs are invalid, default to plan 1
		plan = 1
	}

	baseSlots, exists := torboxSlotConfig[plan]
	if !exists {
		// Default to plan 1 if unknown plan type
		baseSlots = torboxSlotConfig[1]
	}

	// Bounds checking for additionalSlots
	if additionalSlots < 0 {
		// Negative slots don't make sense, treat as 0
		additionalSlots = 0
	}
	if additionalSlots > 1000 {
		// Reasonable upper bound to prevent overflow or unrealistic values
		additionalSlots = 1000
	}

	totalSlots := baseSlots + additionalSlots

	// Edge case: Prevent integer overflow (extremely unlikely but defensive)
	if totalSlots < baseSlots {
		// Overflow detected, cap at reasonable maximum
		return 1000
	}

	return totalSlots
}

type Torbox struct {
	name                  string
	Host                  string `json:"host"`
	APIKey                string
	accountsManager       *account.Manager
	autoExpiresLinksAfter time.Duration

	DownloadUncached bool
	client           *request.Client

	MountPath   string
	logger      zerolog.Logger
	checkCached bool
	addSamples  bool

	// User data cache with 7-day expiry
	userMeCache *userMeCache

	// Torrents list cache with 45-second expiry
	torrentsCache *torrentsListCache
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*Torbox, error) {

	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
		"User-Agent":    fmt.Sprintf("Decypharr/%s (%s; %s)", version.GetInfo(), runtime.GOOS, runtime.GOARCH),
	}
	_log := logger.New(dc.Name)

	// Configure client options
	clientOpts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithRateLimiter(ratelimits["main"]),
		request.WithLogger(_log),
		request.WithProxy(dc.Proxy),
		request.WithMaxRetries(5),
		request.WithRetryableStatus(429, 502, 503, 504),
	}

	// Add endpoint-specific rate limiters for Torbox
	// These endpoints have dual rate limits: hourly AND per-minute
	//
	// Torbox Rate Limits (based on API documentation and observed behavior):
	// - General endpoints: 5 req/sec
	// - POST /torrents/createtorrent: 60/hour AND 10/min (dual limits)
	// - POST /usenet/createusenetdownload: 60/hour AND 10/min (dual limits)
	// - POST /webdl/createwebdownload: 60/hour AND 10/min (dual limits)
	// - GET /torrents/requestdl: 120/hour AND 20/min (download link generation)
	// - GET /torrents/mylist: 300/hour AND 60/min (status checks)
	// - GET /torrents/checkcached: 600/hour AND 120/min (availability checks)
	// - DELETE /torrents/controltorrent: 60/hour AND 10/min (torrent management)
	// - GET /user/me: 60/hour AND 10/min (user information - rarely called due to caching)
	clientOpts = append(clientOpts,
		// createtorrent endpoint with dual limits
		request.WithEndpointLimiter(
			"POST",
			`^/v1/api/torrents/createtorrent`,
			request.ParseMultipleRateLimits("60/hour", "10/min"),
		),
		// createusenetdownload endpoint with dual limits
		request.WithEndpointLimiter(
			"POST",
			`^/v1/api/usenet/createusenetdownload`,
			request.ParseMultipleRateLimits("60/hour", "10/min"),
		),
		// createwebdownload endpoint with dual limits
		request.WithEndpointLimiter(
			"POST",
			`^/v1/api/webdl/createwebdownload`,
			request.ParseMultipleRateLimits("60/hour", "10/min"),
		),
		// P1 Fix: requestdl endpoint with zero slack for strict rate limiting
		// This endpoint is frequently used by WebDAV for download link generation
		// Zero slack prevents burst requests that trigger 429 errors
		request.WithEndpointLimiter(
			"GET",
			`^/v1/api/torrents/requestdl`,
			request.ParseMultipleRateLimitsWithSlack(0, "120/hour", "20/min"),
		),
		// mylist endpoint - torrent status checks (very frequent)
		request.WithEndpointLimiter(
			"GET",
			`^/v1/api/torrents/mylist`,
			request.ParseMultipleRateLimits("300/hour", "60/min"),
		),
		// checkcached endpoint - availability checks (batch operations)
		request.WithEndpointLimiter(
			"GET",
			`^/v1/api/torrents/checkcached`,
			request.ParseMultipleRateLimits("600/hour", "120/min"),
		),
		// controltorrent endpoint - torrent management operations
		request.WithEndpointLimiter(
			"DELETE",
			`^/v1/api/torrents/controltorrent`,
			request.ParseMultipleRateLimits("60/hour", "10/min"),
		),
		// user/me endpoint - user information (rarely called due to 7-day caching)
		request.WithEndpointLimiter(
			"GET",
			`^/v1/api/user/me`,
			request.ParseMultipleRateLimits("60/hour", "10/min"),
		),
	)

	client := request.New(clientOpts...)

	autoExpiresLinksAfter, err := time.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}

	_log.Info().
		Str("provider", "torbox").
		Str("general_rate_limit", dc.RateLimit).
		Int("endpoint_limiters", 8).
		Msg("Torbox client initialized with enhanced endpoint-specific rate limiters")

	tb := &Torbox{
		name:                  "torbox",
		Host:                  "https://api.torbox.app/v1",
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], _log),
		DownloadUncached:      dc.DownloadUncached,
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                client,
		MountPath:             dc.Folder,
		logger:                _log,
		checkCached:           dc.CheckCached,
		addSamples:            dc.AddSamples,
		userMeCache: &userMeCache{
			data:      nil,
			expiresAt: time.Time{},
			mu:        sync.RWMutex{},
		},
		torrentsCache: &torrentsListCache{
			mu:            sync.RWMutex{},
			data:          nil,
			expiresAt:     time.Time{},
			convertedData: nil,
			dataMap:       make(map[string]*torboxInfo),
			fetchMu:       sync.Mutex{},
		},
	}

	// Fetch user data on startup
	go func() {
		if _, err := tb.GetUserMe(context.Background()); err != nil {
			_log.Warn().Err(err).Msg("Failed to fetch user data on startup")
		} else {
			_log.Info().Msg("User data fetched and cached successfully")
		}
	}()

	// Start background refresh worker
	go tb.startUserMeRefreshWorker(context.Background())

	return tb, nil
}

// GetUserMe fetches user information from Torbox API with 7-day caching
// The cache is automatically refreshed when expired
func (tb *Torbox) GetUserMe(ctx context.Context) (*UserMeData, error) {
	// Check if cached data is still valid
	tb.userMeCache.mu.RLock()
	if tb.userMeCache.data != nil && time.Now().Before(tb.userMeCache.expiresAt) {
		data := tb.userMeCache.data
		tb.userMeCache.mu.RUnlock()
		tb.logger.Debug().
			Time("expires_at", tb.userMeCache.expiresAt).
			Msg("Returning cached user data")
		return data, nil
	}
	tb.userMeCache.mu.RUnlock()

	// Fetch fresh data
	tb.logger.Debug().Msg("Fetching fresh user data from Torbox API")
	url := fmt.Sprintf("%s/api/user/me?settings=false", tb.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user data: %w", err)
	}

	var userResp UserMeResponse
	if err := json.Unmarshal(resp, &userResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	if !userResp.Success || userResp.Data == nil {
		return nil, fmt.Errorf("torbox API error: %v (detail: %s)", userResp.Error, userResp.Detail)
	}

	// Update cache with new data
	tb.userMeCache.mu.Lock()
	tb.userMeCache.data = userResp.Data
	tb.userMeCache.expiresAt = time.Now().Add(userMeCacheDuration)
	tb.userMeCache.mu.Unlock()

	tb.logger.Info().
		Int("user_id", userResp.Data.Id).
		Str("email", userResp.Data.Email).
		Bool("is_subscribed", userResp.Data.IsSubscribed).
		Int("plan", userResp.Data.Plan).
		Time("premium_expires_at", userResp.Data.PremiumExpiresAt).
		Time("cache_expires_at", tb.userMeCache.expiresAt).
		Msg("User data fetched and cached")

	return userResp.Data, nil
}

// startUserMeRefreshWorker runs a background worker that refreshes user data every 7 days
func (tb *Torbox) startUserMeRefreshWorker(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour) // Check daily if refresh is needed
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			tb.logger.Debug().Msg("User data refresh worker stopped")
			return
		case <-ticker.C:
			// Check if cache needs refresh
			tb.userMeCache.mu.RLock()
			needsRefresh := tb.userMeCache.data == nil || time.Now().After(tb.userMeCache.expiresAt)
			tb.userMeCache.mu.RUnlock()

			if needsRefresh {
				tb.logger.Debug().Msg("Cache expired, refreshing user data")
				if _, err := tb.GetUserMe(ctx); err != nil {
					tb.logger.Error().Err(err).Msg("Failed to refresh user data")
				}
			}
		}
	}
}

func (tb *Torbox) Name() string {
	return tb.name
}

func (tb *Torbox) Logger() zerolog.Logger {
	return tb.logger
}

func (tb *Torbox) IsAvailable(hashes []string) map[string]bool {
	// Check if the infohashes are available in the local cache
	result := make(map[string]bool)

	// Divide hashes into groups of 100
	for i := 0; i < len(hashes); i += 100 {
		end := i + 100
		if end > len(hashes) {
			end = len(hashes)
		}

		// Filter out empty strings
		validHashes := make([]string, 0, end-i)
		for _, hash := range hashes[i:end] {
			if hash != "" {
				validHashes = append(validHashes, hash)
			}
		}

		// If no valid hashes in this batch, continue to the next batch
		if len(validHashes) == 0 {
			continue
		}

		hashStr := strings.Join(validHashes, ",")
		url := fmt.Sprintf("%s/api/torrents/checkcached?hash=%s", tb.Host, hashStr)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := tb.client.MakeRequest(req)
		if err != nil {
			tb.logger.Error().Err(err).Msgf("Error checking availability")
			return result
		}
		var res AvailableResponse
		err = json.Unmarshal(resp, &res)
		if err != nil {
			tb.logger.Error().Err(err).Msgf("Error marshalling availability")
			return result
		}
		if res.Data == nil {
			return result
		}

		for h, c := range *res.Data {
			if c.Size > 0 {
				result[strings.ToUpper(h)] = true
			}
		}
	}
	return result
}

func (tb *Torbox) SubmitMagnet(torrent *types.Torrent) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/api/torrents/createtorrent", tb.Host)
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("magnet", torrent.Magnet.Link)
	if !torrent.DownloadUncached {
		_ = writer.WriteField("add_only_if_cached", "true")
	}
	err := writer.Close()
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodPost, url, payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var data AddMagnetResponse
	err = json.Unmarshal(resp, &data)
	if err != nil {
		return nil, err
	}
	if data.Data == nil {
		return nil, fmt.Errorf("error adding torrent")
	}
	dt := *data.Data
	torrentId := strconv.Itoa(dt.Id)
	torrent.Id = torrentId
	torrent.MountPath = tb.MountPath
	torrent.Debrid = tb.name
	torrent.Added = time.Now().Format(time.RFC3339)

	// P0 Fix: Invalidate cache when a new torrent is added
	tb.invalidateCache()

	return torrent, nil
}

func (tb *Torbox) getTorboxStatus(status string, finished bool) string {
	if finished {
		return "downloaded"
	}
	downloading := []string{"completed", "cached", "paused", "downloading", "uploading",
		"checkingResumeData", "metaDL", "pausedUP", "queuedUP", "checkingUP",
		"forcedUP", "allocating", "downloading", "metaDL", "pausedDL",
		"queuedDL", "checkingDL", "forcedDL", "checkingResumeData", "moving"}

	var determinedStatus string
	switch {
	case utils.Contains(downloading, status):
		determinedStatus = "downloading"
	default:
		determinedStatus = "error"
	}

	return determinedStatus
}

func (tb *Torbox) GetTorrent(torrentId string) (*types.Torrent, error) {
	// P1 Fix: Use O(1) map lookup instead of linear search
	if cachedInfo := tb.getTorboxInfoFromCache(torrentId); cachedInfo != nil {
		tb.logger.Debug().
			Str("torrent_id", torrentId).
			Msg("Returning torrent from cache")
		return tb.convertSingleTorboxInfoToTorrent(cachedInfo), nil
	}

	// Cache miss or expired, fetch from API
	tb.logger.Debug().
		Str("torrent_id", torrentId).
		Msg("Fetching torrent from API (cache miss)")

	url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, torrentId)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var res TorrentsListResponse
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return nil, err
	}
	if res.Data == nil || len(*res.Data) == 0 {
		return nil, fmt.Errorf("error getting torrent: no data returned")
	}
	data := &(*res.Data)[0]

	return tb.convertSingleTorboxInfoToTorrent(data), nil
}

// convertSingleTorboxInfoToTorrent converts a single torboxInfo to types.Torrent
func (tb *Torbox) convertSingleTorboxInfoToTorrent(data *torboxInfo) *types.Torrent {
	t := &types.Torrent{
		Id:               strconv.Itoa(data.Id),
		Name:             data.Name,
		Bytes:            data.Size,
		Folder:           data.Name,
		Progress:         data.Progress * 100,
		Status:           tb.getTorboxStatus(data.DownloadState, data.DownloadFinished),
		Speed:            data.DownloadSpeed,
		Seeders:          data.Seeds,
		Filename:         data.Name,
		OriginalFilename: data.Name,
		MountPath:        tb.MountPath,
		Debrid:           tb.name,
		Files:            make(map[string]types.File),
		Added:            data.CreatedAt.Format(time.RFC3339),
	}
	cfg := config.Get()

	totalFiles := 0
	skippedSamples := 0
	skippedFileType := 0
	skippedSize := 0
	validFiles := 0
	filesWithLinks := 0

	for _, f := range data.Files {
		totalFiles++
		fileName := filepath.Base(f.Name)

		if !tb.addSamples && utils.IsSampleFile(f.AbsolutePath) {
			skippedSamples++
			continue
		}
		if !cfg.IsAllowedFile(fileName) {
			skippedFileType++
			continue
		}

		if !cfg.IsSizeAllowed(f.Size) {
			skippedSize++
			continue
		}

		validFiles++
		file := types.File{
			TorrentId: t.Id,
			Id:        strconv.Itoa(f.Id),
			Name:      fileName,
			Size:      f.Size,
			Path:      f.Name,
		}

		// For downloaded torrents, set a placeholder link to indicate file is available
		if data.DownloadFinished {
			file.Link = fmt.Sprintf("torbox://%s/%d", t.Id, f.Id)
			filesWithLinks++
		}

		t.Files[fileName] = file
	}

	// Log summary only if there are issues or for debugging
	tb.logger.Debug().
		Str("torrent_id", t.Id).
		Str("torrent_name", t.Name).
		Bool("download_finished", data.DownloadFinished).
		Str("status", t.Status).
		Int("total_files", totalFiles).
		Int("valid_files", validFiles).
		Int("final_file_count", len(t.Files)).
		Msg("Torrent file processing completed")
	var cleanPath string
	if len(t.Files) > 0 {
		cleanPath = path.Clean(data.Files[0].Name)
	} else {
		cleanPath = path.Clean(data.Name)
	}

	t.OriginalFilename = strings.Split(cleanPath, "/")[0]
	t.Debrid = tb.name

	return t
}

func (tb *Torbox) UpdateTorrent(t *types.Torrent) error {
	// Try to get from cache first (P1: uses O(1) map lookup)
	var data *torboxInfo
	if cachedInfo := tb.getTorboxInfoFromCache(t.Id); cachedInfo != nil {
		tb.logger.Debug().
			Str("torrent_id", t.Id).
			Msg("Updating torrent from cache")
		data = cachedInfo
	} else {
		// Cache miss or expired, fetch from API
		tb.logger.Debug().
			Str("torrent_id", t.Id).
			Msg("Updating torrent from API (cache miss)")

		url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, t.Id)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := tb.client.MakeRequest(req)
		if err != nil {
			return err
		}
		var res TorrentsListResponse
		err = json.Unmarshal(resp, &res)
		if err != nil {
			return err
		}
		if res.Data == nil || len(*res.Data) == 0 {
			return fmt.Errorf("error getting torrent: no data returned")
		}
		data = &(*res.Data)[0]
	}

	name := data.Name

	t.Name = name
	t.Bytes = data.Size
	t.Folder = name
	t.Progress = data.Progress * 100
	t.Status = tb.getTorboxStatus(data.DownloadState, data.DownloadFinished)
	t.Speed = data.DownloadSpeed
	t.Seeders = data.Seeds
	t.Filename = name
	t.OriginalFilename = name
	t.MountPath = tb.MountPath
	t.Debrid = tb.name

	// Clear existing files map to rebuild it
	t.Files = make(map[string]types.File)

	cfg := config.Get()
	validFiles := 0
	filesWithLinks := 0

	for _, f := range data.Files {
		fileName := filepath.Base(f.Name)

		if !tb.addSamples && utils.IsSampleFile(f.AbsolutePath) {
			continue
		}

		if !cfg.IsAllowedFile(fileName) {
			continue
		}

		if !cfg.IsSizeAllowed(f.Size) {
			continue
		}

		validFiles++
		file := types.File{
			TorrentId: t.Id,
			Id:        strconv.Itoa(f.Id),
			Name:      fileName,
			Size:      f.Size,
			Path:      fileName,
		}

		// For downloaded torrents, set a placeholder link to indicate file is available
		if data.DownloadFinished {
			file.Link = fmt.Sprintf("torbox://%s/%s", t.Id, strconv.Itoa(f.Id))
			filesWithLinks++
		}

		t.Files[fileName] = file
	}

	var cleanPath string
	if len(t.Files) > 0 {
		cleanPath = path.Clean(data.Files[0].Name)
	} else {
		cleanPath = path.Clean(data.Name)
	}

	t.OriginalFilename = strings.Split(cleanPath, "/")[0]
	t.Debrid = tb.name
	return nil
}

func (tb *Torbox) CheckStatus(torrent *types.Torrent) (*types.Torrent, error) {
	for {
		err := tb.UpdateTorrent(torrent)

		if err != nil || torrent == nil {
			return torrent, err
		}
		status := torrent.Status
		if status == "downloaded" {
			tb.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
			return torrent, nil
		} else if utils.Contains(tb.GetDownloadingStatus(), status) {
			if !torrent.DownloadUncached {
				return torrent, fmt.Errorf("torrent: %s not cached", torrent.Name)
			}
			// Break out of the loop if the torrent is downloading.
			// This is necessary to prevent infinite loop since we moved to sync downloading and async processing
			return torrent, nil
		} else {
			return torrent, fmt.Errorf("torrent: %s has error", torrent.Name)
		}

	}
}

func (tb *Torbox) DeleteTorrent(torrentId string) error {
	url := fmt.Sprintf("%s/api/torrents/controltorrent/%s", tb.Host, torrentId)
	payload := map[string]string{"torrent_id": torrentId, "action": "Delete"}
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodDelete, url, bytes.NewBuffer(jsonPayload))
	if _, err := tb.client.MakeRequest(req); err != nil {
		return err
	}
	tb.logger.Info().Msgf("Torrent %s deleted from Torbox", torrentId)

	// P0 Fix: Invalidate cache when a torrent is deleted
	tb.invalidateCache()

	return nil
}

// invalidateCache clears all cached torrent data
// Should be called when torrents are added or deleted to ensure fresh data on next fetch
func (tb *Torbox) invalidateCache() {
	tb.torrentsCache.mu.Lock()
	defer tb.torrentsCache.mu.Unlock()

	tb.torrentsCache.data = nil
	tb.torrentsCache.expiresAt = time.Time{}
	tb.torrentsCache.convertedData = nil
	// Explicitly clear map to help GC
	for k := range tb.torrentsCache.dataMap {
		delete(tb.torrentsCache.dataMap, k)
	}

	tb.logger.Debug().Msg("Torrents cache invalidated")
}

func (tb *Torbox) GetFileDownloadLinks(t *types.Torrent) error {
	filesCh := make(chan types.File, len(t.Files))
	linkCh := make(chan types.DownloadLink)
	errCh := make(chan error, len(t.Files))

	var wg sync.WaitGroup
	wg.Add(len(t.Files))
	for _, file := range t.Files {
		go func() {
			defer wg.Done()
			link, err := tb.GetDownloadLink(t, &file)
			if err != nil {
				errCh <- err
				return
			}
			if link.DownloadLink != "" {
				linkCh <- link
				file.DownloadLink = link
			}
			filesCh <- file
		}()
	}
	go func() {
		wg.Wait()
		close(filesCh)
		close(linkCh)
		close(errCh)
	}()

	// Collect results
	files := make(map[string]types.File, len(t.Files))
	for file := range filesCh {
		files[file.Name] = file
	}

	// Check for errors
	for err := range errCh {
		if err != nil {
			return err // Return the first error encountered
		}
	}

	t.Files = files
	return nil
}

func (tb *Torbox) GetDownloadLink(t *types.Torrent, file *types.File) (types.DownloadLink, error) {
	url := fmt.Sprintf("%s/api/torrents/requestdl/", tb.Host)
	query := gourl.Values{}
	query.Add("torrent_id", t.Id)
	query.Add("token", tb.APIKey)
	query.Add("file_id", file.Id)
	url += "?" + query.Encode()

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		// Check if the error contains DATABASE_ERROR (HTTP 500 with DATABASE_ERROR in body)
		if strings.Contains(err.Error(), "DATABASE_ERROR") {
			tb.logger.Warn().
				Str("torrent_id", t.Id).
				Str("file_id", file.Id).
				Str("torrent_hash", t.InfoHash).
				Msg("Torrent deleted from Torbox (DATABASE_ERROR in HTTP 500 response)")
			return types.DownloadLink{}, utils.TorrentNotFoundError
		}
		tb.logger.Error().
			Err(err).
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Msg("Failed to make request to Torbox API")
		return types.DownloadLink{}, err
	}

	var data DownloadLinksResponse
	if err = json.Unmarshal(resp, &data); err != nil {
		tb.logger.Error().
			Err(err).
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Msg("Failed to unmarshal Torbox API response")
		return types.DownloadLink{}, err
	}

	if data.Data == nil {
		errorCode := (*APIResponse[string])(&data).ParseErrorCode()
		tb.logger.Error().
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Bool("success", data.Success).
			Interface("error", data.Error).
			Str("error_code", errorCode).
			Str("detail", data.Detail).
			Msg("Torbox API returned no data")

		// DATABASE_ERROR means the torrent has been deleted from Torbox
		if errorCode == ErrorCodeDatabaseError {
			tb.logger.Warn().
				Str("torrent_id", t.Id).
				Str("torrent_hash", t.InfoHash).
				Msg("Torrent deleted from Torbox (DATABASE_ERROR)")
			return types.DownloadLink{}, utils.TorrentNotFoundError
		}

		return types.DownloadLink{}, fmt.Errorf("error getting download links")
	}

	link := *data.Data
	if link == "" {
		tb.logger.Error().
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Msg("Torbox API returned empty download link")
		return types.DownloadLink{}, fmt.Errorf("error getting download links")
	}

	now := time.Now()
	dl := types.DownloadLink{
		Token:        tb.APIKey,
		Link:         file.Link,
		DownloadLink: link,
		Id:           file.Id,
		Generated:    now,
		ExpiresAt:    now.Add(tb.autoExpiresLinksAfter),
	}

	tb.accountsManager.StoreDownloadLink(dl)

	return dl, nil
}

func (tb *Torbox) GetDownloadingStatus() []string {
	return []string{"downloading"}
}

// GetTorrents fetches the list of torrents with sophisticated caching
// P0 Fix: Implements double-checked locking to prevent thundering herd
// P1 Fix: Returns stale cache on API error instead of failing completely
// P2 Fix: Caches converted torrents to avoid repeated allocations
func (tb *Torbox) GetTorrents(ctx context.Context) ([]*types.Torrent, error) {
	// First check: Quick read lock to check if cache is valid
	tb.torrentsCache.mu.RLock()
	cacheValid := tb.torrentsCache.data != nil && time.Now().Before(tb.torrentsCache.expiresAt)
	if cacheValid && tb.torrentsCache.convertedData != nil {
		// Fast path: return cached converted data
		convertedData := tb.torrentsCache.convertedData
		tb.torrentsCache.mu.RUnlock()
		tb.logger.Debug().
			Int("cached_count", len(convertedData)).
			Msg("Returning cached converted torrents list")
		return convertedData, nil
	}
	cachedData := tb.torrentsCache.data
	tb.torrentsCache.mu.RUnlock()

	// If cache is valid but converted data is nil, convert and cache
	if cacheValid {
		tb.logger.Debug().
			Int("cached_count", len(cachedData)).
			Msg("Converting cached raw data to torrents")
		converted := tb.convertTorboxInfoToTorrents(cachedData)

		// Store converted data
		tb.torrentsCache.mu.Lock()
		tb.torrentsCache.convertedData = converted
		tb.torrentsCache.mu.Unlock()

		return converted, nil
	}

	// P0 Fix: Double-checked locking pattern to prevent thundering herd
	// Use fetch mutex to ensure only one goroutine fetches when cache expires
	tb.torrentsCache.fetchMu.Lock()

	// Second check: After acquiring fetch lock, check again if another goroutine already fetched
	tb.torrentsCache.mu.RLock()
	cacheValid = tb.torrentsCache.data != nil && time.Now().Before(tb.torrentsCache.expiresAt)
	if cacheValid && tb.torrentsCache.convertedData != nil {
		convertedData := tb.torrentsCache.convertedData
		tb.torrentsCache.mu.RUnlock()
		tb.torrentsCache.fetchMu.Unlock()
		tb.logger.Debug().
			Int("cached_count", len(convertedData)).
			Msg("Another goroutine already fetched, returning cached data")
		return convertedData, nil
	}
	cachedData = tb.torrentsCache.data
	tb.torrentsCache.mu.RUnlock()

	// If cache is valid but converted data is nil, convert and cache
	if cacheValid {
		tb.torrentsCache.fetchMu.Unlock()
		converted := tb.convertTorboxInfoToTorrents(cachedData)
		tb.torrentsCache.mu.Lock()
		tb.torrentsCache.convertedData = converted
		tb.torrentsCache.mu.Unlock()
		return converted, nil
	}

	// Now we're the only goroutine that will fetch
	tb.logger.Debug().Msg("Fetching fresh torrents list from Torbox API")

	// Store old data in case API call fails (already have RLock released)
	tb.torrentsCache.mu.RLock()
	staleData := tb.torrentsCache.data
	tb.torrentsCache.mu.RUnlock()

	// Fetch fresh data from API
	offset := 0
	allTorboxInfo := make([]*torboxInfo, 0)
	iterations := 0

	for {
		// P0 Fix: Check context cancellation before each iteration
		select {
		case <-ctx.Done():
			tb.torrentsCache.fetchMu.Unlock()
			if staleData != nil {
				tb.logger.Warn().
					Err(ctx.Err()).
					Int("stale_count", len(staleData)).
					Msg("Context cancelled during fetch, returning stale cache")
				return tb.convertTorboxInfoToTorrents(staleData), nil
			}
			return nil, fmt.Errorf("context cancelled: %w", ctx.Err())
		default:
		}

		// P0 Fix: Prevent infinite loops with max iteration limit
		if iterations >= maxPaginationIterations {
			tb.logger.Warn().
				Int("iterations", iterations).
				Int("fetched_count", len(allTorboxInfo)).
				Msg("Reached max pagination iterations, stopping fetch")
			break
		}

		torboxInfoBatch, err := tb.getTorboxInfoBatch(ctx, offset)
		if err != nil {
			// P1 Fix: Don't fail completely on error, return stale cache if available
			tb.torrentsCache.fetchMu.Unlock()
			if staleData != nil {
				tb.logger.Warn().
					Err(err).
					Int("stale_count", len(staleData)).
					Msg("API fetch failed, returning stale cache")
				return tb.convertTorboxInfoToTorrents(staleData), nil
			}
			return nil, fmt.Errorf("failed to fetch torrents and no stale cache available: %w", err)
		}
		if len(torboxInfoBatch) == 0 {
			break
		}
		allTorboxInfo = append(allTorboxInfo, torboxInfoBatch...)
		offset += len(torboxInfoBatch)
		iterations++
	}

	// P1 Fix: Build dataMap for O(1) lookup
	dataMap := make(map[string]*torboxInfo, len(allTorboxInfo))
	for _, info := range allTorboxInfo {
		dataMap[strconv.Itoa(info.Id)] = info
	}

	// P2 Fix: Convert data before caching
	converted := tb.convertTorboxInfoToTorrents(allTorboxInfo)

	// Update all caches with new data (single lock)
	tb.torrentsCache.mu.Lock()
	tb.torrentsCache.data = allTorboxInfo
	tb.torrentsCache.expiresAt = time.Now().Add(torrentsListCacheDuration)
	tb.torrentsCache.dataMap = dataMap
	tb.torrentsCache.convertedData = converted
	cacheExpiresAt := tb.torrentsCache.expiresAt
	tb.torrentsCache.mu.Unlock()

	tb.torrentsCache.fetchMu.Unlock()

	tb.logger.Debug().
		Int("total_torrents", len(allTorboxInfo)).
		Time("cache_expires_at", cacheExpiresAt).
		Msg("Torrents list fetched and cached")

	return converted, nil
}

// getTorboxInfoBatch fetches a batch of torrents from Torbox API
func (tb *Torbox) getTorboxInfoBatch(ctx context.Context, offset int) ([]*torboxInfo, error) {
	url := fmt.Sprintf("%s/api/torrents/mylist?offset=%d", tb.Host, offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		// Check if the error contains DATABASE_ERROR (HTTP 500 with DATABASE_ERROR in body)
		if strings.Contains(err.Error(), "DATABASE_ERROR") {
			tb.logger.Warn().
				Msg("DATABASE_ERROR in getTorboxInfoBatch - torrent(s) deleted from Torbox")
			return nil, utils.TorrentNotFoundError
		}
		return nil, err
	}

	var res TorrentsListResponse
	if err := json.Unmarshal(resp, &res); err != nil {
		return nil, err
	}

	if !res.Success || res.Data == nil {
		errorCode := (*APIResponse[[]torboxInfo])(&res).ParseErrorCode()
		if errorCode == ErrorCodeDatabaseError {
			tb.logger.Warn().
				Str("error_code", errorCode).
				Msg("DATABASE_ERROR in getTorboxInfoBatch - torrent(s) deleted from Torbox")
			return nil, utils.TorrentNotFoundError
		}
		return nil, fmt.Errorf("torbox API error: %v", res.Error)
	}

	result := make([]*torboxInfo, len(*res.Data))
	for i := range *res.Data {
		result[i] = &(*res.Data)[i]
	}

	return result, nil
}

// getTorboxInfoFromCache retrieves a single torrent from the cache by ID
// P1 Fix: Uses O(1) map lookup instead of O(n) linear search
// P2 Fix: Returns a deep copy to prevent external mutations and ensure thread safety
// Returns nil if not found or cache is expired
func (tb *Torbox) getTorboxInfoFromCache(torrentId string) *torboxInfo {
	// Single read lock for both cache validity check and map lookup
	tb.torrentsCache.mu.RLock()
	defer tb.torrentsCache.mu.RUnlock()

	// Check if cache is valid
	cacheValid := tb.torrentsCache.data != nil && time.Now().Before(tb.torrentsCache.expiresAt)
	if !cacheValid {
		return nil
	}

	// P1 Fix: Use O(1) map lookup
	info, found := tb.torrentsCache.dataMap[torrentId]
	if !found {
		return nil
	}

	// P2 Fix: Return a deep copy to prevent external mutations affecting cached data
	// Deep copy is necessary because:
	// 1. The Files slice is shared between the original and shallow copies
	// 2. External code modifying the Files slice could cause race conditions
	// 3. Ensures thread safety when multiple goroutines access the same cached data
	//
	// THREAD SAFETY NOTE: The Md5 field is interface{} which typically contains
	// immutable data (nil, string, or number). If Md5 ever contains mutable data,
	// this deep copy would need to handle that case explicitly.
	infoCopy := *info

	// Deep copy the Files slice to prevent shared references
	if len(info.Files) > 0 {
		infoCopy.Files = make([]TorboxFile, len(info.Files))
		copy(infoCopy.Files, info.Files)
	}

	return &infoCopy
}

// convertTorboxInfoToTorrents converts torboxInfo structs to types.Torrent
func (tb *Torbox) convertTorboxInfoToTorrents(torboxInfoList []*torboxInfo) []*types.Torrent {
	torrents := make([]*types.Torrent, 0, len(torboxInfoList))
	cfg := config.Get()

	for _, data := range torboxInfoList {
		t := &types.Torrent{
			Id:               strconv.Itoa(data.Id),
			Name:             data.Name,
			Bytes:            data.Size,
			Folder:           data.Name,
			Progress:         data.Progress * 100,
			Status:           tb.getTorboxStatus(data.DownloadState, data.DownloadFinished),
			Speed:            data.DownloadSpeed,
			Seeders:          data.Seeds,
			Filename:         data.Name,
			OriginalFilename: data.Name,
			MountPath:        tb.MountPath,
			Debrid:           tb.name,
			Files:            make(map[string]types.File),
			Added:            data.CreatedAt.Format(time.RFC3339),
			InfoHash:         data.Hash,
		}

		// Process files
		for _, f := range data.Files {
			fileName := filepath.Base(f.Name)
			if !tb.addSamples && utils.IsSampleFile(f.AbsolutePath) {
				// Skip sample files
				continue
			}
			if !cfg.IsAllowedFile(fileName) {
				continue
			}
			if !cfg.IsSizeAllowed(f.Size) {
				continue
			}
			file := types.File{
				TorrentId: t.Id,
				Id:        strconv.Itoa(f.Id),
				Name:      fileName,
				Size:      f.Size,
				Path:      f.Name,
			}

			// For downloaded torrents, set a placeholder link to indicate file is available
			if data.DownloadFinished {
				file.Link = fmt.Sprintf("torbox://%s/%d", t.Id, f.Id)
			}

			t.Files[fileName] = file
		}

		// Set original filename based on first file or torrent name
		var cleanPath string
		if len(t.Files) > 0 {
			cleanPath = path.Clean(data.Files[0].Name)
		} else {
			cleanPath = path.Clean(data.Name)
		}
		t.OriginalFilename = strings.Split(cleanPath, "/")[0]

		torrents = append(torrents, t)
	}

	return torrents
}

func (tb *Torbox) GetDownloadUncached() bool {
	return tb.DownloadUncached
}

func (tb *Torbox) RefreshDownloadLinks() error {
	return nil
}

func (tb *Torbox) CheckLink(link string) error {
	return nil
}

func (tb *Torbox) GetMountPath() string {
	return tb.MountPath
}

func (tb *Torbox) GetAvailableSlots(ctx context.Context) (int, error) {
	// Get user data from cache (will fetch if not cached or expired)
	userData, err := tb.GetUserMe(ctx)
	if err != nil {
		tb.logger.Error().Err(err).Msg("Failed to get user data for slot calculation")
		return 0, fmt.Errorf("failed to get user data: %w", err)
	}

	// Calculate total slots based on plan and additional slots
	totalSlots := getTotalSlots(userData.Plan, userData.AdditionalConcurrentSlots)

	// Get current active torrents count
	torrents, err := tb.GetTorrents(ctx)
	if err != nil {
		tb.logger.Error().Err(err).Msg("Failed to get torrents list for slot calculation")
		return 0, fmt.Errorf("failed to get torrents: %w", err)
	}

	// Count active torrents (downloading or downloaded status)
	activeTorrents := 0
	for _, torrent := range torrents {
		if torrent.Status == "downloading" || torrent.Status == "downloaded" {
			activeTorrents++
		}
	}

	// Calculate available slots
	availableSlots := totalSlots - activeTorrents
	if availableSlots < 0 {
		availableSlots = 0
	}

	tb.logger.Debug().
		Int("total_slots", totalSlots).
		Int("active_torrents", activeTorrents).
		Int("available_slots", availableSlots).
		Int("plan", userData.Plan).
		Int("additional_slots", userData.AdditionalConcurrentSlots).
		Msg("Calculated available slots")

	return availableSlots, nil
}

func (tb *Torbox) GetProfile() (*types.Profile, error) {
	return nil, nil
}

func (tb *Torbox) AccountManager() *account.Manager {
	return tb.accountsManager
}

func (tb *Torbox) SyncAccounts() error {
	return nil
}
