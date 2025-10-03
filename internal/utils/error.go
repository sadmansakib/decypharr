package utils

import "errors"

type HTTPError struct {
	StatusCode int
	Message    string
	Code       string
}

func (e *HTTPError) Error() string {
	return e.Message
}

var HosterUnavailableError = &HTTPError{
	StatusCode: 503,
	Message:    "Hoster is unavailable",
	Code:       "hoster_unavailable",
}

var TrafficExceededError = &HTTPError{
	StatusCode: 503,
	Message:    "Traffic exceeded",
	Code:       "traffic_exceeded",
}

var ErrLinkBroken = &HTTPError{
	StatusCode: 404,
	Message:    "File is unavailable",
	Code:       "file_unavailable",
}

var TorrentNotFoundError = &HTTPError{
	StatusCode: 404,
	Message:    "Torrent not found",
	Code:       "torrent_not_found",
}

var TooManyActiveDownloadsError = &HTTPError{
	StatusCode: 509,
	Message:    "Too many active downloads",
	Code:       "too_many_active_downloads",
}

var DatabaseError = &HTTPError{
	StatusCode: 500,
	Message:    "Database error occurred",
	Code:       "database_error",
}

func IsTooManyActiveDownloadsError(err error) bool {
	return errors.As(err, &TooManyActiveDownloadsError)
}

func IsDatabaseError(err error) bool {
	return errors.Is(err, DatabaseError)
}
