// SPDX-License-Identifier: Apache-2.0

//go:build linux

package joincmd

import "syscall"

// statfsFreeBytes reports the given path's available space in bytes.
// Looming hosts are Linux (the runtime plane is Docker), where
// syscall.Statfs answers directly. A package variable so tests can
// script it (AD-25: no real filesystem in unit tests).
var statfsFreeBytes = func(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
