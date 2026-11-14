package storage

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/db"
	"golang.org/x/net/webdav"
)

// ReadOnlyMultiStorageFS implements webdav.FileSystem over all configured local storages
// while strictly rejecting every write/mutation operation (§2 & §9: read-only /dav, zero paywall).
type ReadOnlyMultiStorageFS struct {
	store *db.Store
}

func NewReadOnlyMultiStorageFS(store *db.Store) *ReadOnlyMultiStorageFS {
	return &ReadOnlyMultiStorageFS{store: store}
}

func (vfs *ReadOnlyMultiStorageFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return os.ErrPermission
}

func (vfs *ReadOnlyMultiStorageFS) RemoveAll(ctx context.Context, name string) error {
	return os.ErrPermission
}

func (vfs *ReadOnlyMultiStorageFS) Rename(ctx context.Context, oldName, newName string) error {
	return os.ErrPermission
}

func (vfs *ReadOnlyMultiStorageFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		return nil, os.ErrPermission
	}
	clean := strings.Trim(path.Clean("/"+strings.ReplaceAll(name, "\\", "/")), "/")
	if clean == "" {
		storages, err := vfs.store.ListStorages(ctx)
		if err != nil {
			return nil, err
		}
		var entries []os.FileInfo
		for _, st := range storages {
			if st.Kind == "local" && st.Name != "" {
				entries = append(entries, virtualDirInfo{name: st.Name, modTime: time.Now()})
			}
		}
		return &virtualRootFile{entries: entries}, nil
	}

	parts := strings.SplitN(clean, "/", 2)
	storageName := parts[0]
	rel := ""
	if len(parts) == 2 {
		rel = parts[1]
	}

	storages, err := vfs.store.ListStorages(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range storages {
		if st.Kind == "local" && st.Name == storageName {
			abs, err := SafeJoin(st.Root, rel)
			if err != nil {
				return nil, os.ErrPermission
			}
			return os.Open(abs)
		}
	}
	return nil, os.ErrNotExist
}

func (vfs *ReadOnlyMultiStorageFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	clean := strings.Trim(path.Clean("/"+strings.ReplaceAll(name, "\\", "/")), "/")
	if clean == "" {
		return virtualDirInfo{name: "/", modTime: time.Now()}, nil
	}
	parts := strings.SplitN(clean, "/", 2)
	storageName := parts[0]
	rel := ""
	if len(parts) == 2 {
		rel = parts[1]
	}
	storages, err := vfs.store.ListStorages(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range storages {
		if st.Kind == "local" && st.Name == storageName {
			if rel == "" {
				if info, err := os.Stat(st.Root); err == nil {
					return virtualDirInfo{name: storageName, modTime: info.ModTime()}, nil
				}
				return virtualDirInfo{name: storageName, modTime: time.Now()}, nil
			}
			abs, err := SafeJoin(st.Root, filepath.ToSlash(rel))
			if err != nil {
				return nil, os.ErrPermission
			}
			return os.Stat(abs)
		}
	}
	return nil, os.ErrNotExist
}

type virtualDirInfo struct {
	name    string
	modTime time.Time
}

func (v virtualDirInfo) Name() string       { return v.name }
func (v virtualDirInfo) Size() int64        { return 0 }
func (v virtualDirInfo) Mode() fs.FileMode  { return os.ModeDir | 0o555 }
func (v virtualDirInfo) ModTime() time.Time { return v.modTime }
func (v virtualDirInfo) IsDir() bool        { return true }
func (v virtualDirInfo) Sys() any           { return nil }

type virtualRootFile struct {
	entries []os.FileInfo
	pos     int
}

func (f *virtualRootFile) Close() error                                 { return nil }
func (f *virtualRootFile) Read(p []byte) (int, error)                   { return 0, os.ErrInvalid }
func (f *virtualRootFile) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (f *virtualRootFile) Write(p []byte) (int, error)                  { return 0, os.ErrPermission }
func (f *virtualRootFile) Stat() (os.FileInfo, error) {
	return virtualDirInfo{name: "/", modTime: time.Now()}, nil
}
func (f *virtualRootFile) Readdir(count int) ([]os.FileInfo, error) {
	if count <= 0 {
		out := f.entries[f.pos:]
		f.pos = len(f.entries)
		return out, nil
	}
	if f.pos >= len(f.entries) {
		return nil, os.ErrExist
	}
	end := f.pos + count
	if end > len(f.entries) {
		end = len(f.entries)
	}
	out := f.entries[f.pos:end]
	f.pos = end
	return out, nil
}

// NewWebDAVHandler returns an http.Handler serving strictly read-only WebDAV at `prefix` (e.g. "/dav").
func NewWebDAVHandler(prefix string, store *db.Store) http.Handler {
	dav := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: NewReadOnlyMultiStorageFS(store),
		LockSystem: webdav.NewMemLS(),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND":
			dav.ServeHTTP(w, r)
		default:
			http.Error(w, "WebDAV export is read-only", http.StatusMethodNotAllowed)
		}
	})
}
