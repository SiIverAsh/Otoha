package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/history"
)

func TestSafeWebNext(t *testing.T) {
	tests := []struct {
		name, candidate, want string
	}{
		{name: "search parameters", candidate: "/?q=voice+actor", want: "/?q=voice+actor"},
		{name: "discard session token", candidate: "/history?token=example&age=all", want: "/history?age=all"},
		{name: "absolute destination", candidate: "https://example.com/settings", want: "/"},
		{name: "protocol relative", candidate: "//example.com/settings", want: "/"},
		{name: "encoded slash", candidate: "/%2Fexample.com/settings", want: "/"},
		{name: "non-document route", candidate: "/api/export/metadata", want: "/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SafeWebNext(test.candidate); got != test.want {
				t.Fatalf("SafeWebNext(%q) = %q, want %q", test.candidate, got, test.want)
			}
		})
	}
}

func TestAuthenticateWebResetsMigratedAccountAndIssuesSession(t *testing.T) {
	for _, test := range []struct {
		name, password string
		status         int
		needsReset     bool
	}{
		{name: "weak password", password: "abc", status: http.StatusForbidden, needsReset: true},
		{name: "first password", password: "some password", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := managementTestAPI(t)
			user, err := api.Store.UpsertUser(t.Context(), "migrated", "", true, true)
			if err != nil {
				t.Fatal(err)
			}
			const testHash = "test password hash"
			api.Auth.HashPassword = func(string) (string, error) { return testHash, nil }
			api.Auth.IssueSessionCookie = func(w http.ResponseWriter, r *http.Request, id int64) (string, error) {
				const token = "test-session"
				if err := api.Store.CreateSession(r.Context(), token, id, time.Now().Add(time.Hour).Unix()); err != nil {
					return "", err
				}
				http.SetCookie(w, &http.Cookie{Name: "otoha_session", Value: token})
				return token, nil
			}
			form := url.Values{"username": {" migrated "}, "password": {test.password}, "next": {"/settings"}}
			request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			result := api.AuthenticateWeb(recorder, request)
			if result.Status != test.status || result.ResetPassword != test.needsReset || result.Next != "/settings" {
				t.Fatalf("unexpected login result: %+v", result)
			}
			cookies := recorder.Result().Cookies()
			if test.needsReset {
				if len(cookies) != 0 {
					t.Fatalf("a rejected password received cookies: %+v", cookies)
				}
				return
			}
			if len(cookies) != 1 {
				t.Fatalf("expected session cookie, got %+v", cookies)
			}
			loggedIn, err := api.Store.GetUserBySession(t.Context(), cookies[0].Value)
			if err != nil {
				t.Fatal(err)
			}
			if loggedIn.ID != user.ID || loggedIn.MustResetPassword || loggedIn.PasswordHash != testHash {
				t.Fatalf("unexpected account after login: %+v", loggedIn)
			}
		})
	}
}

func TestDeleteWebHistoryReturnsCurrentUsersFilteredFragment(t *testing.T) {
	api := managementTestAPI(t)
	const title = `Track "Title" <script>alert(1)</script>`
	seedWebHistory(t, api, 1, 1, "RJ123456", title, dlsite.AgeGeneral)
	seedWebHistory(t, api, 2, 1, "RJ123457", "Mature", dlsite.AgeR18)
	seedWebHistory(t, api, 3, 2, "RJ123458", "Other account", dlsite.AgeGeneral)
	seedWebHistory(t, api, 4, 1, "RJ123459", "Remove me", dlsite.AgeGeneral)

	request := httptest.NewRequest(http.MethodDelete, "/ui/history/4?age=general", nil)
	request.SetPathValue("id", "4")
	request = request.WithContext(WithUser(t.Context(), &db.User{ID: 1}))
	recorder := httptest.NewRecorder()
	api.DeleteWebHistory(recorder, request)
	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("expected HTML fragment, got %d %v", recorder.Code, recorder.Header())
	}
	document, err := goquery.NewDocumentFromReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	buttons := document.Find("button[data-action='resume-history']")
	if buttons.Length() != 1 || document.Find("script").Length() != 0 {
		t.Fatal("history included hidden, removed or unsafe markup")
	}
	var resume db.HistoryEntry
	if err := json.Unmarshal([]byte(buttons.AttrOr("data-entry", "")), &resume); err != nil {
		t.Fatal(err)
	}
	if resume.ID != 1 || resume.WorkTitle != title {
		t.Fatalf("resume button rendered wrong entry: %+v", resume)
	}

	var workno string
	if err := api.Store.DB().QueryRowContext(t.Context(), "SELECT workno FROM listen_history WHERE id = 4").Scan(&workno); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("history record was not removed: %v", err)
	}
}

func TestDeleteWebHistoryRejectsInvalidDaysWithoutClearingRecords(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "malformed escape", query: "days=%"},
		{name: "duplicate", query: "days=1&days=2"},
		{name: "empty", query: "days="},
		{name: "zero", query: "days=0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := managementTestAPI(t)
			const workno = "RJ123456"
			seedWebHistory(t, api, 1, 1, workno, "Track", dlsite.AgeGeneral)
			ctx := WithUser(t.Context(), &db.User{ID: 1})
			request := httptest.NewRequest(http.MethodDelete, "/ui/history", nil).WithContext(ctx)
			request.URL.RawQuery = test.query
			recorder := httptest.NewRecorder()
			api.DeleteWebHistory(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("got status %d, want %d", recorder.Code, http.StatusBadRequest)
			}

			var remaining string
			row := api.Store.DB().QueryRowContext(ctx, "SELECT workno FROM listen_history WHERE id = 1")
			if err := row.Scan(&remaining); err != nil || remaining != workno {
				t.Fatalf("invalid request changed history: workno=%q err=%v", remaining, err)
			}
		})
	}
}

func managementTestAPI(t *testing.T) *API {
	t.Helper()
	cfg := config.Default(t.TempDir())
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return &API{Cfg: cfg, Store: store, History: history.NewService(store, cfg)}
}

func seedWebHistory(t *testing.T, api *API, id, userID int64, workno, title string, ageCategory int) {
	t.Helper()
	ctx := t.Context()
	if err := api.Store.SaveMetadata(ctx, &dlsite.Metadata{
		Workno: workno, Title: title, AgeCategory: ageCategory,
	}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Store.DB().ExecContext(ctx,
		"INSERT INTO listen_history (id, user_id, workno, started_at, last_at) VALUES (?, ?, ?, 1, 1)",
		id, userID, workno,
	); err != nil {
		t.Fatal(err)
	}
}
