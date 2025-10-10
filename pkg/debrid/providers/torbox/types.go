package torbox

import (
	"sync"
	"time"

	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const (
	// ErrorCodeDatabaseError indicates the torrent has been deleted from Torbox's database
	ErrorCodeDatabaseError = "DATABASE_ERROR"
)

type APIResponse[T any] struct {
	Success bool   `json:"success"`
	Error   any    `json:"error"`
	Detail  string `json:"detail"`
	Data    *T     `json:"data"` // Use pointer to allow nil
}

// ParseErrorCode extracts the error code from the API response
// Torbox API can return errors as strings or objects with a code field
func (r *APIResponse[T]) ParseErrorCode() string {
	if r.Error == nil {
		return ""
	}

	// Error can be a string (error code directly) or an object
	switch v := r.Error.(type) {
	case string:
		return v
	case map[string]interface{}:
		if code, ok := v["code"].(string); ok {
			return code
		}
		if msg, ok := v["message"].(string); ok {
			// Sometimes error code is in the message field
			return msg
		}
	}
	return ""
}

type AvailableResponse APIResponse[map[string]struct {
	Name string `json:"name"`
	Size int    `json:"size"`
	Hash string `json:"hash"`
}]

type AddMagnetResponse APIResponse[struct {
	Id   int    `json:"torrent_id"`
	Hash string `json:"hash"`
}]

// TorboxFile represents a file within a torrent
type TorboxFile struct {
	Id           int         `json:"id"`
	Md5          interface{} `json:"md5"`
	Hash         string      `json:"hash"`
	Name         string      `json:"name"`
	Size         int64       `json:"size"`
	Zipped       bool        `json:"zipped"`
	S3Path       string      `json:"s3_path"`
	Infected     bool        `json:"infected"`
	Mimetype     string      `json:"mimetype"`
	ShortName    string      `json:"short_name"`
	AbsolutePath string      `json:"absolute_path"`
}

type torboxInfo struct {
	Id              int         `json:"id"`
	AuthId          string      `json:"auth_id"`
	Server          int         `json:"server"`
	Hash            string      `json:"hash"`
	Name            string      `json:"name"`
	Magnet          interface{} `json:"magnet"`
	Size            int64       `json:"size"`
	Active          bool        `json:"active"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	DownloadState   string      `json:"download_state"`
	Seeds           int         `json:"seeds"`
	Peers           int         `json:"peers"`
	Ratio           float64     `json:"ratio"`
	Progress        float64     `json:"progress"`
	DownloadSpeed   int64       `json:"download_speed"`
	UploadSpeed     int         `json:"upload_speed"`
	Eta             int         `json:"eta"`
	TorrentFile     bool        `json:"torrent_file"`
	ExpiresAt       interface{} `json:"expires_at"`
	DownloadPresent bool         `json:"download_present"`
	Files           []TorboxFile `json:"files"`
	DownloadPath    string       `json:"download_path"`
	InactiveCheck    int         `json:"inactive_check"`
	Availability     float64     `json:"availability"`
	DownloadFinished bool        `json:"download_finished"`
	Tracker          interface{} `json:"tracker"`
	TotalUploaded    int         `json:"total_uploaded"`
	TotalDownloaded  int         `json:"total_downloaded"`
	Cached           bool        `json:"cached"`
	Owner            string      `json:"owner"`
	SeedTorrent      bool        `json:"seed_torrent"`
	AllowZipped      bool        `json:"allow_zipped"`
	LongTermSeeding  bool        `json:"long_term_seeding"`
	TrackerMessage   interface{} `json:"tracker_message"`
}

type InfoResponse APIResponse[torboxInfo]

type DownloadLinksResponse APIResponse[string]

type TorrentsListResponse APIResponse[[]torboxInfo]

// UserMeData represents the user information returned by /api/user/me
// Updated to match actual Torbox API response structure
type UserMeData struct {
	Id                          int        `json:"id"`
	AuthId                      string     `json:"auth_id"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
	Plan                        int        `json:"plan"`
	TotalDownloaded             int        `json:"total_downloaded"`
	Customer                    string     `json:"customer"`
	IsSubscribed                bool       `json:"is_subscribed"`
	PremiumExpiresAt            time.Time  `json:"premium_expires_at"`
	CooldownUntil               *time.Time `json:"cooldown_until"` // Nullable
	Email                       string     `json:"email"`
	UserReferral                string     `json:"user_referral"`
	BaseEmail                   string     `json:"base_email"`
	TotalBytesDownloaded        int64      `json:"total_bytes_downloaded"`
	TotalBytesUploaded          int64      `json:"total_bytes_uploaded"`
	TorrentsDownloaded          int        `json:"torrents_downloaded"`
	WebDownloadsDownloaded      int        `json:"web_downloads_downloaded"`
	UsenetDownloadsDownloaded   int        `json:"usenet_downloads_downloaded"`
	AdditionalConcurrentSlots   int        `json:"additional_concurrent_slots"`
	LongTermSeeding             bool       `json:"long_term_seeding"`
	LongTermStorage             bool       `json:"long_term_storage"`
	IsVendor                    bool       `json:"is_vendor"`
	VendorId                    *string    `json:"vendor_id"` // Nullable
	PurchasesReferred           int        `json:"purchases_referred"`
}

type UserMeResponse APIResponse[UserMeData]

// userMeCache holds cached user data with expiration
type userMeCache struct {
	data      *UserMeData
	expiresAt time.Time
	mu        sync.RWMutex
}

// torrentsListCache holds cached torrents list with expiration
// Implements double-checked locking with separate fetch mutex to prevent thundering herd
//
// LOCK ORDERING (to prevent deadlocks):
// 1. fetchMu - Used for coordinating fetch operations (held during API calls)
// 2. mu - Single lock protecting all cache data (data, convertedData, dataMap, expiresAt)
//
// The simplified single-lock approach eliminates potential deadlock scenarios
// that could occur with multiple locks. While this may increase lock contention,
// it ensures correctness and is acceptable for this use case since:
// - Cache hits are fast (read lock only)
// - Cache misses are infrequent (45-second TTL)
// - The fetchMu prevents multiple goroutines from fetching simultaneously
type torrentsListCache struct {
	// Single mutex protecting all cache data
	mu sync.RWMutex

	// Raw torboxInfo data from API
	data      []*torboxInfo
	expiresAt time.Time

	// Converted torrents cache to avoid repeated allocations
	convertedData []*types.Torrent

	// Map for O(1) lookup of torboxInfo by ID
	dataMap map[string]*torboxInfo

	// Separate mutex for fetch operations to prevent thundering herd
	// This ensures only one goroutine fetches when cache expires
	fetchMu sync.Mutex
}
