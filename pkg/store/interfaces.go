package store

import (
	"context"
	"github.com/go-co-op/gocron/v2"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/rclone"
	"github.com/sirrobot01/decypharr/pkg/repair"
)

// Services defines the core services interface
type Services interface {
	Arr() ArrService
	Debrid() DebridService
	Repair() RepairService
	Torrents() TorrentService
	RcloneManager() RcloneService
	Scheduler() gocron.Scheduler
	StartWorkers(ctx context.Context)
}

// ArrService interface for Arr-related operations
type ArrService interface {
	StartWorker(ctx context.Context) error
	// Add other methods as needed
}

// DebridService interface for debrid-related operations
type DebridService interface {
	StartWorker(ctx context.Context) error
	// Note: Using interface{} temporarily for cache types to avoid circular imports
	Caches() map[string]interface{}
	Clients() map[string]interface{}
	// Add other methods as needed
}

// RepairService interface for repair operations
type RepairService interface {
	Start(ctx context.Context) error
	// Add other methods as needed
}

// TorrentService interface for torrent operations
type TorrentService interface {
	Reset()
	// Add other methods as needed based on TorrentStorage
}

// RcloneService interface for rclone operations
type RcloneService interface {
	Start(ctx context.Context) error
	Stop()
	IsReady() bool
	// Add other methods as needed
}

// QueueService interface for queue operations
type QueueService interface {
	StartQueueWorkers(ctx context.Context) error
	Close()
	// Add other methods as needed
}

// ServiceAdapter wraps the existing services to implement the interfaces
type ServiceAdapter struct {
	arr    *arr.Storage
	debrid *debrid.Storage
	repair *repair.Repair
}

func (sa *ServiceAdapter) StartWorker(ctx context.Context) error {
	if sa.arr != nil {
		return sa.arr.StartWorker(ctx)
	}
	return nil
}

// DebridServiceAdapter implements DebridService
type DebridServiceAdapter struct {
	storage *debrid.Storage
}

func (dsa *DebridServiceAdapter) StartWorker(ctx context.Context) error {
	if dsa.storage != nil {
		return dsa.storage.StartWorker(ctx)
	}
	return nil
}

func (dsa *DebridServiceAdapter) Caches() map[string]interface{} {
	if dsa.storage != nil {
		// Convert to interface{} to avoid type issues
		caches := dsa.storage.Caches()
		result := make(map[string]interface{})
		for k, v := range caches {
			result[k] = v
		}
		return result
	}
	return make(map[string]interface{})
}

func (dsa *DebridServiceAdapter) Clients() map[string]interface{} {
	if dsa.storage != nil {
		// Convert to interface{} to avoid type issues
		clients := dsa.storage.Clients()
		result := make(map[string]interface{})
		for k, v := range clients {
			result[k] = v
		}
		return result
	}
	return make(map[string]interface{})
}

// RepairServiceAdapter implements RepairService
type RepairServiceAdapter struct {
	repair *repair.Repair
}

func (rsa *RepairServiceAdapter) Start(ctx context.Context) error {
	if rsa.repair != nil {
		return rsa.repair.Start(ctx)
	}
	return nil
}

// TorrentServiceAdapter implements TorrentService
type TorrentServiceAdapter struct {
	storage *TorrentStorage
}

func (tsa *TorrentServiceAdapter) Reset() {
	if tsa.storage != nil {
		tsa.storage.Reset()
	}
}

// RcloneServiceAdapter implements RcloneService
type RcloneServiceAdapter struct {
	manager *rclone.Manager
}

func (rsa *RcloneServiceAdapter) Start(ctx context.Context) error {
	if rsa.manager != nil {
		return rsa.manager.Start(ctx)
	}
	return nil
}

func (rsa *RcloneServiceAdapter) Stop() {
	if rsa.manager != nil {
		rsa.manager.Stop()
	}
}

func (rsa *RcloneServiceAdapter) IsReady() bool {
	if rsa.manager != nil {
		return rsa.manager.IsReady()
	}
	return false
}