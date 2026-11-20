package handlers

import (
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/SiIverAsh/otoha/web/views"
	"github.com/a-h/templ"
)

const (
	catalogPageSize    = 40
	catalogPageLimit   = 200
	catalogRecentLimit = 8
)

// CatalogQuery reads the catalog controls and defaults from a web request.
func (a *API) CatalogQuery(r *http.Request) views.CatalogQuery {
	values := r.URL.Query()
	source, _ := parseWorkSource(values.Get("source"))
	opts := a.listWorkOptions(r, source)
	if opts.PageSize <= 0 || opts.PageSize > catalogPageLimit {
		opts.PageSize = catalogPageSize
	}
	if opts.Page <= 0 || opts.Page > math.MaxInt/opts.PageSize {
		opts.Page = 1
	}
	if !views.IsCatalogSort(opts.SortBy) {
		opts.SortBy = "release_date"
	}
	if !views.IsCatalogAge(opts.AgeMode) {
		opts.AgeMode = "general"
	}
	view := values.Get("view")
	if view == "" {
		if cookie, err := r.Cookie("otoha_view"); err == nil {
			view = cookie.Value
		}
	}
	if view != "table" {
		view = "grid"
	}
	return views.CatalogQuery{Query: strings.TrimSpace(values.Get("q")), View: view, Options: opts}
}

// CatalogPage renders the catalog or an index using the authenticated request.
func (a *API) CatalogPage(r *http.Request, tab string) (templ.Component, error) {
	ctx := r.Context()
	query := a.CatalogQuery(r)
	opts := query.Options
	opts.UserID = UserFromContext(ctx).ID
	circles, vas, tags, err := a.Store.ListFacets(ctx, opts.AgeMode, opts.Source)
	if err != nil {
		return nil, fmt.Errorf("read work facets: %w", err)
	}

	page := views.CatalogData{Query: query, Circles: circles, VAs: vas, Tags: tags}
	if tab != "works" {
		return views.CatalogIndex(page, tab), nil
	}

	if query.Query == "" {
		page.Works, page.Total, err = a.Store.ListWorks(ctx, opts)
		page.SearchDegraded, page.SearchWarning = a.Search.IsDegraded()
	} else {
		items, total, result, searchErr := a.searchWorks(ctx, query.Query, opts)
		if searchErr != nil {
			return nil, fmt.Errorf("search works: %w", searchErr)
		}
		page.Works, page.Total = items, total
		page.SearchDegraded, page.SearchWarning = result.Degraded, result.Warning
	}
	if err != nil {
		return nil, fmt.Errorf("list works: %w", err)
	}

	if opts.Page == 1 && !query.HasFilters() {
		page.Recent, err = a.History.ListRecent(ctx, opts.UserID, opts.AgeMode, catalogRecentLimit)
		if err != nil {
			return nil, fmt.Errorf("list recently played works: %w", err)
		}
	}
	return views.Catalog(page), nil
}
