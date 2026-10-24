package vault

import (
	"errors"
	"fmt"
	"syscall"
)

func checkRecoverySpace(path string, needed uint64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return err
	}
	if stat.Bsize <= 0 {
		return errors.New("cannot determine recovery filesystem space")
	}
	free := stat.Bavail * uint64(stat.Bsize)
	if needed+(16<<20) < needed {
		return errors.New("invalid recovery size")
	}
	if free < needed+(16<<20) {
		return fmt.Errorf("not enough free space for recovery: need at least %d MiB, have %d MiB", (needed+(16<<20))>>20, free>>20)
	}
	return nil
}
