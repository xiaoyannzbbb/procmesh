package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/qleelulu/procmesh/internal/errcode"
)

// requiredBinaries are replaced in the running agent executable directory.
var requiredBinaries = []string{"procmesh-agent", "procmesh-shim", "procmesh"}

const maxExtractedBytes = 512 << 20

// TarInstaller extracts procmesh-agent, procmesh-shim, and procmesh from a
// release tarball, keeps one previous bundle, and replaces dest via temp+rename.
type TarInstaller struct{}

func (TarInstaller) Install(ctx context.Context, tarball []byte, destDir, previousDir string) error {
	if err := ctx.Err(); err != nil {
		return mapApplyErr(err)
	}
	extracted, err := extractRequiredBinaries(tarball)
	if err != nil {
		return err
	}
	if err := savePrevious(destDir, previousDir, extracted); err != nil {
		return err
	}
	return replaceBinaries(destDir, extracted)
}

func isRequiredBinary(name string) bool {
	for _, b := range requiredBinaries {
		if name == b {
			return true
		}
	}
	return false
}

func archivePathUnsafe(name string) bool {
	name = strings.ReplaceAll(name, `\`, "/")
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return true
	}
	if len(name) >= 2 && name[1] == ':' {
		return true
	}
	cleaned := path.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || path.IsAbs(cleaned) {
		return true
	}
	for _, p := range strings.Split(name, "/") {
		if p == ".." {
			return true
		}
	}
	return false
}

func extractRequiredBinaries(tarball []byte) (map[string][]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, errcode.Wrap(errcode.INVALID, "invalid gzip archive", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	out := make(map[string][]byte, len(requiredBinaries))
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errcode.Wrap(errcode.INVALID, "invalid tar archive", err)
		}
		if archivePathUnsafe(hdr.Name) {
			return nil, errcode.E(errcode.INVALID, "archive path traversal")
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
		default:
			continue
		}
		base := path.Base(filepath.ToSlash(hdr.Name))
		if !isRequiredBinary(base) {
			continue
		}
		if hdr.Size < 0 || hdr.Size > maxExtractedBytes {
			return nil, errcode.E(errcode.INVALID, "invalid tar archive")
		}
		if total > maxExtractedBytes-hdr.Size {
			return nil, errcode.E(errcode.INVALID, "archive too large")
		}
		body, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
		if err != nil {
			return nil, errcode.Wrap(errcode.INVALID, "invalid tar archive", err)
		}
		if int64(len(body)) != hdr.Size {
			return nil, errcode.E(errcode.INVALID, "invalid tar archive")
		}
		total += int64(len(body))
		out[base] = body
	}
	for _, name := range requiredBinaries {
		if _, ok := out[name]; !ok {
			return nil, errcode.E(errcode.INVALID, "missing required binary")
		}
	}
	return out, nil
}

func savePrevious(destDir, previousDir string, replacing map[string][]byte) error {
	if strings.TrimSpace(previousDir) == "" {
		return errcode.E(errcode.INVALID, "previous dir required")
	}
	parent := filepath.Dir(previousDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return errcode.E(errcode.UNAVAILABLE, "prepare update directory failed")
	}
	staging := previousDir + ".next"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return errcode.E(errcode.UNAVAILABLE, "prepare previous bundle failed")
	}
	defer os.RemoveAll(staging)

	for _, name := range requiredBinaries {
		if _, ok := replacing[name]; !ok {
			continue
		}
		src := filepath.Join(destDir, name)
		st, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return errcode.E(errcode.UNAVAILABLE, "stat existing binary failed")
		}
		if !st.Mode().IsRegular() {
			continue
		}
		if err := copyFile(src, filepath.Join(staging, name)); err != nil {
			return err
		}
	}

	backup := previousDir + ".old"
	_ = os.RemoveAll(backup)
	if err := os.Rename(previousDir, backup); err != nil && !os.IsNotExist(err) {
		return errcode.E(errcode.UNAVAILABLE, "rotate previous bundle failed")
	}
	if err := os.Rename(staging, previousDir); err != nil {
		if _, statErr := os.Stat(backup); statErr == nil {
			_ = os.Rename(backup, previousDir)
		}
		return errcode.E(errcode.UNAVAILABLE, "save previous bundle failed")
	}
	_ = os.RemoveAll(backup)
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return errcode.E(errcode.UNAVAILABLE, "read existing binary failed")
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		return errcode.E(errcode.UNAVAILABLE, "write previous binary failed")
	}
	return nil
}

func replaceBinaries(destDir string, files map[string][]byte) error {
	if strings.TrimSpace(destDir) == "" {
		return errcode.E(errcode.INVALID, "destination dir required")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return errcode.E(errcode.UNAVAILABLE, "prepare binary directory failed")
	}

	type staged struct{ tmp, final string }
	var tmps []staged
	defer func() {
		for _, s := range tmps {
			if s.tmp != "" {
				_ = os.Remove(s.tmp)
			}
		}
	}()

	for _, name := range requiredBinaries {
		body := files[name]
		final := filepath.Join(destDir, name)
		f, err := os.CreateTemp(destDir, "."+name+".*.new")
		if err != nil {
			return errcode.E(errcode.UNAVAILABLE, "stage binary failed")
		}
		tmpName := f.Name()
		if _, err := f.Write(body); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpName)
			return errcode.E(errcode.UNAVAILABLE, "write staged binary failed")
		}
		if err := f.Chmod(0o755); err != nil {
			_ = f.Close()
			_ = os.Remove(tmpName)
			return errcode.E(errcode.UNAVAILABLE, "chmod staged binary failed")
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(tmpName)
			return errcode.E(errcode.UNAVAILABLE, "close staged binary failed")
		}
		tmps = append(tmps, staged{tmp: tmpName, final: final})
	}
	for i, s := range tmps {
		if err := os.Rename(s.tmp, s.final); err != nil {
			return errcode.E(errcode.UNAVAILABLE, "replace binary failed")
		}
		tmps[i].tmp = ""
		_ = os.Chmod(s.final, 0o755)
	}
	return nil
}
