package dlsite

import (
	"context"
	"fmt"
)

// FetchReleaseDate checks product metadata without fetching dynamic statistics,
// original editions, covers, tracks, or media. It stops at the first known date.
func (c *Client) FetchReleaseDate(ctx context.Context, workno string) (*Metadata, error) {
	wno := NormalizeWorkno(workno)
	if wno == "" {
		return nil, fmt.Errorf("invalid workno %q", workno)
	}
	meta, productErr := c.fetchProductJSON(ctx, wno, FormatLocaleParam(c.opts.Locale))
	if productErr == nil && meta.ReleaseDate != "" {
		return meta, nil
	}
	htmlMeta, htmlErr := c.fetchHTML(ctx, wno, c.opts.Locale)
	if htmlErr == nil && htmlMeta.ReleaseDate != "" {
		return htmlMeta, nil
	}
	if fb := c.tryFallback(ctx, wno); fb != nil && fb.ReleaseDate != "" {
		return fb, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if productErr == nil {
		return meta, nil
	}
	if htmlErr == nil {
		return htmlMeta, nil
	}
	return nil, productErr
}
