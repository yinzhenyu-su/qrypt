// Package crypt wraps drive drivers with rclone-compatible filename and
// content encryption.
//
// It is a provider-independent adapter: it must preserve the drive contract,
// report runtime capabilities accurately, and avoid leaking encryption details
// into VFS or concrete provider drivers.
package crypt
