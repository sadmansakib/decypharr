package types

import (
	"github.com/rs/zerolog"
)

// Provider represents the new focused interface for debrid providers
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
	SubmitMagnet(tr *Torrent) (*Torrent, error)
	CheckStatus(tr *Torrent) (*Torrent, error)
	UpdateTorrent(torrent *Torrent) error
	GetTorrent(torrentId string) (*Torrent, error)
	GetTorrents() ([]*Torrent, error)
	DeleteTorrent(torrentId string) error
	GetDownloadingStatus() []string
	GetDownloadUncached() bool
	GetMountPath() string
}

// DownloadManager handles download links and file operations
type DownloadManager interface {
	GetFileDownloadLinks(tr *Torrent) error
	GetDownloadLink(tr *Torrent, file *File) (*DownloadLink, error)
	GetDownloadLinks() (map[string]*DownloadLink, error)
	DeleteDownloadLink(linkId string) error
	CheckLink(link string) error
}

// AccountManager handles account operations
type AccountManager interface {
	Accounts() *Accounts
	GetProfile() (*Profile, error)
	GetAvailableSlots() (int, error)
	SyncAccounts() error
}

// AvailabilityChecker checks if content is cached/available
type AvailabilityChecker interface {
	IsAvailable(infohashes []string) map[string]bool
}

// ClientAdapter adapts the new Provider interface to the legacy Client interface
type ClientAdapter struct {
	Provider
}

// Ensure ClientAdapter implements Client interface
var _ Client = (*ClientAdapter)(nil)

// NewClientAdapter creates a new adapter that implements the legacy Client interface
func NewClientAdapter(provider Provider) Client {
	return &ClientAdapter{Provider: provider}
}