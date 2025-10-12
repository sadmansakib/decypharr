package debridlink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"go.uber.org/ratelimit"

	"net/http"
	"strings"
)

type DebridLink struct {
	name             string
	Host             string `json:"host"`
	APIKey           string
	accountsManager  *account.Manager
	DownloadUncached bool
	client           *request.Client

	autoExpiresLinksAfter time.Duration

	MountPath   string
	logger      zerolog.Logger
	checkCached bool
	addSamples  bool

	Profile *types.Profile `json:"profile,omitempty"`
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*DebridLink, error) {
	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
		"Content-Type":  "application/json",
	}
	_log := logger.New(dc.Name)
	client := request.New(
		request.WithHeaders(headers),
		request.WithLogger(_log),
		request.WithRateLimiter(ratelimits["main"]),
		request.WithProxy(dc.Proxy),
	)

	autoExpiresLinksAfter, err := time.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}
	return &DebridLink{
		name:                  "debridlink",
		Host:                  "https://debrid-link.com/api/v2",
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], _log),
		DownloadUncached:      dc.DownloadUncached,
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                client,
		MountPath:             dc.Folder,
		logger:                logger.New(dc.Name),
		checkCached:           dc.CheckCached,
		addSamples:            dc.AddSamples,
	}, nil
}

func (dl *DebridLink) Name() string {
	return dl.name
}

func (dl *DebridLink) Logger() zerolog.Logger {
	return dl.logger
}

func (dl *DebridLink) IsAvailable(ctx context.Context, hashes []string) map[string]bool {
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
		url := fmt.Sprintf("%s/seedbox/cached/%s", dl.Host, hashStr)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			dl.logger.Error().Err(err).Msg("Failed to create request for checking availability")
			return result
		}
		resp, err := dl.client.MakeRequest(req)
		if err != nil {
			dl.logger.Error().Err(err).Msgf("Error checking availability")
			return result
		}
		var data AvailableResponse
		err = json.Unmarshal(resp, &data)
		if err != nil {
			dl.logger.Error().Err(err).Msgf("Error marshalling availability")
			return result
		}
		if data.Value == nil {
			return result
		}
		value := *data.Value
		for _, h := range hashes[i:end] {
			_, exists := value[h]
			if exists {
				result[h] = true
			}
		}
	}
	return result
}

func (dl *DebridLink) GetTorrent(ctx context.Context, torrentId string) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/seedbox/%s", dl.Host, torrentId)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for getting torrent: %w", err)
	}
	resp, err := dl.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var res torrentInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return nil, err
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error getting torrent")
	}
	data := *res.Value

	if len(data) == 0 {
		return nil, fmt.Errorf("torrent not found")
	}
	t := data[0]
	name := utils.RemoveInvalidChars(t.Name)
	torrent := &types.Torrent{
		Id:               t.ID,
		Name:             name,
		Bytes:            t.TotalSize,
		Status:           "downloaded",
		Filename:         name,
		OriginalFilename: name,
		MountPath:        dl.MountPath,
		Debrid:           dl.name,
		Added:            time.Unix(t.Created, 0).Format(time.RFC3339),
	}
	cfg := config.Get()
	for _, f := range t.Files {
		if !cfg.IsSizeAllowed(f.Size) {
			continue
		}
		file := types.File{
			TorrentId: t.ID,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			Link:      f.DownloadURL,
		}
		torrent.Files[file.Name] = file
	}

	return torrent, nil
}

