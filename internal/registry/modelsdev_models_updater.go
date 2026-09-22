package registry

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// MaxModelsDevSize caps the models.dev catalog payload (api.json is ~4.6MB
// today). Shared with cmd/fetch_modelsdev_models so the regen CLI never
// writes snapshots the runtime updater would reject.
const MaxModelsDevSize = 8 << 20

var modelsdevURLs = []string{
	"https://models.dev/api.json",
}

var modelsdevUpdaterOnce sync.Once

// StartModelsDevUpdater starts a background updater that fetches the
// models.dev catalog immediately and refreshes it every 3 hours.
// Safe to call multiple times; only one updater runs.
func StartModelsDevUpdater(ctx context.Context) {
	modelsdevUpdaterOnce.Do(func() {
		go runModelsDevUpdater(ctx)
	})
}

func runModelsDevUpdater(ctx context.Context) {
	tryRefreshModelsDev(ctx, "startup models.dev refresh")

	ticker := time.NewTicker(modelsRefreshInterval)
	defer ticker.Stop()
	log.Infof("periodic models.dev refresh started (interval=%s)", modelsRefreshInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tryRefreshModelsDev(ctx, "periodic models.dev refresh")
		}
	}
}

func tryRefreshModelsDev(ctx context.Context, label string) {
	data, sourceURL := fetchModelsDevFromRemote(ctx)
	if data == nil {
		log.Warnf("%s: fetch failed from all URLs, keeping current data (fallback catalog)", label)
		return
	}

	changed, err := loadModelsDevLiveFromBytes(data, sourceURL)
	if err != nil {
		log.Warnf("%s: fetched catalog rejected, keeping current data: %v", label, err)
		return
	}
	if len(changed) == 0 {
		log.Infof("%s completed from %s, no changes detected", label, sourceURL)
		return
	}
	log.Infof("%s completed from %s, changes detected for providers: %v", label, sourceURL, changed)
	notifyModelRefresh(changed)
}

func fetchModelsDevFromRemote(ctx context.Context) ([]byte, string) {
	client := &http.Client{Timeout: modelsFetchTimeout}
	for _, sourceURL := range modelsdevURLs {
		reqCtx, cancel := context.WithTimeout(ctx, modelsFetchTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, sourceURL, nil)
		if err != nil {
			cancel()
			log.Debugf("models.dev fetch request creation failed for %s: %v", sourceURL, err)
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			log.Debugf("models.dev fetch failed from %s: %v", sourceURL, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			log.Debugf("models.dev fetch returned %d from %s", resp.StatusCode, sourceURL)
			continue
		}

		data, errRead := io.ReadAll(io.LimitReader(resp.Body, MaxModelsDevSize+1))
		errClose := resp.Body.Close()
		cancel()
		if errRead != nil {
			log.Debugf("models.dev fetch read error from %s: %v", sourceURL, errRead)
			continue
		}
		if errClose != nil {
			log.Debugf("models.dev response close failed for %s: %v", sourceURL, errClose)
			continue
		}
		if len(data) > MaxModelsDevSize {
			log.Warnf("models.dev fetch from %s exceeded %d bytes, rejecting", sourceURL, MaxModelsDevSize)
			continue
		}

		return data, sourceURL
	}
	return nil, ""
}
