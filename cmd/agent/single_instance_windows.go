//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32Single = windows.NewLazySystemDLL("kernel32.dll")
	procCreateMutexW  = modKernel32Single.NewProc("CreateMutexW")
	instanceMutex     windows.Handle
)

// ensureSingleInstance makes sure only one agent runs per user session. Two
// instances would fight over the OLE clipboard and the shared cache files.
func ensureSingleInstance() error {
	name, err := windows.UTF16PtrFromString(`Local\clipboard-sync-agent`)
	if err != nil {
		return fmt.Errorf("encode mutex name: %w", err)
	}
	handle, _, errno := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return fmt.Errorf("create single-instance mutex: %w", errno)
	}
	if errno == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(windows.Handle(handle))
		return fmt.Errorf("another clipboard-sync agent is already running in this session")
	}
	instanceMutex = windows.Handle(handle)
	return nil
}
