package scrape

import (
	"time"

	"github.com/SiIverAsh/otoha/internal/asmrone"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

// NewClient builds the DLsite client with the optional asmr.one Tier-4 fallback attached.
// minInterval overrides cfg.ScraperIntervalSec when > 0 (single-work UI scrapes use a short one).
func NewClient(cfg *config.Config, cache *dlsite.Cache, minInterval time.Duration) (*dlsite.Client, error) {
	snap := cfg.Snapshot()
	if minInterval <= 0 {
		minInterval = time.Duration(snap.ScraperIntervalSec * float64(time.Second))
	}

	opts := dlsite.ClientOptions{
		BaseURL:     snap.DLsiteBaseURL,
		Locale:      snap.ScraperLocale,
		UserAgent:   snap.ScraperUserAgent,
		ProxyURL:    snap.ScraperProxy,
		MinInterval: minInterval,
		Cache:       cache,
		CoversDir:   cfg.CoversDir(),
	}

	if minInterval > 0 && minInterval <= 200*time.Millisecond {
		opts.RequestTimeout = 6 * time.Second
		opts.MaxRetries = 1
	}

	if snap.AsmrOneEnabled {
		fb, err := asmrone.New(asmrone.Options{
			BaseURLs:    snap.AsmrOneBaseURLs,
			Locale:      snap.ScraperLocale,
			UserAgent:   snap.ScraperUserAgent,
			ProxyURL:    snap.ScraperProxy,
			MinInterval: time.Duration(snap.AsmrOneIntervalSec * float64(time.Second)),
		})
		if err != nil {
			return nil, err
		}
		opts.Fallback = fb
	}
	return dlsite.NewClient(opts)
}
