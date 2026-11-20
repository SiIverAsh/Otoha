package views

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/scrape"
)

// HistoryRow pairs a history entry with the serialized state used to resume playback.
type HistoryRow struct {
	Entry      db.HistoryEntry
	ResumeJSON string
}

// SettingsPage supplies preference fields and library administration controls.
type SettingsPage struct {
	Config   config.Config
	Storages []db.Storage
	Users    []db.User
	CanAdmin bool
	Task     SettingsTask
}

// SettingsTask describes progress and the most recent ASMR18 preview.
type SettingsTask struct {
	Checkpoint            *scrape.AsmrOneSyncCheckpoint
	CheckpointUnavailable bool
	PreviewTitle          string
	PreviewWorkno         string
	PreviewJSON           string
}

func historyDeleteURL(path, ageMode string, days int) string {
	query := url.Values{"age": {ageMode}}
	if days > 0 {
		query.Set("days", strconv.Itoa(days))
	}
	return path + "?" + query.Encode()
}

func historyTime(unix int64) string {
	return time.Unix(unix, 0).Format("2006/1/2 15:04:05")
}

func historyDateTime(unix int64) string {
	return time.Unix(unix, 0).Format(time.RFC3339)
}

func countLabel(count int) string {
	if count == 0 {
		return "?"
	}
	return strconv.Itoa(count)
}

func storageSectionID(storage db.Storage) string {
	return "settings-storage-" + strconv.FormatInt(storage.ID, 10)
}

func accountNames(users []db.User) string {
	names := make([]string, 0, len(users))
	for _, user := range users {
		name := user.Username
		if user.MustResetPassword {
			name += "（待设密）"
		}
		names = append(names, name)
	}
	return strings.Join(names, "、")
}
