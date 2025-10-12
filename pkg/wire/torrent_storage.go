package wire

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

func keyPair(hash, category string) string {
	return fmt.Sprintf("%s|%s", hash, category)
}

type Torrents = map[string]*Torrent

type TorrentStorage struct {
	torrents Torrents
	mu       sync.RWMutex
	filename string // Added to store the filename for persistence
	logger   zerolog.Logger
}

func loadTorrentsFromJSON(filename string) (Torrents, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	torrents := make(Torrents)
	if err := json.Unmarshal(data, &torrents); err != nil {
		return nil, err
	}
	return torrents, nil
}

func newTorrentStorage(filename string, logger zerolog.Logger) *TorrentStorage {
	// Open the JSON file and read the data
	torrents, err := loadTorrentsFromJSON(filename)
	if err != nil {
		torrents = make(Torrents)
	}
	// Create a new Storage
	return &TorrentStorage{
		torrents: torrents,
		filename: filename,
		logger:   logger,
	}
}

func (ts *TorrentStorage) Add(ctx context.Context, torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent
	go func(ctx context.Context) {
		// Check if context is cancelled before expensive operation
		select {
		case <-ctx.Done():
			ts.logger.Debug().
				Str("operation", "Add").
				Str("hash", torrent.Hash).
				Msg("Save operation cancelled")
			return
		default:
		}

		err := ts.saveToFile()
		if err != nil {
			ts.logger.Error().
				Err(err).
				Str("operation", "Add").
				Str("hash", torrent.Hash).
				Str("category", torrent.Category).
				Msg("Failed to save torrent storage")
		}
	}(ctx)
}

func (ts *TorrentStorage) AddOrUpdate(ctx context.Context, torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent
	go func(ctx context.Context) {
		// Check if context is cancelled before expensive operation
		select {
		case <-ctx.Done():
			ts.logger.Debug().
				Str("operation", "AddOrUpdate").
				Str("hash", torrent.Hash).
				Msg("Save operation cancelled")
			return
		default:
		}

		err := ts.saveToFile()
		if err != nil {
			ts.logger.Error().
				Err(err).
				Str("operation", "AddOrUpdate").
				Str("hash", torrent.Hash).
				Str("category", torrent.Category).
				Msg("Failed to save torrent storage")
		}
	}(ctx)
}

func (ts *TorrentStorage) Get(hash, category string) *Torrent {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	torrent, exists := ts.torrents[keyPair(hash, category)]
	if !exists && category == "" {
		// Try to find the torrent without knowing the category
		for _, t := range ts.torrents {
			if t.Hash == hash {
				return t
			}
		}
	}
	return torrent
}

func (ts *TorrentStorage) GetAll(category string, filter string, hashes []string) []*Torrent {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	torrents := make([]*Torrent, 0, len(ts.torrents))
	for _, torrent := range ts.torrents {
		if category != "" && torrent.Category != category {
			continue
		}
		if filter != "" && torrent.State != filter {
			continue
		}
		torrents = append(torrents, torrent)
	}

	if len(hashes) > 0 {
		filtered := make([]*Torrent, 0, len(hashes))
		for _, hash := range hashes {
			for _, torrent := range torrents {
				if torrent.Hash == hash {
					filtered = append(filtered, torrent)
				}
			}
		}
		torrents = filtered
	}
	return torrents
}

func (ts *TorrentStorage) GetAllSorted(category string, filter string, hashes []string, sortBy string, ascending bool) []*Torrent {
	torrents := ts.GetAll(category, filter, hashes)
	if sortBy != "" {
		slices.SortFunc(torrents, func(a, b *Torrent) int {
			var result int

			switch sortBy {
			case "name":
				result = cmp.Compare(a.Name, b.Name)
			case "size":
				result = cmp.Compare(a.Size, b.Size)
			case "added_on":
				result = cmp.Compare(a.AddedOn, b.AddedOn)
			case "completed":
				result = cmp.Compare(a.Completed, b.Completed)
			case "progress":
				result = cmp.Compare(a.Progress, b.Progress)
			case "state":
				result = cmp.Compare(a.State, b.State)
			case "category":
				result = cmp.Compare(a.Category, b.Category)
			case "dlspeed":
				result = cmp.Compare(a.Dlspeed, b.Dlspeed)
			case "upspeed":
				result = cmp.Compare(a.Upspeed, b.Upspeed)
			case "ratio":
				result = cmp.Compare(a.Ratio, b.Ratio)
			default:
				// Default sort by added_on
				result = cmp.Compare(a.AddedOn, b.AddedOn)
			}

			// If descending order, negate the result
			if !ascending {
				result = -result
			}

			return result
		})
	}
	return torrents
}

func (ts *TorrentStorage) Update(ctx context.Context, torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent
	go func(ctx context.Context) {
		// Check if context is cancelled before expensive operation
		select {
		case <-ctx.Done():
			ts.logger.Debug().
				Str("operation", "Update").
				Str("hash", torrent.Hash).
				Msg("Save operation cancelled")
			return
		default:
		}

		err := ts.saveToFile()
		if err != nil {
			ts.logger.Error().
				Err(err).
				Str("operation", "Update").
				Str("hash", torrent.Hash).
				Str("category", torrent.Category).
				Msg("Failed to save torrent storage")
		}
	}(ctx)
}

