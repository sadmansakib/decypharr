package config

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RepairStrategy string

const (
	RepairStrategyPerFile    RepairStrategy = "per_file"
	RepairStrategyPerTorrent RepairStrategy = "per_torrent"
)

var (
	instance   *Config
	once       sync.Once
	configPath string
)

type Debrid struct {
	Name              string   `json:"name,omitempty"`
	APIKey            string   `json:"api_key,omitempty"`
	DownloadAPIKeys   []string `json:"download_api_keys,omitempty"`
	Folder            string   `json:"folder,omitempty"`
	RcloneMountPath   string   `json:"rclone_mount_path,omitempty"` // Custom rclone mount path for this debrid service
	DownloadUncached  bool     `json:"download_uncached,omitempty"`
	CheckCached       bool     `json:"check_cached,omitempty"`
	RateLimit         string   `json:"rate_limit,omitempty"` // 200/minute or 10/second
	RepairRateLimit   string   `json:"repair_rate_limit,omitempty"`
	DownloadRateLimit string   `json:"download_rate_limit,omitempty"`
	Proxy             string   `json:"proxy,omitempty"`
	UnpackRar         bool     `json:"unpack_rar,omitempty"`
	AddSamples        bool     `json:"add_samples,omitempty"`
	MinimumFreeSlot   int      `json:"minimum_free_slot,omitempty"` // Minimum active pots to use this debrid
	Limit             int      `json:"limit,omitempty"`             // Maximum number of total torrents

	UseWebDav bool `json:"use_webdav,omitempty"`
	WebDav
}

type QBitTorrent struct {
	Username        string   `json:"username,omitempty"`
	Password        string   `json:"password,omitempty"`
	Port            string   `json:"port,omitempty"` // deprecated
	DownloadFolder  string   `json:"download_folder,omitempty"`
	Categories      []string `json:"categories,omitempty"`
	RefreshInterval int      `json:"refresh_interval,omitempty"`
	SkipPreCache    bool     `json:"skip_pre_cache,omitempty"`
	MaxDownloads    int      `json:"max_downloads,omitempty"`
}

type Arr struct {
	Name             string `json:"name,omitempty"`
	Host             string `json:"host,omitempty"`
	Token            string `json:"token,omitempty"`
	Cleanup          bool   `json:"cleanup,omitempty"`
	SkipRepair       bool   `json:"skip_repair,omitempty"`
	DownloadUncached *bool  `json:"download_uncached,omitempty"`
	SelectedDebrid   string `json:"selected_debrid,omitempty"`
	Source           string `json:"source,omitempty"` // The source of the arr, e.g. "auto", "config", "". Auto means it was automatically detected from the arr
}

type Repair struct {
	Enabled     bool           `json:"enabled,omitempty"`
	Interval    string         `json:"interval,omitempty"`
	ZurgURL     string         `json:"zurg_url,omitempty"`
	AutoProcess bool           `json:"auto_process,omitempty"`
	UseWebDav   bool           `json:"use_webdav,omitempty"`
	Workers     int            `json:"workers,omitempty"`
	ReInsert    bool           `json:"reinsert,omitempty"`
	Strategy    RepairStrategy `json:"strategy,omitempty"`
}

type Auth struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	APIToken string `json:"api_token,omitempty"`
}

