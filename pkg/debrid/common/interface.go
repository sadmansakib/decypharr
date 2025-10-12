package common

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

type Client interface {
	SubmitMagnet(ctx context.Context, tr *types.Torrent) (*types.Torrent, error)
	CheckStatus(ctx context.Context, tr *types.Torrent) (*types.Torrent, error)
	GetFileDownloadLinks(ctx context.Context, tr *types.Torrent) error
	GetDownloadLink(ctx context.Context, tr *types.Torrent, file *types.File) (types.DownloadLink, error)
	DeleteTorrent(ctx context.Context, torrentId string) error
	IsAvailable(ctx context.Context, infohashes []string) map[string]bool
	GetDownloadUncached() bool
	UpdateTorrent(ctx context.Context, torrent *types.Torrent) error
	GetTorrent(ctx context.Context, torrentId string) (*types.Torrent, error)
	GetTorrents(ctx context.Context) ([]*types.Torrent, error)
	Name() string
	Logger() zerolog.Logger
	GetDownloadingStatus() []string
	RefreshDownloadLinks(ctx context.Context) error
	CheckLink(ctx context.Context, link string) error
	GetMountPath() string
	AccountManager() *account.Manager // Returns the active download account/token
	GetProfile(ctx context.Context) (*types.Profile, error)
	GetAvailableSlots(ctx context.Context) (int, error)
	SyncAccounts(ctx context.Context) error // Updates each accounts details(like traffic, username, etc.)
}
