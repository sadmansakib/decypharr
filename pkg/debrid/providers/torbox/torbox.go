package torbox

import (
	"bytes"
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
}

// validateTorboxRateLimit validates and returns a safe rate limiter for Torbox API
// According to Torbox API specification:
// - General endpoints: 5/sec per IP (no edge rate limiting)
// - Specific endpoints have their own limits (handled separately)
func validateTorboxRateLimit(rateLimit string, logger zerolog.Logger) (ratelimit.Limiter, bool) {
	if rateLimit == "" {
		logger.Warn().Msg("Empty rate limit provided for Torbox, using default 5/second")
		return request.ParseRateLimit("5/second"), true
	}

	// Parse the provided rate limit
	parsed := request.ParseRateLimit(rateLimit)
	if parsed == nil {
		logger.Warn().Str("rate_limit", rateLimit).Msg("Invalid rate limit format for Torbox, using fallback 5/second")
		return request.ParseRateLimit("5/second"), true
	}

	// Extract rate information to validate against Torbox limits
	parts := strings.SplitN(rateLimit, "/", 2)
	if len(parts) != 2 {
		logger.Warn().Str("rate_limit", rateLimit).Msg("Invalid rate limit format for Torbox, using fallback 5/second")
		return request.ParseRateLimit("5/second"), true
	}

	count, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || count <= 0 {
		logger.Warn().Str("rate_limit", rateLimit).Msg("Invalid rate limit count for Torbox, using fallback 5/second")
		return request.ParseRateLimit("5/second"), true
	}

	unit := strings.ToLower(strings.TrimSpace(parts[1]))
	unit = strings.TrimSuffix(unit, "s")

	// Check if the rate exceeds Torbox's 5/second limit
	var ratePerSecond float64
	switch unit {
	case "second", "sec":
		ratePerSecond = float64(count)
	case "minute", "min":
		ratePerSecond = float64(count) / 60.0
	case "hour", "hr":
		ratePerSecond = float64(count) / 3600.0
	case "day", "d":
		ratePerSecond = float64(count) / 86400.0
	default:
		logger.Warn().Str("rate_limit", rateLimit).Str("unit", unit).Msg("Unknown rate limit unit for Torbox, using fallback 5/second")
		return request.ParseRateLimit("5/second"), true
	}

	// Torbox allows maximum 5/second
	if ratePerSecond > 5.0 {
		logger.Warn().
			Str("provided_rate_limit", rateLimit).
			Float64("rate_per_second", ratePerSecond).
			Msg("Rate limit exceeds Torbox API specification (5/second), using fallback 5/second")
		return request.ParseRateLimit("5/second"), true
	}

	// Rate limit is valid and within spec
	return parsed, false
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*Torbox, error) {

	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
		"User-Agent":    fmt.Sprintf("Decypharr/%s (%s; %s)", version.GetInfo(), runtime.GOOS, runtime.GOARCH),
	}
	_log := logger.New(dc.Name)

	// Validate and potentially fallback the main rate limiter for Torbox API compliance
	validatedMainRL, hadFallback := validateTorboxRateLimit(dc.RateLimit, _log)
	if hadFallback {
		_log.Info().Str("fallback_rate_limit", "5/second").Msg("Applied Torbox API compliant rate limit fallback")
	}

	// Update the main rate limiter with validated one
	ratelimits["main"] = validatedMainRL

	// Create endpoint-specific rate limiters for Torbox
	createTorrentLimiter := request.NewMultiRateLimiter("60/hour", "10/minute")
	createUsenetDownloadLimiter := request.NewMultiRateLimiter("60/hour", "10/minute")
	createWebDownloadLimiter := request.NewMultiRateLimiter("60/hour", "10/minute")
	endpointRateLimiters := []request.EndpointRateLimiter{
		{
			Pattern:  "/api/torrents/createtorrent",
			Limiters: []ratelimit.Limiter{createTorrentLimiter},
		},
		{
			Pattern:  "/api/usenet/createusenetdownload",
			Limiters: []ratelimit.Limiter{createUsenetDownloadLimiter},
		},
		{
			Pattern:  "/api/webdl/createwebdownload",
			Limiters: []ratelimit.Limiter{createWebDownloadLimiter},
		},
	}

	client := request.New(
		request.WithHeaders(headers),
		request.WithRateLimiter(ratelimits["main"]),
		request.WithEndpointRateLimiters(endpointRateLimiters),
		request.WithLogger(_log),
		request.WithProxy(dc.Proxy),
	)
	autoExpiresLinksAfter, err := time.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}

	return &Torbox{
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
	}, nil
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
		// Check if the error is a DATABASE_ERROR
		errorStr := ""
		if data.Error != nil {
			errorStr = fmt.Sprintf("%v", data.Error)
		}
		if (data.Detail != "" && strings.Contains(strings.ToUpper(data.Detail), "DATABASE_ERROR")) ||
			(errorStr != "" && strings.Contains(strings.ToUpper(errorStr), "DATABASE_ERROR")) {
			tb.logger.Warn().
				Str("magnet", torrent.Magnet.Link).
				Str("detail", data.Detail).
				Str("error", errorStr).
				Msg("Torbox returned DATABASE_ERROR when adding torrent")
			return nil, utils.DatabaseError
		}
		return nil, fmt.Errorf("error adding torrent")
	}
	dt := *data.Data
	torrentId := strconv.Itoa(dt.Id)
	torrent.Id = torrentId
	torrent.MountPath = tb.MountPath
	torrent.Debrid = tb.name
	torrent.Added = time.Now().Format(time.RFC3339)

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
	url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, torrentId)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var res InfoResponse
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return nil, err
	}
	data := res.Data
	if data == nil {
		// Check if the error is a DATABASE_ERROR
		errorStr := ""
		if res.Error != nil {
			errorStr = fmt.Sprintf("%v", res.Error)
		}
		if (res.Detail != "" && strings.Contains(strings.ToUpper(res.Detail), "DATABASE_ERROR")) ||
			(errorStr != "" && strings.Contains(strings.ToUpper(errorStr), "DATABASE_ERROR")) {
			tb.logger.Warn().
				Str("torrent_id", torrentId).
				Str("detail", res.Detail).
				Str("error", errorStr).
				Msg("Torbox returned DATABASE_ERROR when getting torrent")
			return nil, utils.DatabaseError
		}
		return nil, fmt.Errorf("error getting torrent")
	}
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

	return t, nil
}

