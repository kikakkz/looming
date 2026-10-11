// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package joincmd

import "errors"

// statfsFreeBytes has no portable answer outside Linux (the syscall
// shape differs per OS and Looming hosts are Linux regardless — the
// runtime plane is Docker). The collector degrades: the disk fact
// stays unobserved, everything else still ships.
var statfsFreeBytes = func(string) (uint64, error) {
	return 0, errors.New("disk facts are collected on linux hosts only")
}
