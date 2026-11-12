// Package release builds user distributions without including local configuration.
package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type archiveFile struct {
	name, source string
	mode         int64
}

func writeArchive(output string, files []archiveFile, windows bool) error {
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	err = func() error {
		if windows {
			writer := zip.NewWriter(file)
			for _, item := range files {
				header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
				header.SetMode(os.FileMode(item.mode))
				header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
				entry, err := writer.CreateHeader(header)
				if err != nil {
					writer.Close()
					return err
				}
				if err := copyFileTo(entry, item.source); err != nil {
					writer.Close()
					return err
				}
			}
			return writer.Close()
		}
		compressed := gzip.NewWriter(file)
		writer := tar.NewWriter(compressed)
		for _, item := range files {
			info, err := os.Stat(item.source)
			if err != nil {
				writer.Close()
				compressed.Close()
				return err
			}
			if err := writer.WriteHeader(&tar.Header{Name: item.name, Mode: item.mode, Size: info.Size(), ModTime: time.Unix(0, 0)}); err != nil {
				writer.Close()
				compressed.Close()
				return err
			}
			if err := copyFileTo(writer, item.source); err != nil {
				writer.Close()
				compressed.Close()
				return err
			}
		}
		if err := writer.Close(); err != nil {
			compressed.Close()
			return err
		}
		return compressed.Close()
	}()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func copyFileTo(output io.Writer, name string) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(output, file)
	return err
}

func checksums(directory string, names []string) error {
	sort.Strings(names)
	file, err := os.OpenFile(filepath.Join(directory, "SHA256SUMS"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	for _, name := range names {
		hash := sha256.New()
		if err := copyFileTo(hash, filepath.Join(directory, name)); err != nil {
			file.Close()
			return err
		}
		if _, err := fmt.Fprintf(file, "%x  %s\n", hash.Sum(nil), name); err != nil {
			file.Close()
			return err
		}
	}
	return file.Close()
}