func (tb *Torbox) UpdateTorrent(t *types.Torrent) error {
	url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, t.Id)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return err
	}
	var res InfoResponse
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return err
	}
	data := res.Data
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
	return nil
}

func (tb *Torbox) GetFileDownloadLinks(t *types.Torrent) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	files := make(map[string]types.File)
	links := make(map[string]types.DownloadLink)

	_files := t.GetFiles()
	wg.Add(len(_files))

	for _, f := range _files {
		go func(file types.File) {
			defer wg.Done()

			link, err := tb.GetDownloadLink(t, &file)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}

			if link.DownloadLink != "" {
				file.DownloadLink = link
				mu.Lock()
				files[file.Name] = file
				links[link.DownloadLink] = link
				mu.Unlock()
			} else {
				// Still add file even without download link
				mu.Lock()
				files[file.Name] = file
				mu.Unlock()
			}
		}(f)
	}

	wg.Wait()

	if firstErr != nil {
		return firstErr
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
		tb.logger.Error().
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Bool("success", data.Success).
			Interface("error", data.Error).
			Str("detail", data.Detail).
			Msg("Torbox API returned no data")

		// Check if the error is a DATABASE_ERROR
		errorStr := ""
		if data.Error != nil {
			errorStr = fmt.Sprintf("%v", data.Error)
		}
		if (data.Detail != "" && strings.Contains(strings.ToUpper(data.Detail), "DATABASE_ERROR")) ||
			(errorStr != "" && strings.Contains(strings.ToUpper(errorStr), "DATABASE_ERROR")) {
			tb.logger.Warn().
				Str("torrent_id", t.Id).
				Str("file_id", file.Id).
				Str("detail", data.Detail).
				Str("error", errorStr).
				Msg("Torbox returned DATABASE_ERROR")
			return types.DownloadLink{}, utils.DatabaseError
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

func (tb *Torbox) GetTorrents() ([]*types.Torrent, error) {
	offset := 0
	allTorrents := make([]*types.Torrent, 0)

	for {
		torrents, err := tb.getTorrents(offset)
		if err != nil {
			break
		}
		if len(torrents) == 0 {
			break
		}
		allTorrents = append(allTorrents, torrents...)
		offset += len(torrents)
	}
	return allTorrents, nil
}

func (tb *Torbox) getTorrents(offset int) ([]*types.Torrent, error) {
	url := fmt.Sprintf("%s/api/torrents/mylist?offset=%d", tb.Host, offset)
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

	if !res.Success || res.Data == nil {
		return nil, fmt.Errorf("torbox API error: %v", res.Error)
	}

	torrents := make([]*types.Torrent, 0, len(*res.Data))
	cfg := config.Get()

	for _, data := range *res.Data {
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

	return torrents, nil
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

func (tb *Torbox) GetAvailableSlots() (int, error) {
	//TODO: Implement the logic to check available slots for Torbox
	return 0, fmt.Errorf("not implemented")
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
