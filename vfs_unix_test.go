//go:build unix

package sqlite

import (
	"runtime"
	"testing"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestVFSDefaults(t *testing.T) {
	tls := libc.NewTLS()
	defer tls.Close()
	wantSync, wantOFD := int32(1), int32(-1)
	if runtime.GOOS == "darwin" {
		wantSync = -1
	}
	if runtime.GOOS == "linux" {
		wantOFD = 1
	}
	if got := sqlite3.Xmodernc_fdatasync(tls, -1); got != wantSync {
		t.Errorf("fdatasync = %d, want %d", got, wantSync)
	}
	if got := sqlite3.Xmodernc_ofd_locking(tls, -1); got != wantOFD {
		t.Errorf("OFD locking = %d, want %d", got, wantOFD)
	}
}
