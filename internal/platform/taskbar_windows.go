//go:build windows

package platform

import (
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// taskbarProgress drives ITaskbarList3 on the console window (conhost).
// COM lives on one locked OS thread that is the object's only user (single
// threaded apartment); any COM failure turns the progress into a silent
// no-op (python-behaviour §5.9).
type taskbarProgress struct {
	ch   chan tbMsg
	once sync.Once
	done chan struct{}
}

type tbMsg struct {
	state ProgressState
	frac  float64
	close bool
}

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	clsidTaskbarList     = windows.GUID{Data1: 0x56FDF344, Data2: 0xFD6D, Data3: 0x11d0, Data4: [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}
	iidTaskbarList3      = windows.GUID{Data1: 0xEA1AFB91, Data2: 0x9E28, Data3: 0x4B86, Data4: [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF}}
)

// vtable slots of ITaskbarList3
const (
	slotRelease          = 2
	slotHrInit           = 3
	slotSetProgressValue = 9
	slotSetProgressState = 10
)

func tbFlags(s ProgressState) uintptr {
	switch s {
	case ProgressIndeterminate:
		return 1
	case ProgressNormal:
		return 2
	case ProgressError:
		return 4
	case ProgressPaused:
		return 8
	}
	return 0
}

func newTaskbarProgress(hwnd windows.HWND) *taskbarProgress {
	p := &taskbarProgress{ch: make(chan tbMsg, 32), done: make(chan struct{})}
	go p.loop(hwnd)
	return p
}

// comObject is the first word of every COM interface: its vtable.
type comObject struct{ vtbl *[16]uintptr }

func vcall(obj *comObject, slot int, args ...uintptr) uintptr {
	fn := obj.vtbl[slot]
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)...)
	return r
}

func (p *taskbarProgress) loop(hwnd windows.HWND) {
	defer close(p.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var obj *comObject
	if hwnd != 0 && windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED) == nil {
		defer windows.CoUninitialize()
		r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidTaskbarList)), 0, 1, // CLSCTX_INPROC_SERVER
			uintptr(unsafe.Pointer(&iidTaskbarList3)), uintptr(unsafe.Pointer(&obj)))
		if r != 0 || obj == nil || vcall(obj, slotHrInit) != 0 {
			obj = nil
		}
	}
	defer func() {
		if obj != nil {
			vcall(obj, slotRelease)
		}
	}()
	last := tbMsg{state: -1}
	for m := range p.ch {
		if m.close || obj == nil {
			if m.close {
				return
			}
			continue
		}
		if m.state != last.state {
			vcall(obj, slotSetProgressState, uintptr(hwnd), tbFlags(m.state))
		}
		switch m.state {
		case ProgressNormal, ProgressError, ProgressPaused:
			v := uint64(max(0, min(1000, int(m.frac*1000))))
			vcall(obj, slotSetProgressValue, uintptr(hwnd), uintptr(v), 1000)
		}
		last = m
	}
}

// Set queues a state change; it never blocks the caller.
func (p *taskbarProgress) Set(s ProgressState, f float64) {
	select {
	case <-p.done:
	case p.ch <- tbMsg{state: s, frac: f}:
	default: // the COM thread is behind; the next update supersedes this one
	}
}

// Close leaves the last state visible (the full bar stays while the window
// waits for a key) and stops the COM thread.
func (p *taskbarProgress) Close() {
	p.once.Do(func() {
		select {
		case <-p.done:
		case p.ch <- tbMsg{close: true}:
		}
		<-p.done
	})
}
