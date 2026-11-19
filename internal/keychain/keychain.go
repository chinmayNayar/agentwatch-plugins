// Package keychain stores one secret in the OS credential store using the
// system's own tools, with the secret on stdin, never on the command line
// (where other users could read it from the process table):
//
//	macOS    /usr/bin/security (login keychain), via `security -i`
//	Linux    secret-tool (libsecret / Secret Service), when installed
//	Windows  Credential Manager (keychain_windows.go, Win32 API)
//
// When no store is available the caller falls back to a 0600 file.
package keychain

import "errors"

// ErrUnavailable means there is no usable credential store on this machine.
var ErrUnavailable = errors.New("no OS credential store available")

// ErrNotFound means the store works but holds no such secret.
var ErrNotFound = errors.New("secret not found")
