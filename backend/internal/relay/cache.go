package relay

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

type DiskCache struct {
	root string
}

func NewDiskCache(root string) DiskCache {
	return DiskCache{root: root}
}

func (c DiskCache) Path(slug, ref string) string {
	return filepath.Join(c.root, slug, ref)
}

func (c DiskCache) HasFresh(slug, ref string, ttlSeconds int) bool {
	info, err := os.Stat(c.Path(slug, ref))
	if err != nil || info.IsDir() {
		return false
	}
	if ttlSeconds <= 0 {
		return true
	}
	return time.Since(info.ModTime()) < time.Duration(ttlSeconds)*time.Second
}

func (c DiskCache) Write(slug, ref string, reader io.Reader) (int64, error) {
	dir := filepath.Join(c.root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	finalPath := c.Path(slug, ref)
	tmpPath := finalPath + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return written, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return written, closeErr
	}
	return written, os.Rename(tmpPath, finalPath)
}

func (c DiskCache) Open(slug, ref string) (*os.File, os.FileInfo, error) {
	file, err := os.Open(c.Path(slug, ref))
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, nil, errors.New("cache entry is a directory")
	}
	return file, info, nil
}

func (c DiskCache) PurgeChannel(slug string) error {
	return os.RemoveAll(filepath.Join(c.root, slug))
}

func (c DiskCache) Cleanup(maxAge time.Duration) error {
	if maxAge <= 0 {
		return nil
	}
	return filepath.WalkDir(c.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if time.Since(info.ModTime()) > maxAge {
			_ = os.Remove(path)
		}
		return nil
	})
}
