package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Dryd33n/ephget/internal/fsutil"
	"github.com/Dryd33n/ephget/internal/index"
	"github.com/Dryd33n/ephget/internal/ui"
)

const (
	indexMaxAge   = 24 * time.Hour
	indexMaxBytes = 16 << 20 // far above the 300 KB the pipeline allows
)

// LoadIndex returns the catalog, refreshing it first if it is over a day old. If
// the refresh fails, it warns and uses the stale copy when there is one.
func (c *Client) LoadIndex(ctx context.Context) (*index.Index, error) {
	info, statErr := os.Stat(c.Layout.Index())
	if statErr == nil && time.Since(info.ModTime()) < indexMaxAge {
		idx, err := index.Load(c.Layout.Index())
		if err == nil {
			return idx, nil
		}
		// A damaged or outdated local copy shouldn't wait out the day.
		fmt.Fprintf(c.Warn, "%s local index unusable, downloading it again: %v\n", ui.Err.Yellow("warning:"), err)
	}
	refreshErr := c.refresh(ctx, statErr == nil)
	if refreshErr == nil {
		return index.Load(c.Layout.Index())
	}
	if statErr != nil {
		return nil, fmt.Errorf("couldn't download the app catalog: %w", refreshErr)
	}
	idx, err := index.Load(c.Layout.Index())
	if err != nil {
		return nil, fmt.Errorf("couldn't download the app catalog: %w", refreshErr)
	}
	fmt.Fprintf(c.Warn, "%s using an old copy of the app catalog: %v\n", ui.Err.Yellow("warning:"), refreshErr)
	return idx, nil
}

func (c *Client) refresh(ctx context.Context, haveCopy bool) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.IndexURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	// The URL redirects twice and only the final host answers with a 304. Go's
	// client resends this header on each redirect.
	if haveCopy {
		if _, err := index.Load(c.Layout.Index()); err == nil {
			if etag, err := os.ReadFile(c.Layout.IndexETag()); err == nil && len(etag) > 0 {
				req.Header.Set("If-None-Match", strings.TrimSpace(string(etag)))
			}
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		now := time.Now()
		return os.Chtimes(c.Layout.Index(), now, now)
	case http.StatusOK:
	default:
		return fmt.Errorf("the server answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, indexMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > indexMaxBytes {
		return fmt.Errorf("the index is larger than %d MB", indexMaxBytes>>20)
	}
	if _, err := index.Parse(data); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Layout.Home, 0o755); err != nil {
		return err
	}
	// Drop the old ETag first: a crash between these writes must not leave an
	// ETag that vouches for a different file.
	if err := os.Remove(c.Layout.IndexETag()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := fsutil.WriteAtomic(c.Layout.Index(), data); err != nil {
		return err
	}
	if etag := resp.Header.Get("ETag"); etag != "" {
		return fsutil.WriteAtomic(c.Layout.IndexETag(), []byte(etag))
	}
	return nil
}