type Rclone struct {
	// Global mount folder where all providers will be mounted as subfolders
	Enabled   bool   `json:"enabled,omitempty"`
	MountPath string `json:"mount_path,omitempty"`

	// Cache settings
	CacheDir string `json:"cache_dir,omitempty"`

	// VFS settings
	VfsCacheMode          string `json:"vfs_cache_mode,omitempty"`            // off, minimal, writes, full
	VfsCacheMaxAge        string `json:"vfs_cache_max_age,omitempty"`         // Maximum age of objects in the cache (default 1h)
	VfsCacheMaxSize       string `json:"vfs_cache_max_size,omitempty"`        // Maximum size of the cache (default off)
	VfsCachePollInterval  string `json:"vfs_cache_poll_interval,omitempty"`   // How often to poll for changes (default 1m)
	VfsReadChunkSize      string `json:"vfs_read_chunk_size,omitempty"`       // Read chunk size (default 128M)
	VfsReadChunkSizeLimit string `json:"vfs_read_chunk_size_limit,omitempty"` // Max chunk size (default off)
	VfsReadAhead          string `json:"vfs_read_ahead,omitempty"`            // read ahead size
	VfsPollInterval       string `json:"vfs_poll_interval,omitempty"`         // How often to rclone cleans the cache (default 1m)
	BufferSize            string `json:"buffer_size,omitempty"`               // Buffer size for reading files (default 16M)

	// File system settings
	UID   uint32 `json:"uid,omitempty"` // User ID for mounted files
	GID   uint32 `json:"gid,omitempty"` // Group ID for mounted files
	Umask string `json:"umask,omitempty"`

	// Timeout settings
	AttrTimeout  string `json:"attr_timeout,omitempty"`   // Attribute cache timeout (default 1s)
	DirCacheTime string `json:"dir_cache_time,omitempty"` // Directory cache time (default 5m)

	// Performance settings
	NoModTime  bool `json:"no_modtime,omitempty"`  // Don't read/write modification time
	NoChecksum bool `json:"no_checksum,omitempty"` // Don't checksum files on upload

	LogLevel string `json:"log_level,omitempty"`
}

type Config struct {
	// server
	BindAddress string `json:"bind_address,omitempty"`
	URLBase     string `json:"url_base,omitempty"`
	Port        string `json:"port,omitempty"`

	LogLevel           string      `json:"log_level,omitempty"`
	Debrids            []Debrid    `json:"debrids,omitempty"`
	QBitTorrent        QBitTorrent `json:"qbittorrent,omitempty"`
	Arrs               []Arr       `json:"arrs,omitempty"`
	Repair             Repair      `json:"repair,omitempty"`
	WebDav             WebDav      `json:"webdav,omitempty"`
	Rclone             Rclone      `json:"rclone,omitempty"`
	AllowedExt         []string    `json:"allowed_file_types,omitempty"`
	MinFileSize        string      `json:"min_file_size,omitempty"` // Minimum file size to download, 10MB, 1GB, etc
	MaxFileSize        string      `json:"max_file_size,omitempty"` // Maximum file size to download (0 means no limit)
	Path               string      `json:"-"`                       // Path to save the config file
	UseAuth            bool        `json:"use_auth,omitempty"`
	Auth               *Auth       `json:"-"`
	DiscordWebhook     string      `json:"discord_webhook_url,omitempty"`
	RemoveStalledAfter string      `json:"remove_stalled_after,omitzero"`
}

func (c *Config) JsonFile() string {
	return filepath.Join(c.Path, "config.json")
}
func (c *Config) AuthFile() string {
	return filepath.Join(c.Path, "auth.json")
}

func (c *Config) TorrentsFile() string {
	return filepath.Join(c.Path, "torrents.json")
}

func (c *Config) loadConfig() error {
	// Load the config file
	if configPath == "" {
		return fmt.Errorf("config path not set")
	}
	c.Path = configPath
	file, err := os.ReadFile(c.JsonFile())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Config file not found, creating a new one at %s\n", c.JsonFile())
			// Create a default config file if it doesn't exist
			if err := c.createConfig(c.Path); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}
			return c.Save()
		}
		return fmt.Errorf("error reading config file: %w", err)
	}

	if err := json.Unmarshal(file, &c); err != nil {
		return fmt.Errorf("error unmarshaling config: %w", err)
	}
	c.setDefaults()
	return nil
}

func validateDebrids(debrids []Debrid) error {
	if len(debrids) == 0 {
		return errors.New("no debrids configured")
	}

	for _, debrid := range debrids {
		// Basic field validation
		if debrid.APIKey == "" {
			return errors.New("debrid api key is required")
		}
		if debrid.Folder == "" {
			return errors.New("debrid folder is required")
		}
	}

	return nil
}

func validateQbitTorrent(config *QBitTorrent) error {
	if config.DownloadFolder == "" {
		return errors.New("qbittorent download folder is required")
	}
	if _, err := os.Stat(config.DownloadFolder); os.IsNotExist(err) {
		return fmt.Errorf("qbittorent download folder(%s) does not exist", config.DownloadFolder)
	}
	return nil
}

func validateRepair(config *Repair) error {
	if !config.Enabled {
		return nil
	}
	if config.Interval == "" {
		return errors.New("repair interval is required")
	}
	return nil
}