func (ts *TorrentStorage) Delete(ctx context.Context, hash, category string, removeFromDebrid bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	wireStore := Get()
	for key, torrent := range ts.torrents {
		if torrent == nil {
			continue
		}
		if torrent.Hash == hash && (category == "" || torrent.Category == category) {
			if torrent.State == "queued" && torrent.ID != "" {
				// Remove the torrent from the import queue if it exists
				wireStore.importsQueue.Delete(torrent.ID)
			}
			if removeFromDebrid && torrent.DebridID != "" && torrent.Debrid != "" {
				dbClient := wireStore.debrid.Client(torrent.Debrid)
				if dbClient != nil {
					_ = dbClient.DeleteTorrent(ctx, torrent.DebridID)
				}
			}
			delete(ts.torrents, key)

			// Delete the torrent folder
			if torrent.ContentPath != "" {
				err := os.RemoveAll(torrent.ContentPath)
				if err != nil {
					return
				}
			}
			break
		}
	}
	go func(ctx context.Context) {
		// Check if context is cancelled before expensive operation
		select {
		case <-ctx.Done():
			ts.logger.Debug().
				Str("operation", "Delete").
				Str("hash", hash).
				Msg("Save operation cancelled")
			return
		default:
		}

		err := ts.saveToFile()
		if err != nil {
			ts.logger.Error().
				Err(err).
				Str("operation", "Delete").
				Str("hash", hash).
				Str("category", category).
				Msg("Failed to save torrent storage")
		}
	}(ctx)
}

func (ts *TorrentStorage) DeleteMultiple(ctx context.Context, hashes []string, removeFromDebrid bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	toDelete := make(map[string]string)

	st := Get()

	for _, hash := range hashes {
		for key, torrent := range ts.torrents {
			if torrent == nil {
				continue
			}
			if torrent.Hash == hash {
				if torrent.State == "queued" && torrent.ID != "" {
					// Remove the torrent from the import queue if it exists
					st.importsQueue.Delete(torrent.ID)
				}
				if removeFromDebrid && torrent.DebridID != "" && torrent.Debrid != "" {
					toDelete[torrent.DebridID] = torrent.Debrid
				}
				delete(ts.torrents, key)
				if torrent.ContentPath != "" {
					err := os.RemoveAll(torrent.ContentPath)
					if err != nil {
						return
					}
				}
				break
			}
		}
	}
	go func(ctx context.Context) {
		// Check if context is cancelled before expensive operation
		select {
		case <-ctx.Done():
			ts.logger.Debug().
				Str("operation", "DeleteMultiple").
				Int("count", len(hashes)).
				Msg("Save operation cancelled")
			return
		default:
		}

		err := ts.saveToFile()
		if err != nil {
			ts.logger.Error().
				Err(err).
				Str("operation", "DeleteMultiple").
				Int("count", len(hashes)).
				Msg("Failed to save torrent storage")
		}
	}(ctx)

	clients := st.debrid.Clients()

	go func(ctx context.Context) {
		for id, debrid := range toDelete {
			// Check if context is cancelled before each deletion
			select {
			case <-ctx.Done():
				ts.logger.Debug().
					Str("operation", "DeleteMultiple").
					Msg("Debrid deletion cancelled")
				return
			default:
			}

			dbClient, ok := clients[debrid]
			if !ok {
				continue
			}
			err := dbClient.DeleteTorrent(ctx, id)
			if err != nil {
				ts.logger.Error().
					Err(err).
					Str("operation", "DeleteMultiple").
					Str("debrid_id", id).
					Str("debrid", debrid).
					Msg("Failed to delete torrent from debrid")
			}
		}
	}(ctx)
}

func (ts *TorrentStorage) Save() error {
	return ts.saveToFile()
}

// saveToFile is a helper function to write the current state to the JSON file
func (ts *TorrentStorage) saveToFile() error {
	ts.mu.RLock()
	data, err := json.MarshalIndent(ts.torrents, "", "  ")
	ts.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(ts.filename, data, 0644)
}

func (ts *TorrentStorage) Reset() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents = make(Torrents)
}

// GetStalledTorrents returns a list of torrents that are stalled
// A torrent is considered stalled if it has no seeds, no progress, and has been downloading for longer than removeStalledAfter
// The torrent must have a DebridID and be in the "downloading" state
func (ts *TorrentStorage) GetStalledTorrents(removeAfter time.Duration) []*Torrent {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	stalled := make([]*Torrent, 0, 10) // Preallocate with reasonable initial capacity
	currentTime := time.Now()
	for _, torrent := range ts.torrents {
		if torrent.DebridID != "" && torrent.State == "downloading" && torrent.NumSeeds == 0 && torrent.Progress == 0 {
			addedOn := time.Unix(torrent.AddedOn, 0)
			if currentTime.Sub(addedOn) > removeAfter {
				stalled = append(stalled, torrent)
			}
		}
	}
	return stalled
}
