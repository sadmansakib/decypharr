package debrid

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/alldebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/debridlink"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/realdebrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/providers/torbox"
	debridStore "github.com/sirrobot01/decypharr/pkg/debrid/store"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/rclone"
	"go.uber.org/ratelimit"
)

type Debrid struct {
	cache  *debridStore.Cache // Could be nil if not using WebDAV
	client common.Client      // HTTP client for making requests to the debrid service
}

func (de *Debrid) Client() common.Client {
	return de.client
}

func (de *Debrid) Cache() *debridStore.Cache {
	return de.cache
}

func (de *Debrid) Reset() {
	if de.cache != nil {
		de.cache.Reset()
	}
}

type Storage struct {
	debrids  map[string]*Debrid
	mu       sync.RWMutex
	lastUsed string
}

func NewStorage(rcManager *rclone.Manager) *Storage {
	cfg := config.Get()

	_logger := logger.Default()

	debrids := make(map[string]*Debrid)

	bindAddress := cfg.BindAddress
	if bindAddress == "" {
		bindAddress = "localhost"
	}
	webdavUrl := fmt.Sprintf("http://%s:%s%s/webdav", bindAddress, cfg.Port, cfg.URLBase)

	for _, dc := range cfg.Debrids {
		client, err := createDebridClient(dc)
		if err != nil {
			_logger.Error().Err(err).Str("Debrid", dc.Name).Msg("failed to connect to debrid client")
			continue
		}
		var (
			cache   *debridStore.Cache
			mounter *rclone.Mount
		)
		_log := client.Logger()
		if dc.UseWebDav {
			if cfg.Rclone.Enabled && rcManager != nil {
				mounter = rclone.NewMount(dc.Name, dc.RcloneMountPath, webdavUrl, rcManager)
			}
			cache = debridStore.NewDebridCache(dc, client, mounter)
			_log.Info().Msg("Debrid Service started with WebDAV")
		} else {
			_log.Info().Msg("Debrid Service started")
		}
		debrids[dc.Name] = &Debrid{
			cache:  cache,
			client: client,
		}
	}

	d := &Storage{
		debrids:  debrids,
		lastUsed: "",
	}
	return d
}

func (d *Storage) Debrid(name string) *Debrid {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if debrid, exists := d.debrids[name]; exists {
		return debrid
	}
	return nil
}

func (d *Storage) StartWorker(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context cannot be nil for StartWorker")
	}

	// Start syncAccounts worker
	go d.syncAccountsWorker(ctx)

	// Start bandwidth reset worker
	go d.checkBandwidthWorker(ctx)

	return nil
}

func (d *Storage) checkBandwidthWorker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.checkAccountBandwidth()
			}
		}
	}()
}

func (d *Storage) checkAccountBandwidth() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, debrid := range d.debrids {
		if debrid == nil || debrid.client == nil {
			continue
		}
		accountManager := debrid.client.AccountManager()
		if accountManager == nil {
			continue
		}
		accountManager.CheckAndResetBandwidth()
	}
}

func (d *Storage) syncAccountsWorker(ctx context.Context) {
	_ = d.syncAccounts(ctx)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = d.syncAccounts(ctx)
			}
		}
	}()

}

func (d *Storage) syncAccounts(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for name, debrid := range d.debrids {
		if debrid == nil || debrid.client == nil {
			continue
		}
		_log := debrid.client.Logger()
		if err := debrid.client.SyncAccounts(ctx); err != nil {
			_log.Error().Err(err).Msgf("Failed to sync account for %s", name)
			continue
		}
	}
	return nil
}

func (d *Storage) Debrids() map[string]*Debrid {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// Use maps.Clone for efficient map cloning, then filter out nil entries
	debridsCopy := maps.Clone(d.debrids)
	for name, debrid := range debridsCopy {
		if debrid == nil {
			delete(debridsCopy, name)
		}
	}
	return debridsCopy
}

func (d *Storage) Client(name string) common.Client {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if client, exists := d.debrids[name]; exists {
		return client.client
	}
	return nil
}

func (d *Storage) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Reset all debrid clients and caches
	for _, debrid := range d.debrids {
		if debrid != nil {
			debrid.Reset()
		}
	}

	// Clear the debrids map using built-in clear()
	clear(d.debrids)
	d.lastUsed = ""
}

