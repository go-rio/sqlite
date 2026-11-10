//go:build unix

package sqlite

import (
	"os"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// init turns on fdatasync commits and, unless MODERNC_SQLITE_OFD_LOCK is set,
// OFD locks; the library leaves either off where the platform lacks it.
func init() {
	tls := libc.NewTLS()
	defer tls.Close()
	sqlite3.Xmodernc_fdatasync(tls, 1)
	if _, set := os.LookupEnv("MODERNC_SQLITE_OFD_LOCK"); !set {
		sqlite3.Xmodernc_ofd_locking(tls, 1)
	}
}
