package registry

import (
	"context"
	"fmt"
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
	_, _ = refreshModelsDev(ctx, label)
}

// TriggerModelsDevRefresh runs one models.dev fetch outside the ticker
// (management API, manual use). It returns the changed provider names.
func TriggerModelsDevRefresh(ctx context.Context) ([]string, error) {
	return refreshModelsDev(ctx, "manual models.dev refresh")
}

func refreshModelsDev(ctx context.Context, label string) ([]string, error) {
	data, sourceURL := fetchModelsDevFromRemote(ctx)
	if data == nil {
		msg := "fetch failed from all URLs, keeping current data (fallback catalog)"
		setModelsDevLastError(msg)
		log.Warnf("%s: %s", label, msg)
		return nil, fmt.Errorf("%s", msg)
	}

	changed, err := loadModelsDevLiveFromBytes(data, sourceURL)
	changedCustom, errIndex := loadModelsDevCustomProviders(data, sourceURL)
	if errIndex != nil {
		// The fixed opencode/zai sections already loaded, so a failure here
		// must not discard them. Keep the previous custom-provider index and
		// report the fault without failing the whole refresh.
		log.Warnf("%s: custom provider capability index rejected, keeping previous index: %v", label, errIndex)
	}
	if err != nil {
		setModelsDevLastError(err.Error())
		log.Warnf("%s: fetched catalog rejected, keeping current data: %v", label, err)
		return nil, err
	}
	// Custom providers are matched by base URL rather than by a models.dev
	// provider ID, so their entries are appended to the changed list: the
	// registration callback keys custom-provider auths off this payload.
	for _, base := range changedCustom {
		changed = append(changed, ModelsDevCustomProviderPrefix+base)
	}
	setModelsDevLastError("")
	if len(changed) == 0 {
		log.Infof("%s completed from %s, no changes detected", label, sourceURL)
		return nil, nil
	}
	log.Infof("%s completed from %s, changes detected for providers: %v", label, sourceURL, changed)
	notifyModelRefresh(changed)
	return changed, nil
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