// parseRateLimit parses rate limit strings like "5/second", "200/minute", "60/hour"
// Returns requests per second for comparison with API limits
// Supports formats: "number/unit" where unit is second, minute, or hour
// Examples: "5/second" -> 5.0, "120/minute" -> 2.0, "3600/hour" -> 1.0
func parseRateLimit(rateLimit string) (float64, error) {
	if rateLimit == "" {
		return 0, nil
	}

	// Match patterns like "5/second", "200/minute", "60/hour" with optional spaces
	re := regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*/\s*(second|minute|hour)\s*$`)
	matches := re.FindStringSubmatch(strings.ToLower(rateLimit))
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid rate limit format: %s (expected format: 'number/unit' where unit is second, minute, or hour)", rateLimit)
	}

	rate, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate number in rate limit: %s", rateLimit)
	}

	// Convert to requests per second
	switch matches[2] {
	case "second":
		return rate, nil
	case "minute":
		return rate / 60.0, nil
	case "hour":
		return rate / 3600.0, nil
	default:
		return 0, fmt.Errorf("unsupported time unit: %s", matches[2])
	}
}

// validateDuration validates and corrects duration fields against reasonable limits
func validateDuration(duration, fieldName, defaultValue string, minDur, maxDur time.Duration) (string, bool) {
	if duration == "" {
		return defaultValue, false
	}

	parsedDur, err := time.ParseDuration(duration)
	if err != nil {
		log.Printf("[WARNING] [Torbox] Invalid %s duration format '%s': %v. Using default '%s'",
			fieldName, duration, err, defaultValue)
		return defaultValue, true
	}

	if parsedDur < minDur {
		log.Printf("[WARNING] [Torbox] %s duration '%s' is too short (min: %s). Using safe value '%s'",
			fieldName, duration, minDur, defaultValue)
		return defaultValue, true
	}

	if parsedDur > maxDur {
		log.Printf("[WARNING] [Torbox] %s duration '%s' is too long (max: %s). Using safe value '%s'",
			fieldName, duration, maxDur, defaultValue)
		return defaultValue, true
	}

	return duration, false
}

// validateResourceLimit validates and corrects resource limit fields
func validateResourceLimit(value int, fieldName string, minValue, maxValue, defaultValue int) (int, bool) {
	if value == 0 {
		return defaultValue, false
	}

	if value < minValue {
		log.Printf("[WARNING] [Torbox] %s value %d is too low (min: %d). Using safe value %d",
			fieldName, value, minValue, defaultValue)
		return defaultValue, true
	}

	if value > maxValue {
		log.Printf("[WARNING] [Torbox] %s value %d is too high (max: %d). Using safe value %d",
			fieldName, value, maxValue, defaultValue)
		return defaultValue, true
	}

	return value, false
}

// validateTorboxConfiguration validates and corrects Torbox-specific configuration against API constraints
// Validates:
// - Rate limits against Torbox API maximums
// - Duration fields for reasonable intervals
// - Resource limits for optimal performance
// - Network configuration parameters
// Returns corrected Debrid configuration with safe defaults
func validateTorboxConfiguration(debrid Debrid) Debrid {
	// Torbox API rate limits (converted to requests per second)
	const (
		maxGeneralAPIRate    = 5.0           // 5/sec per IP
		maxCreateTorrentHour = 60.0 / 3600.0 // 60/hour = 0.0167/sec
		maxCreateTorrentMin  = 10.0 / 60.0   // 10/min = 0.167/sec
		maxCreateUsenetHour  = 60.0 / 3600.0 // 60/hour = 0.0167/sec
		maxCreateUsenetMin   = 10.0 / 60.0   // 10/min = 0.167/sec
		maxCreateWebdlHour   = 60.0 / 3600.0 // 60/hour = 0.0167/sec
		maxCreateWebdlMin    = 10.0 / 60.0   // 10/min = 0.167/sec
	)

	// Safe default rate limits that respect Torbox API constraints
	const (
		safeGeneralRate = "4/second" // Conservative general API rate
		safeCreateRate  = "8/hour"   // Conservative create endpoint rate
	)

	corrected := debrid // Copy the debrid config

	// Validate and correct general rate limit
	if corrected.RateLimit != "" {
		rate, err := parseRateLimit(corrected.RateLimit)
		if err != nil {
			log.Printf("[WARNING] [Torbox:%s] Invalid rate limit format '%s': %v. Using safe default '%s'",
				corrected.Name, corrected.RateLimit, err, safeGeneralRate)
			corrected.RateLimit = safeGeneralRate
		} else if rate > maxGeneralAPIRate {
			log.Printf("[INFO] [Torbox:%s] Rate limit '%s' (%.3f/sec) exceeds API maximum of 5/second. Automatically corrected to '%s'",
				corrected.Name, corrected.RateLimit, rate, safeGeneralRate)
			corrected.RateLimit = safeGeneralRate
		}
	}

	// Validate and correct repair rate limit (typically uses general API)
	if corrected.RepairRateLimit != "" {
		rate, err := parseRateLimit(corrected.RepairRateLimit)
		if err != nil {
			log.Printf("[WARNING] [Torbox:%s] Invalid repair rate limit format '%s': %v. Using safe default '%s'",
				corrected.Name, corrected.RepairRateLimit, err, safeGeneralRate)
			corrected.RepairRateLimit = safeGeneralRate
		} else if rate > maxGeneralAPIRate {
			log.Printf("[INFO] [Torbox:%s] Repair rate limit '%s' (%.3f/sec) exceeds API maximum of 5/second. Automatically corrected to '%s'",
				corrected.Name, corrected.RepairRateLimit, rate, safeGeneralRate)
			corrected.RepairRateLimit = safeGeneralRate
		}
	}

	// Validate and correct download rate limit (typically uses general API)
	if corrected.DownloadRateLimit != "" {
		rate, err := parseRateLimit(corrected.DownloadRateLimit)
		if err != nil {
			log.Printf("[WARNING] [Torbox:%s] Invalid download rate limit format '%s': %v. Using safe default '%s'",
				corrected.Name, corrected.DownloadRateLimit, err, safeGeneralRate)
			corrected.DownloadRateLimit = safeGeneralRate
		} else if rate > maxGeneralAPIRate {
			log.Printf("[INFO] [Torbox:%s] Download rate limit '%s' (%.3f/sec) exceeds API maximum of 5/second. Automatically corrected to '%s'",
				corrected.Name, corrected.DownloadRateLimit, rate, safeGeneralRate)
			corrected.DownloadRateLimit = safeGeneralRate
		}
	}

	// Validate duration fields for WebDAV configuration
	var durationCorrected bool
	corrected.TorrentsRefreshInterval, durationCorrected = validateDuration(
		corrected.TorrentsRefreshInterval, "TorrentsRefreshInterval", "45s",
		10*time.Second, 300*time.Second)
	if durationCorrected {
		log.Printf("[INFO] [Torbox:%s] TorrentsRefreshInterval corrected to %s for optimal rate limiting", corrected.Name, corrected.TorrentsRefreshInterval)
	}

	corrected.DownloadLinksRefreshInterval, durationCorrected = validateDuration(
		corrected.DownloadLinksRefreshInterval, "DownloadLinksRefreshInterval", "40m",
		5*time.Minute, 120*time.Minute)
	if durationCorrected {
		log.Printf("[INFO] [Torbox:%s] DownloadLinksRefreshInterval corrected to %s for optimal rate limiting", corrected.Name, corrected.DownloadLinksRefreshInterval)
	}

	corrected.AutoExpireLinksAfter, durationCorrected = validateDuration(
		corrected.AutoExpireLinksAfter, "AutoExpireLinksAfter", "3d",
		1*time.Hour, 30*24*time.Hour)
	if durationCorrected {
		log.Printf("[INFO] [Torbox:%s] AutoExpireLinksAfter corrected to %s for optimal performance", corrected.Name, corrected.AutoExpireLinksAfter)
	}

	// Validate resource limits
	var resourceCorrected bool
	corrected.Workers, resourceCorrected = validateResourceLimit(
		corrected.Workers, "Workers", 1, 200, 50)
	if resourceCorrected {
		log.Printf("[INFO] [Torbox:%s] Workers corrected to %d to prevent rate limit violations", corrected.Name, corrected.Workers)
	}

	corrected.MinimumFreeSlot, resourceCorrected = validateResourceLimit(
		corrected.MinimumFreeSlot, "MinimumFreeSlot", 1, 100, 10)
	if resourceCorrected {
		log.Printf("[INFO] [Torbox:%s] MinimumFreeSlot corrected to %d for optimal resource management", corrected.Name, corrected.MinimumFreeSlot)
	}

	corrected.Limit, resourceCorrected = validateResourceLimit(
		corrected.Limit, "Limit", 10, 1000, 100)
	if resourceCorrected {
		log.Printf("[INFO] [Torbox:%s] Limit corrected to %d for optimal torrent management", corrected.Name, corrected.Limit)
	}

	// Log information about Torbox-specific endpoint limits
	log.Printf("[INFO] [Torbox:%s] Configuration validated - API limits: General 5/sec, Create endpoints 60/hour and 10/min", corrected.Name)

	return corrected
}

func ValidateConfig(config *Config) error {
	// Run validations concurrently

	if err := validateDebrids(config.Debrids); err != nil {
		return err
	}

	if err := validateQbitTorrent(&config.QBitTorrent); err != nil {
		return err
	}

	if err := validateRepair(&config.Repair); err != nil {
		return err
	}

	// Validate and correct Torbox configuration
	for i, debrid := range config.Debrids {
		if strings.ToLower(debrid.Name) == "torbox" {
			config.Debrids[i] = validateTorboxConfiguration(debrid)
		}
	}

	return nil
}

// generateAPIToken creates a new random API token
func generateAPIToken() (string, error) {
	bytes := make([]byte, 32) // 256-bit token
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func SetConfigPath(path string) {
	configPath = path
}

func Get() *Config {
	once.Do(func() {
		instance = &Config{} // Initialize instance first
		if err := instance.loadConfig(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "configuration Error: %v\n", err)
			os.Exit(1)
		}
	})
	return instance
}

func (c *Config) GetMinFileSize() int64 {
	// 0 means no limit
	if c.MinFileSize == "" {
		return 0
	}
	s, err := ParseSize(c.MinFileSize)
	if err != nil {
		return 0
	}
	return s
}

func (c *Config) GetMaxFileSize() int64 {
	// 0 means no limit
	if c.MaxFileSize == "" {
		return 0
	}
	s, err := ParseSize(c.MaxFileSize)
	if err != nil {
		return 0
	}
	return s
}

func (c *Config) IsSizeAllowed(size int64) bool {
	if size == 0 {
		return true // Maybe the debrid hasn't reported the size yet
	}
	if c.GetMinFileSize() > 0 && size < c.GetMinFileSize() {
		return false
	}
	if c.GetMaxFileSize() > 0 && size > c.GetMaxFileSize() {
		return false
	}
	return true
}

func (c *Config) GetAuth() *Auth {
	if !c.UseAuth {
		return nil
	}
	if c.Auth == nil {
		c.Auth = &Auth{}
		if _, err := os.Stat(c.AuthFile()); err == nil {
			file, err := os.ReadFile(c.AuthFile())
			if err == nil {
				_ = json.Unmarshal(file, c.Auth)
			}
		}
	}
	return c.Auth
}

func (c *Config) SaveAuth(auth *Auth) error {
	c.Auth = auth
	data, err := json.Marshal(auth)
	if err != nil {
		return err
	}
	return os.WriteFile(c.AuthFile(), data, 0644)
}

func (c *Config) NeedsSetup() error {
	return ValidateConfig(c)
}

func (c *Config) NeedsAuth() bool {
	if c.UseAuth {
		return c.GetAuth().Username == ""
	}
	return false
}

func (c *Config) updateDebrid(d Debrid) Debrid {
	workers := runtime.NumCPU() * 50
	perDebrid := workers / len(c.Debrids)

	var downloadKeys []string

	if len(d.DownloadAPIKeys) > 0 {
		downloadKeys = d.DownloadAPIKeys
	} else {
		// If no download API keys are specified, use the main API key
		downloadKeys = []string{d.APIKey}
	}
	d.DownloadAPIKeys = downloadKeys

	if !d.UseWebDav {
		return d
	}

	if d.TorrentsRefreshInterval == "" {
		d.TorrentsRefreshInterval = cmp.Or(c.WebDav.TorrentsRefreshInterval, "45s") // 45 seconds
	}
	if d.WebDav.DownloadLinksRefreshInterval == "" {
		d.DownloadLinksRefreshInterval = cmp.Or(c.WebDav.DownloadLinksRefreshInterval, "40m") // 40 minutes
	}
	if d.Workers == 0 {
		d.Workers = perDebrid
	}
	if d.FolderNaming == "" {
		d.FolderNaming = cmp.Or(c.WebDav.FolderNaming, "original_no_ext")
	}
	if d.AutoExpireLinksAfter == "" {
		d.AutoExpireLinksAfter = cmp.Or(c.WebDav.AutoExpireLinksAfter, "3d") // 2 days
	}

	// Merge debrid specified directories with global directories

	directories := c.WebDav.Directories
	if directories == nil {
		directories = make(map[string]WebdavDirectories)
	}

	for name, dir := range d.Directories {
		directories[name] = dir
	}
	d.Directories = directories

	d.RcUrl = cmp.Or(d.RcUrl, c.WebDav.RcUrl)
	d.RcUser = cmp.Or(d.RcUser, c.WebDav.RcUser)
	d.RcPass = cmp.Or(d.RcPass, c.WebDav.RcPass)

	return d
}

func (c *Config) setDefaults() {
	for i, debrid := range c.Debrids {
		c.Debrids[i] = c.updateDebrid(debrid)
	}

	if len(c.AllowedExt) == 0 {
		c.AllowedExt = getDefaultExtensions()
	}

	c.Port = cmp.Or(c.Port, c.QBitTorrent.Port)

	if c.URLBase == "" {
		c.URLBase = "/"
	}
	// validate url base starts with /
	if !strings.HasPrefix(c.URLBase, "/") {
		c.URLBase = "/" + c.URLBase
	}
	if !strings.HasSuffix(c.URLBase, "/") {
		c.URLBase += "/"
	}

	// Set repair defaults
	if c.Repair.Strategy == "" {
		c.Repair.Strategy = RepairStrategyPerTorrent
	}

	// Rclone defaults
	if c.Rclone.Enabled {
		c.Rclone.VfsCacheMode = cmp.Or(c.Rclone.VfsCacheMode, "off")
		if c.Rclone.UID == 0 {
			c.Rclone.UID = uint32(os.Getuid())
		}
		if c.Rclone.GID == 0 {
			if runtime.GOOS == "windows" {
				// On Windows, we use the current user's SID as GID
				c.Rclone.GID = uint32(os.Getuid()) // Windows does not have GID, using UID instead
			} else {
				c.Rclone.GID = uint32(os.Getgid())
			}
		}
		if c.Rclone.VfsCacheMode != "off" {
			c.Rclone.VfsCachePollInterval = cmp.Or(c.Rclone.VfsCachePollInterval, "1m") // Clean cache every minute
		}
		c.Rclone.DirCacheTime = cmp.Or(c.Rclone.DirCacheTime, "5m")
		c.Rclone.LogLevel = cmp.Or(c.Rclone.LogLevel, "INFO")
	}
	// Load the auth file
	c.Auth = c.GetAuth()

	// Generate API token if auth is enabled and no token exists
	if c.UseAuth {
		if c.Auth == nil {
			c.Auth = &Auth{}
		}
		if c.Auth.APIToken == "" {
			if token, err := generateAPIToken(); err == nil {
				c.Auth.APIToken = token
				// Save the updated auth config
				_ = c.SaveAuth(c.Auth)
			}
		}
	}
}

func (c *Config) Save() error {

	c.setDefaults()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(c.JsonFile(), data, 0644); err != nil {
		return err
	}
	return nil
}

func (c *Config) createConfig(path string) error {
	// Create the directory if it doesn't exist
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	c.Path = path
	c.URLBase = "/"
	c.Port = "8282"
	c.LogLevel = "info"
	c.UseAuth = true
	c.QBitTorrent = QBitTorrent{
		DownloadFolder:  filepath.Join(path, "downloads"),
		Categories:      []string{"sonarr", "radarr"},
		RefreshInterval: 15,
	}
	return nil
}

// Reload forces a reload of the configuration from disk
func Reload() {
	instance = nil
	once = sync.Once{}
}

func DefaultFreeSlot() int {
	return 10
}
