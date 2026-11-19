//go:build windows

package keychain

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procCredWriteW = advapi32.NewProc("CredWriteW")
	procCredReadW  = advapi32.NewProc("CredReadW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
)

type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

func target(service, account string) (*uint16, error) {
	return syscall.UTF16PtrFromString(service + ":" + account)
}

// Set stores secret in Windows Credential Manager.
func Set(service, account, secret string) error {
	t, err := target(service, account)
	if err != nil {
		return err
	}
	user, _ := syscall.UTF16PtrFromString(account)
	blob := []byte(secret)
	c := credential{Type: credTypeGeneric, TargetName: t, UserName: user, Persist: credPersistLocalMachine,
		CredentialBlobSize: uint32(len(blob))}
	if len(blob) > 0 {
		c.CredentialBlob = &blob[0]
	}
	r, _, _ := procCredWriteW.Call(uintptr(unsafe.Pointer(&c)), 0)
	if r == 0 {
		return ErrUnavailable
	}
	return nil
}

// Get reads a secret.
func Get(service, account string) (string, error) {
	t, err := target(service, account)
	if err != nil {
		return "", err
	}
	var pc *credential
	r, _, _ := procCredReadW.Call(uintptr(unsafe.Pointer(t)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&pc)))
	if r == 0 || pc == nil {
		return "", ErrNotFound
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(pc)))
	b := unsafe.Slice(pc.CredentialBlob, pc.CredentialBlobSize)
	return string(b), nil
}

// Delete removes a secret.
func Delete(service, account string) error {
	t, err := target(service, account)
	if err != nil {
		return err
	}
	procCredDelete.Call(uintptr(unsafe.Pointer(t)), credTypeGeneric, 0)
	return nil
}
