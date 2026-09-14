package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Archive writes root as a .tar.gz at file, with paths relative to root.
func Archive(root, file string) error {
	out, err := os.Create(file)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return out.Close()
}

// Extract unpacks a .tar.gz into a fresh temporary directory and returns
// it; the caller removes it. Entries escaping the directory are rejected.
func Extract(file string) (string, error) {
	in, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return "", fmt.Errorf("%s: %w", file, err)
	}
	dir, err := os.MkdirTemp("", "gnife-restore-*")
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		name := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") || strings.Contains(name, string(filepath.Separator)+"..") {
			os.RemoveAll(dir)
			return "", fmt.Errorf("%s: unsafe entry '%s'", file, hdr.Name)
		}
		target := filepath.Join(dir, name)
		if !strings.HasPrefix(target, dir+string(filepath.Separator)) {
			os.RemoveAll(dir)
			return "", fmt.Errorf("%s: unsafe entry '%s'", file, hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				os.RemoveAll(dir)
				return "", err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				os.RemoveAll(dir)
				return "", err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				os.RemoveAll(dir)
				return "", err
			}
			f.Close()
		}
	}
	return dir, nil
}

// IsArchive reports whether path names a .tar.gz / .tgz file.
func IsArchive(path string) bool {
	return strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".tgz")
}