func (dl *DebridLink) UpdateTorrent(ctx context.Context, t *types.Torrent) error {
	url := fmt.Sprintf("%s/seedbox/list?ids=%s", dl.Host, t.Id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for updating torrent: %w", err)
	}
	resp, err := dl.client.MakeRequest(req)
	if err != nil {
		return err
	}
	var res torrentInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return err
	}
	if !res.Success {
		return fmt.Errorf("error getting torrent")
	}
	if res.Value == nil {
		return fmt.Errorf("torrent not found")
	}
	dt := *res.Value

	if len(dt) == 0 {
		return fmt.Errorf("torrent not found")
	}
	data := dt[0]
	status := "downloading"
	if data.Status == 100 {
		status = "downloaded"
	}
	name := utils.RemoveInvalidChars(data.Name)
	t.Id = data.ID
	t.Name = name
	t.Bytes = data.TotalSize
	t.Folder = name
	t.Progress = data.DownloadPercent
	t.Status = status
	t.Speed = data.DownloadSpeed
	t.Seeders = data.PeersConnected
	t.Filename = name
	t.OriginalFilename = name
	t.Added = time.Unix(data.Created, 0).Format(time.RFC3339)
	cfg := config.Get()
	now := time.Now()
	for _, f := range data.Files {
		if !cfg.IsSizeAllowed(f.Size) {
			continue
		}
		file := types.File{
			TorrentId: t.Id,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			Link:      f.DownloadURL,
		}
		link := types.DownloadLink{
			Token:        dl.APIKey,
			Filename:     f.Name,
			Link:         f.DownloadURL,
			DownloadLink: f.DownloadURL,
			Generated:    now,
			ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
		}
		file.DownloadLink = link
		t.Files[f.Name] = file
		dl.accountsManager.StoreDownloadLink(link)
	}

	return nil
}

func (dl *DebridLink) SubmitMagnet(ctx context.Context, t *types.Torrent) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/seedbox/add", dl.Host)
	payload := map[string]string{"url": t.Magnet.Link}
	jsonPayload, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request for submitting magnet: %w", err)
	}
	resp, err := dl.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var res SubmitTorrentInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return nil, err
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error adding torrent")
	}
	data := *res.Value
	status := "downloading"
	name := utils.RemoveInvalidChars(data.Name)
	t.Id = data.ID
	t.Name = name
	t.Bytes = data.TotalSize
	t.Folder = name
	t.Progress = data.DownloadPercent
	t.Status = status
	t.Speed = data.DownloadSpeed
	t.Seeders = data.PeersConnected
	t.Filename = name
	t.OriginalFilename = name
	t.MountPath = dl.MountPath
	t.Debrid = dl.name
	t.Added = time.Unix(data.Created, 0).Format(time.RFC3339)
	now := time.Now()
	for _, f := range data.Files {
		file := types.File{
			TorrentId: t.Id,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			Link:      f.DownloadURL,
			Generated: now,
		}
		link := types.DownloadLink{
			Token:        dl.APIKey,
			Filename:     f.Name,
			Link:         f.DownloadURL,
			DownloadLink: f.DownloadURL,
			Generated:    now,
			ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
		}
		file.DownloadLink = link
		t.Files[f.Name] = file
		dl.accountsManager.StoreDownloadLink(link)
	}

	return t, nil
}

func (dl *DebridLink) CheckStatus(ctx context.Context, torrent *types.Torrent) (*types.Torrent, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return torrent, fmt.Errorf("context cancelled during status check: %w", ctx.Err())
		case <-ticker.C:
			err := dl.UpdateTorrent(ctx, torrent)
			if err != nil || torrent == nil {
				return torrent, err
			}
			status := torrent.Status
			if status == "downloaded" {
				dl.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
				return torrent, nil
			} else if utils.Contains(dl.GetDownloadingStatus(), status) {
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
}

func (dl *DebridLink) DeleteTorrent(ctx context.Context, torrentId string) error {
	url := fmt.Sprintf("%s/seedbox/%s/remove", dl.Host, torrentId)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for deleting torrent: %w", err)
	}
	if _, err := dl.client.MakeRequest(req); err != nil {
		return err
	}
	dl.logger.Info().Msgf("Torrent: %s deleted from DebridLink", torrentId)
	return nil
}

func (dl *DebridLink) GetFileDownloadLinks(ctx context.Context, t *types.Torrent) error {
	// Download links are already generated
	return nil
}

func (dl *DebridLink) RefreshDownloadLinks(ctx context.Context) error {
	return nil
}

func (dl *DebridLink) GetDownloadLink(ctx context.Context, t *types.Torrent, file *types.File) (types.DownloadLink, error) {
	return dl.accountsManager.GetDownloadLink(file.Link)
}

func (dl *DebridLink) GetDownloadingStatus() []string {
	return []string{"downloading"}
}

