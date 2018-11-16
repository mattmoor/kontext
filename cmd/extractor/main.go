package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

const (
	BasePath   = "/var/run/kontext"
	TargetPath = "/workspace"
)

func copy(src, dest string, info os.FileInfo) error {
	from, err := os.Open(src)
	if err != nil {
		return err
	}
	defer from.Close()

	to, err := os.OpenFile(dest, os.O_RDWR|os.O_CREATE, info.Mode())
	if err != nil {
		return err
	}
	defer to.Close()

	_, err = io.Copy(to, from)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	err := filepath.Walk(BasePath,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if path == BasePath {
				return nil
			}
			relativePath := path[len(BasePath)+1:]
			target := filepath.Join(TargetPath, relativePath)

			if info.IsDir() {
				return os.MkdirAll(target, info.Mode())
			}
			if !info.Mode().IsRegular() {
				log.Printf("Skipping irregular file: %q", relativePath)
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(target), 0444); err != nil {
				return err
			}
			return copy(path, target, info)
		})
	if err != nil {
		log.Println(err)
	}
	log.Println("Done!")
}