func (d *Storage) Clients() map[string]common.Client {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// Build filtered map of clients
	clientsCopy := make(map[string]common.Client, len(d.debrids))
	for name, debrid := range d.debrids {
		if debrid != nil && debrid.client != nil {
			clientsCopy[name] = debrid.client
		}
	}
	return clientsCopy
}

func (d *Storage) Caches() map[string]*debridStore.Cache {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// Build filtered map of caches
	cachesCopy := make(map[string]*debridStore.Cache, len(d.debrids))
	for name, debrid := range d.debrids {
		if debrid != nil && debrid.cache != nil {
			cachesCopy[name] = debrid.cache
		}
	}
	return cachesCopy
}

func (d *Storage) FilterClients(filter func(common.Client) bool) map[string]common.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Build filtered map of clients based on filter function
	filteredClients := make(map[string]common.Client, len(d.debrids))
	for name, client := range d.debrids {
		if client != nil && filter(client.client) {
			filteredClients[name] = client.client
		}
	}
	return filteredClients
}

func createDebridClient(dc config.Debrid) (common.Client, error) {
	rateLimits := map[string]ratelimit.Limiter{}

	mainRL := request.ParseRateLimit(dc.RateLimit)
	repairRL := request.ParseRateLimit(cmp.Or(dc.RepairRateLimit, dc.RateLimit))
	downloadRL := request.ParseRateLimit(cmp.Or(dc.DownloadRateLimit, dc.RateLimit))

	rateLimits["main"] = mainRL
	rateLimits["repair"] = repairRL
	rateLimits["download"] = downloadRL

	switch dc.Name {
	case "realdebrid":
		return realdebrid.New(dc, rateLimits)
	case "torbox":
		return torbox.New(dc, rateLimits)
	case "debridlink":
		return debridlink.New(dc, rateLimits)
	case "alldebrid":
		return alldebrid.New(dc, rateLimits)
	default:
		return realdebrid.New(dc, rateLimits)
	}
}

func Process(ctx context.Context, store *Storage, selectedDebrid string, magnet *utils.Magnet, a *arr.Arr, action string, overrideDownloadUncached bool) (*types.Torrent, error) {

	debridTorrent := &types.Torrent{
		InfoHash: magnet.InfoHash,
		Magnet:   magnet,
		Name:     magnet.Name,
		Arr:      a,
		Size:     magnet.Size,
		Files:    make(map[string]types.File),
	}

	clients := store.FilterClients(func(c common.Client) bool {
		if selectedDebrid != "" && c.Name() != selectedDebrid {
			return false
		}
		return true
	})

	if len(clients) == 0 {
		return nil, fmt.Errorf("no debrid clients available")
	}

	errs := make([]error, 0, len(clients))

	// Override first, arr second, debrid third

	if overrideDownloadUncached {
		debridTorrent.DownloadUncached = true
	} else if a.DownloadUncached != nil {
		// Arr cached is set
		debridTorrent.DownloadUncached = *a.DownloadUncached
	} else {
		debridTorrent.DownloadUncached = false
	}

	for _, db := range clients {
		_logger := db.Logger()
		_logger.Info().
			Str("Debrid", db.Name()).
			Str("Arr", a.Name).
			Str("Hash", debridTorrent.InfoHash).
			Str("Name", debridTorrent.Name).
			Str("Action", action).
			Msg("Processing torrent")

		if !overrideDownloadUncached && a.DownloadUncached == nil {
			debridTorrent.DownloadUncached = db.GetDownloadUncached()
		}

		dbt, err := db.SubmitMagnet(ctx, debridTorrent)
		if err != nil || dbt == nil || dbt.Id == "" {
			errs = append(errs, err)
			continue
		}
		dbt.Arr = a
		_logger.Info().Str("id", dbt.Id).Msgf("Torrent: %s submitted to %s", dbt.Name, db.Name())
		store.lastUsed = db.Name()

		torrent, err := db.CheckStatus(ctx, dbt)
		if err != nil && torrent != nil && torrent.Id != "" {
			// Delete the torrent if it was not downloaded
			go func(id string) {
				_ = db.DeleteTorrent(ctx, id)
			}(torrent.Id)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if torrent == nil {
			errs = append(errs, fmt.Errorf("torrent %s returned nil after checking status", dbt.Name))
			continue
		}
		return torrent, nil
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("failed to process torrent: no clients available")
	}
	return nil, fmt.Errorf("failed to process torrent: %w", errors.Join(errs...))
}
