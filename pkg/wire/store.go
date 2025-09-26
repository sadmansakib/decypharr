package wire

import (
	"cmp"
	"context"
	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/rclone"
	"github.com/sirrobot01/decypharr/pkg/repair"
	"sync"
	"time"
)

type Store struct {
	repair             *repair.Repair
	arr                *arr.Storage
	debrid             *debrid.Storage
	rcloneManager      *rclone.Manager
	importsQueue       *ImportQueue // Queued import requests(probably from too_many_active_downloads)
	torrents           *TorrentStorage
	logger             zerolog.Logger
	refreshInterval    time.Duration
	skipPreCache       bool
	downloadSemaphore  chan struct{}
	removeStalledAfter time.Duration // Duration after which stalled torrents are removed
	scheduler          gocron.Scheduler
	cacheWorkersCancel context.CancelFunc // Cancel function for cache workers
}

var (
	instance *Store
	once     sync.Once
	mu       sync.RWMutex // Protects instance access during reset operations
)

// Get returns the singleton instance
func Get() *Store {
	mu.RLock()
	if instance != nil {
		defer mu.RUnlock()
		return instance
	}
	mu.RUnlock()

	once.Do(func() {
		mu.Lock()
		defer mu.Unlock()

		// Double-check after acquiring write lock
		if instance != nil {
			return
		}

		cfg := config.Get()
		qbitCfg := cfg.QBitTorrent

		// Create rclone manager if enabled
		var rcManager *rclone.Manager
		if cfg.Rclone.Enabled {
			rcManager = rclone.NewManager()
		}

		// Create services with dependencies
		arrs := arr.NewStorage()
		deb := debrid.NewStorage(rcManager)

		scheduler, err := gocron.NewScheduler(gocron.WithLocation(time.Local), gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")))
		if err != nil {
			scheduler, _ = gocron.NewScheduler(gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")))
		}

		// Create the instance atomically
		newInstance := &Store{
			repair:            repair.New(arrs, deb),
			arr:               arrs,
			debrid:            deb,
			rcloneManager:     rcManager,
			torrents:          newTorrentStorage(cfg.TorrentsFile()),
			logger:            logger.Default(), // Use default logger [decypharr]
			refreshInterval:   time.Duration(cmp.Or(qbitCfg.RefreshInterval, 30)) * time.Second,
			skipPreCache:      qbitCfg.SkipPreCache,
			downloadSemaphore: make(chan struct{}, cmp.Or(qbitCfg.MaxDownloads, 5)),
			importsQueue:      NewImportQueue(context.Background(), 1000),
			scheduler:         scheduler,
		}

		if cfg.RemoveStalledAfter != "" {
			removeStalledAfter, err := time.ParseDuration(cfg.RemoveStalledAfter)
			if err == nil {
				newInstance.removeStalledAfter = removeStalledAfter
			}
		}

		// Assign the fully initialized instance atomically
		instance = newInstance
	})

	mu.RLock()
	defer mu.RUnlock()
	return instance
}

func Reset() {
	mu.Lock()
	defer mu.Unlock()

	// Capture the current instance to avoid race conditions
	currentInstance := instance
	if currentInstance != nil {
		// Stop workers first
		currentInstance.StopWorkers()

		if currentInstance.debrid != nil {
			currentInstance.debrid.Reset()
		}

		if currentInstance.rcloneManager != nil {
			err := currentInstance.rcloneManager.Stop()
			if err != nil {
				currentInstance.logger.Error().Err(err).Msg("Failed to stop rclone manager")
			}
		}

		if currentInstance.importsQueue != nil {
			currentInstance.importsQueue.Close()
		}
		if currentInstance.torrents != nil {
			if err := currentInstance.torrents.Close(); err != nil {
				currentInstance.logger.Error().Err(err).Msg("Failed to close torrent storage")
			}
		}
		if currentInstance.downloadSemaphore != nil {
			// Close the semaphore channel
			close(currentInstance.downloadSemaphore)
		}

		if currentInstance.scheduler != nil {
			_ = currentInstance.scheduler.StopJobs()
			_ = currentInstance.scheduler.Shutdown()
		}

		// Cancel cache workers if still running
		if currentInstance.cacheWorkersCancel != nil {
			currentInstance.cacheWorkersCancel()
		}
	}

	// Reset the singleton state atomically
	once = sync.Once{}
	instance = nil
}

func (s *Store) Arr() *arr.Storage {
	return s.arr
}
func (s *Store) Debrid() *debrid.Storage {
	return s.debrid
}
func (s *Store) Repair() *repair.Repair {
	return s.repair
}
func (s *Store) Torrents() *TorrentStorage {
	return s.torrents
}
func (s *Store) RcloneManager() *rclone.Manager {
	return s.rcloneManager
}

func (s *Store) Scheduler() gocron.Scheduler {
	return s.scheduler
}
