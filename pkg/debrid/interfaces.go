package debrid

import (
	"context"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

// Provider represents a debrid service provider
type Provider interface {
	TorrentManager
	DownloadManager
	AccountManager
	AvailabilityChecker
	
	// Core identification
	Name() string
	Logger() zerolog.Logger
}

// TorrentManager handles torrent operations
type TorrentManager interface {
	SubmitMagnet(tr *types.Torrent) (*types.Torrent, error)
	CheckStatus(tr *types.Torrent) (*types.Torrent, error)
	UpdateTorrent(torrent *types.Torrent) error
	GetTorrent(torrentId string) (*types.Torrent, error)
	GetTorrents() ([]*types.Torrent, error)
	DeleteTorrent(torrentId string) error
	GetDownloadingStatus() []string
	GetDownloadUncached() bool
	GetMountPath() string
}

// DownloadManager handles download links and file operations
type DownloadManager interface {
	GetFileDownloadLinks(tr *types.Torrent) error
	GetDownloadLink(tr *types.Torrent, file *types.File) (*types.DownloadLink, error)
	GetDownloadLinks() (map[string]*types.DownloadLink, error)
	DeleteDownloadLink(linkId string) error
	CheckLink(link string) error
}

// AccountManager handles account operations
type AccountManager interface {
	Accounts() *types.Accounts
	GetProfile() (*types.Profile, error)
	GetAvailableSlots() (int, error)
	SyncAccounts() error
}

// AvailabilityChecker checks if content is cached/available
type AvailabilityChecker interface {
	IsAvailable(infohashes []string) map[string]bool
}

// StorageManager handles debrid storage operations
type StorageManager interface {
	StartWorker(ctx context.Context) error
	Reset()
	
	// Provider management
	Provider(name string) Provider
	Providers() map[string]Provider
	
	// Legacy methods for backward compatibility
	Client(name string) types.Client
	Clients() map[string]types.Client
	Caches() map[string]*Cache // Note: Cache type needs to be properly defined
}

// Cache represents a debrid cache (placeholder for now)
type Cache interface {
	StartWorker(ctx context.Context) error
	Reset()
	GetConfig() CacheConfig
}

// CacheConfig represents cache configuration
type CacheConfig struct {
	Name string
	// Add other config fields as needed
}

// ProviderFactory creates debrid providers
type ProviderFactory interface {
	CreateProvider(providerType string, config ProviderConfig) (Provider, error)
	SupportedProviders() []string
}

// ProviderConfig represents provider configuration
type ProviderConfig struct {
	Name     string
	APIKey   string
	Settings map[string]interface{}
	// Add other common config fields
}