func (dl *DebridLink) GetDownloadUncached() bool {
	return dl.DownloadUncached
}

func (dl *DebridLink) GetTorrents(ctx context.Context) ([]*types.Torrent, error) {
	page := 0
	perPage := 100
	torrents := make([]*types.Torrent, 0)
	for {
		t, err := dl.getTorrents(ctx, page, perPage)
		if err != nil {
			break
		}
		if len(t) == 0 {
			break
		}
		torrents = append(torrents, t...)
		page++
	}
	return torrents, nil
}

func (dl *DebridLink) getTorrents(ctx context.Context, page, perPage int) ([]*types.Torrent, error) {
	url := fmt.Sprintf("%s/seedbox/list?page=%d&perPage=%d", dl.Host, page, perPage)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for getting torrents: %w", err)
	}
	resp, err := dl.client.MakeRequest(req)
	torrents := make([]*types.Torrent, 0)
	if err != nil {
		return torrents, err
	}
	var res torrentInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		dl.logger.Error().Err(err).Msgf("Error unmarshalling torrent info")
		return torrents, err
	}

	data := *res.Value

	if len(data) == 0 {
		return torrents, nil
	}
	for _, t := range data {
		if t.Status != 100 {
			continue
		}
		torrent := &types.Torrent{
			Id:               t.ID,
			Name:             t.Name,
			Bytes:            t.TotalSize,
			Status:           "downloaded",
			Filename:         t.Name,
			OriginalFilename: t.Name,
			InfoHash:         t.HashString,
			Files:            make(map[string]types.File),
			Debrid:           dl.name,
			MountPath:        dl.MountPath,
			Added:            time.Unix(t.Created, 0).Format(time.RFC3339),
		}
		cfg := config.Get()
		now := time.Now()
		for _, f := range t.Files {
			if !cfg.IsSizeAllowed(f.Size) {
				continue
			}
			file := types.File{
				TorrentId: torrent.Id,
				Id:        f.ID,
				Name:      f.Name,
				Size:      f.Size,
				Path:      f.Name,
				Link:      f.DownloadURL,
			}
			link := types.DownloadLink{
				Token:        dl.APIKey,
				Filename:     f.Name,
				Link:         f.DownloadURL,
				DownloadLink: f.DownloadURL,
				Generated:    now,
				ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
			}
			file.DownloadLink = link
			torrent.Files[f.Name] = file
			dl.accountsManager.StoreDownloadLink(link)
		}
		torrents = append(torrents, torrent)
	}

	return torrents, nil
}

func (dl *DebridLink) CheckLink(ctx context.Context, link string) error {
	return nil
}

func (dl *DebridLink) GetMountPath() string {
	return dl.MountPath
}

func (dl *DebridLink) GetAvailableSlots(ctx context.Context) (int, error) {
	//TODO: Implement the logic to check available slots for DebridLink
	return 0, fmt.Errorf("GetAvailableSlots not implemented for DebridLink")
}

func (dl *DebridLink) GetProfile(ctx context.Context) (*types.Profile, error) {
	if dl.Profile != nil {
		return dl.Profile, nil
	}
	url := fmt.Sprintf("%s/account/infos", dl.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for getting profile: %w", err)
	}
	resp, err := dl.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var res UserInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		dl.logger.Error().Err(err).Msgf("Error unmarshalling user info")
		return nil, err
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error getting user info")
	}
	data := *res.Value
	expiration := time.Unix(data.PremiumLeft, 0)
	profile := &types.Profile{
		Id:         1,
		Username:   data.Username,
		Name:       dl.name,
		Email:      data.Email,
		Points:     data.Points,
		Premium:    data.PremiumLeft,
		Expiration: expiration,
	}
	if expiration.IsZero() {
		profile.Expiration = time.Now().AddDate(1, 0, 0) // Default to 1 year if no expiration
	}
	if data.PremiumLeft > 0 {
		profile.Type = "premium"
	} else {
		profile.Type = "free"
	}
	dl.Profile = profile
	return profile, nil
}

func (dl *DebridLink) AccountManager() *account.Manager {
	return dl.accountsManager
}

func (dl *DebridLink) SyncAccounts(ctx context.Context) error {
	return nil
}
