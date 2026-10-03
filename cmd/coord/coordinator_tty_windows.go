//go:build windows

package main

import (
	"encoding/binary"
	"os"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

func ttyFile(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		return true
	}
	return msysPTY(h)
}

func processTerminal() bool {
	if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		f.Close()
		return true
	}
	return slices.ContainsFunc([]*os.File{os.Stdin, os.Stdout, os.Stderr}, func(f *os.File) bool { return f != nil && ttyFile(f) })
}

func msysPTY(h windows.Handle) bool {
	if t, err := windows.GetFileType(h); err != nil || t != windows.FILE_TYPE_PIPE {
		return false
	}
	buf := make([]byte, 4+2*windows.MAX_PATH)
	if err := windows.GetFileInformationByHandleEx(h, windows.FileNameInfo, &buf[0], uint32(len(buf))); err != nil {
		return false
	}
	n := int(binary.LittleEndian.Uint32(buf[:4])) / 2
	if n <= 0 || 4+2*n > len(buf) {
		return false
	}
	name := windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[4])), n))
	return msysPTYName(name)
}
