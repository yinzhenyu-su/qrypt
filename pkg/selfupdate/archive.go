package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
)

// ExtractBinary extracts the named file from a release archive into dst and
// marks it executable. innerName is matched on the base name so a tar entry
// with a "./" prefix still resolves.
func ExtractBinary(archivePath string, format ArchiveFormat, innerName, dst string) error {
	switch format {
	case FormatTarGz:
		return extractTarGz(archivePath, innerName, dst)
	case FormatZip:
		return extractZip(archivePath, innerName, dst)
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
}

func extractTarGz(archivePath, innerName, dst string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if path.Base(header.Name) != innerName {
			continue
		}
		return writeExtracted(dst, tr)
	}
	return fmt.Errorf("%s: %w", innerName, os.ErrNotExist)
}

func extractZip(archivePath, innerName, dst string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, file := range zr.File {
		if file.FileInfo().IsDir() || path.Base(file.Name) != innerName {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		err = writeExtracted(dst, rc)
		rc.Close()
		return err
	}
	return fmt.Errorf("%s: %w", innerName, os.ErrNotExist)
}

func writeExtracted(dst string, r io.Reader) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
