package cache

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
)

type Space struct {
	path string
}

func Application(appId string) (*Space, error) {
	cdir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	p := filepath.Join(cdir, appId)
	if err := os.MkdirAll(p, 0700); err != nil {
		return nil, err
	}
	return &Space{p}, nil
}

func isFresh(dir string, maxAge time.Duration) bool {
	dstat, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		log.Fatal(err)
	}
	modTime := dstat.ModTime()
	files, err := os.ReadDir(dir)
	if err != nil {
		log.Fatal(err)
	}
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			log.Fatal(err)
		}
		if info.ModTime().After(modTime) {
			modTime = info.ModTime()
		}
	}
	return time.Since(modTime) <= maxAge
}

func GetOrCreate[T any](s *Space, id string, maxAge time.Duration, load, create func(dir string) (T, error)) (T, error) {
	var t T
	p := filepath.Join(s.path, id)
	if isFresh(p, maxAge) {
		return load(p)
	}
	if err := os.RemoveAll(p); err != nil {
		return t, err
	}
	if err := os.MkdirAll(p, 0700); err != nil {
		return t, err
	}
	return create(p)
}
