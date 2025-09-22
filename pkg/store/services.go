package store

import (
	"context"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/rclone"
	"github.com/sirrobot01/decypharr/pkg/repair"
)

// ServiceContainer holds all the core services
type ServiceContainer struct {
	arrService         *arr.Storage
	debridService      *debrid.Storage
	repairService      *repair.Repair
	torrentService     *TorrentStorage
	rcloneService      *rclone.Manager
	queueService       *ImportQueue
	scheduler          gocron.Scheduler
	downloadSemaphore  chan struct{}
	removeStalledAfter time.Duration
	refreshInterval    time.Duration
	skipPreCache       bool
	logger             zerolog.Logger
}

// ServiceConfig holds configuration for services
type ServiceConfig struct {
	RefreshInterval    time.Duration
	SkipPreCache       bool
	MaxDownloads       int
	RemoveStalledAfter time.Duration
	TorrentsFile       string
	Logger             zerolog.Logger
}

// NewServiceContainer creates a new service container with dependency injection
func NewServiceContainer(config ServiceConfig) (*ServiceContainer, error) {
	// Create scheduler
	scheduler, err := gocron.NewScheduler(
		gocron.WithLocation(time.Local),
		gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")),
	)
	if err != nil {
		scheduler, _ = gocron.NewScheduler(gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")))
	}

	// Create torrent storage
	torrentStorage := newTorrentStorage(config.TorrentsFile)

	// Create import queue
	importQueue := NewImportQueue(context.Background(), 1000)

	// Create download semaphore
	downloadSemaphore := make(chan struct{}, config.MaxDownloads)

	return &ServiceContainer{
		torrentService:     torrentStorage,
		queueService:       importQueue,
		scheduler:          scheduler,
		downloadSemaphore:  downloadSemaphore,
		removeStalledAfter: config.RemoveStalledAfter,
		refreshInterval:    config.RefreshInterval,
		skipPreCache:       config.SkipPreCache,
		logger:             config.Logger,
	}, nil
}

// InjectServices injects external services into the container
func (sc *ServiceContainer) InjectServices(
	arrService *arr.Storage,
	debridService *debrid.Storage,
	repairService *repair.Repair,
	rcloneService *rclone.Manager,
) {
	sc.arrService = arrService
	sc.debridService = debridService
	sc.repairService = repairService
	sc.rcloneService = rcloneService
}

// Implement Services interface
func (sc *ServiceContainer) Arr() *arr.Storage {
	return sc.arrService
}

func (sc *ServiceContainer) Debrid() *debrid.Storage {
	return sc.debridService
}

func (sc *ServiceContainer) Repair() *repair.Repair {
	return sc.repairService
}

func (sc *ServiceContainer) Torrents() *TorrentStorage {
	return sc.torrentService
}

func (sc *ServiceContainer) RcloneManager() *rclone.Manager {
	return sc.rcloneService
}

func (sc *ServiceContainer) Scheduler() gocron.Scheduler {
	return sc.scheduler
}

func (sc *ServiceContainer) StartWorkers(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Start debrid workers
	if sc.debridService != nil {
		if err := sc.debridService.StartWorker(ctx); err != nil {
			sc.logger.Error().Err(err).Msg("Failed to start debrid worker")
		} else {
			sc.logger.Debug().Msg("Started debrid worker")
		}

		// Cache workers
		for _, cache := range sc.debridService.Caches() {
			if cache == nil {
				continue
			}
			// Skip cache workers for now as the interface needs to be properly defined
			// This can be implemented once the debrid.Cache interface is properly abstracted
			sc.logger.Debug().Msgf("Skipping cache worker startup (needs interface refactoring)")
		}
	}

	// Store queue workers - temporarily disabled pending refactoring
	// TODO: Implement queue workers in the new architecture
	sc.logger.Debug().Msg("Queue workers startup skipped (pending refactoring)")

	// Arr workers
	if sc.arrService != nil {
		if err := sc.arrService.StartWorker(ctx); err != nil {
			sc.logger.Error().Err(err).Msg("Failed to start Arr worker")
		} else {
			sc.logger.Debug().Msg("Started Arr worker")
		}
	}
}

// Reset cleans up all services
func (sc *ServiceContainer) Reset() {
	if sc.debridService != nil {
		sc.debridService.Reset()
	}

	if sc.rcloneService != nil {
		sc.rcloneService.Stop()
	}

	if sc.queueService != nil {
		sc.queueService.Close()
	}

	if sc.downloadSemaphore != nil {
		close(sc.downloadSemaphore)
	}

	if sc.scheduler != nil {
		_ = sc.scheduler.StopJobs()
		_ = sc.scheduler.Shutdown()
	}
}

// GetDownloadSemaphore returns the download semaphore for backward compatibility
func (sc *ServiceContainer) GetDownloadSemaphore() chan struct{} {
	return sc.downloadSemaphore
}

// GetRefreshInterval returns the refresh interval for backward compatibility
func (sc *ServiceContainer) GetRefreshInterval() time.Duration {
	return sc.refreshInterval
}

// GetSkipPreCache returns the skip pre-cache setting for backward compatibility
func (sc *ServiceContainer) GetSkipPreCache() bool {
	return sc.skipPreCache
}

// GetRemoveStalledAfter returns the remove stalled after duration for backward compatibility
func (sc *ServiceContainer) GetRemoveStalledAfter() time.Duration {
	return sc.removeStalledAfter
}