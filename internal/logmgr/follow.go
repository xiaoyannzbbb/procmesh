package logmgr

import (
	"context"
	"io"
	"os"
	"time"
)

const followPoll = 50 * time.Millisecond

// Follow reads path and emits new bytes as they appear.
// fromEnd starts at the current file end; otherwise it reads from offset 0.
// Both channels close when ctx is cancelled.
func Follow(ctx context.Context, path string, fromEnd bool) (<-chan []byte, <-chan error) {
	dataCh := make(chan []byte, 16)
	errCh := make(chan error, 1)
	go func() {
		defer close(dataCh)
		defer close(errCh)
		if err := follow(ctx, path, fromEnd, dataCh); err != nil && ctx.Err() == nil {
			errCh <- err
		}
	}()
	return dataCh, errCh
}

func follow(ctx context.Context, path string, fromEnd bool, dataCh chan<- []byte) error {
	var (
		f   *os.File
		off int64
	)
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()

	buf := make([]byte, tailChunk)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f == nil {
			opened, err := os.Open(path)
			if err != nil {
				if os.IsNotExist(err) {
					if err := sleepPoll(ctx); err != nil {
						return err
					}
					continue
				}
				return err
			}
			f = opened
			if fromEnd {
				n, err := f.Seek(0, io.SeekEnd)
				if err != nil {
					return err
				}
				off = n
				fromEnd = false
			} else if _, err := f.Seek(off, io.SeekStart); err != nil {
				return err
			}
		}

		n, err := f.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			off += int64(n)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case dataCh <- chunk:
			}
		}
		if err == io.EOF {
			switched, switchErr := reopenIfRotated(f, path, &off)
			if switchErr != nil {
				return switchErr
			}
			if switched {
				_ = f.Close()
				f = nil
				continue
			}
			if err := sleepPoll(ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}

// reopenIfRotated reports whether the caller must close f and reopen path.
// Rotate renames the inode at path; an in-place truncate stays on the same fd.
func reopenIfRotated(f *os.File, path string, off *int64) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			*off = 0
			return true, nil
		}
		return false, err
	}
	cur, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(st, cur) {
		*off = 0
		return true, nil
	}
	if st.Size() < *off {
		*off = 0
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return true, nil
		}
	}
	return false, nil
}

func sleepPoll(ctx context.Context) error {
	t := time.NewTimer(followPoll)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
