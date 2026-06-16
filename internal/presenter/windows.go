//go:build windows

package presenter

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"clipboard-sync/cache"
	"clipboard-sync/protocol"
	"golang.org/x/sys/windows"
)

const (
	tymedHGlobal    = 1
	tymedIStream    = 4
	dvaspectContent = 1
	fdFileSize      = 0x00000040
	fdProgressUI    = 0x00004000
	ghnd            = 0x0042
	streamSeekSet   = 0
	stgtyStream     = 2
	pmRemove        = 0x0001
	maxHGlobalFile  = 64 * 1024 * 1024
)

var (
	modOle32                  = windows.NewLazySystemDLL("ole32.dll")
	modKernel32               = windows.NewLazySystemDLL("kernel32.dll")
	modUser32                 = windows.NewLazySystemDLL("user32.dll")
	procOleInitialize         = modOle32.NewProc("OleInitialize")
	procOleSetClipboard       = modOle32.NewProc("OleSetClipboard")
	procOleFlushClipboard     = modOle32.NewProc("OleFlushClipboard")
	procCoTaskMemAlloc        = modOle32.NewProc("CoTaskMemAlloc")
	procGlobalAllocPresenter  = modKernel32.NewProc("GlobalAlloc")
	procGlobalLockPresenter   = modKernel32.NewProc("GlobalLock")
	procGlobalUnlockPresenter = modKernel32.NewProc("GlobalUnlock")
	procGetFileAttributesEx   = modKernel32.NewProc("GetFileAttributesExW")
	procPeekMessage           = modUser32.NewProc("PeekMessageW")
	procTranslateMessage      = modUser32.NewProc("TranslateMessage")
	procDispatchMessage       = modUser32.NewProc("DispatchMessageW")

	cfFileDescriptorW   uint16
	cfFileContents      uint16
	registerFormatsOnce sync.Once
	clipboardThread     *windowsPresenter
)

type windowsPresenter struct {
	accessor TransferAccessor
	mu       sync.RWMutex
	state    cache.RemoteClipboardState
	dataObj  *iDataObject
	streams  []*remoteFileStream
	enums    []*formatEnumerator
	refCount int32
	vtbl     *iDataObjectVtbl
	self     unsafe.Pointer
	requests chan clipboardRequest
}

type clipboardRequest struct {
	state  cache.RemoteClipboardState
	result chan error
}

type iDataObject struct {
	lpVtbl *iDataObjectVtbl
}

type iDataObjectVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	getData        uintptr
	getDataHere    uintptr
	queryGetData   uintptr
	getCanonical   uintptr
	setData        uintptr
	enumFormatEtc  uintptr
	dAdvise        uintptr
	dUnadvise      uintptr
	enumDAdvise    uintptr
}

type iStream struct {
	lpVtbl *iStreamVtbl
}

type iStreamVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	read           uintptr
	write          uintptr
	seek           uintptr
	setSize        uintptr
	copyTo         uintptr
	commit         uintptr
	revert         uintptr
	lockRegion     uintptr
	unlockRegion   uintptr
	stat           uintptr
	clone          uintptr
}

type iEnumFormatEtc struct {
	lpVtbl *iEnumFormatEtcVtbl
}

type iEnumFormatEtcVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	next           uintptr
	skip           uintptr
	reset          uintptr
	clone          uintptr
}

type formatEtc struct {
	cfFormat uint16
	ptd      uintptr
	dwAspect uint32
	lindex   int32
	tymed    uint32
}

type stgMedium struct {
	tymed          uint32
	unionMember    uintptr
	pUnkForRelease uintptr
}

type fileDescriptorW struct {
	dwFlags          uint32
	clsid            windows.GUID
	sizelCX          int32
	sizelCY          int32
	pointlX          int32
	pointlY          int32
	dwFileAttributes uint32
	ftCreationTime   syscall.Filetime
	ftLastAccessTime syscall.Filetime
	ftLastWriteTime  syscall.Filetime
	nFileSizeHigh    uint32
	nFileSizeLow     uint32
	cFileName        [260]uint16
}

type win32FileAttributeData struct {
	FileAttributes uint32
	CreationTime   syscall.Filetime
	LastAccessTime syscall.Filetime
	LastWriteTime  syscall.Filetime
	FileSizeHigh   uint32
	FileSizeLow    uint32
}

type statstg struct {
	PwcsName          uintptr
	Type              uint32
	CbSize            uint64
	Mtime             syscall.Filetime
	Ctime             syscall.Filetime
	Atime             syscall.Filetime
	GrfMode           uint32
	GrfLocksSupported uint32
	Clsid             windows.GUID
	GrfStateBits      uint32
	Reserved          uint32
}

type point struct {
	X int32
	Y int32
}

type windowMessage struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type remoteFileStream struct {
	vtbl      *iStreamVtbl
	presenter *windowsPresenter
	state     cache.RemoteClipboardState
	file      protocol.FileMeta
	refCount  int32
	offset    int64
}

type formatEnumerator struct {
	vtbl     *iEnumFormatEtcVtbl
	refCount int32
	formats  []formatEtc
	index    int
}

var (
	dataObjectVTable = &iDataObjectVtbl{
		queryInterface: syscall.NewCallback(dataObjectQueryInterface),
		addRef:         syscall.NewCallback(dataObjectAddRef),
		release:        syscall.NewCallback(dataObjectRelease),
		getData:        syscall.NewCallback(dataObjectGetData),
		getDataHere:    syscall.NewCallback(dataObjectGetDataHere),
		queryGetData:   syscall.NewCallback(dataObjectQueryGetData),
		getCanonical:   syscall.NewCallback(dataObjectGetCanonicalFormatEtc),
		setData:        syscall.NewCallback(dataObjectSetData),
		enumFormatEtc:  syscall.NewCallback(dataObjectEnumFormatEtc),
		dAdvise:        syscall.NewCallback(dataObjectDAdvise),
		dUnadvise:      syscall.NewCallback(dataObjectDUnadvise),
		enumDAdvise:    syscall.NewCallback(dataObjectEnumDAdvise),
	}
	streamVTable = &iStreamVtbl{
		queryInterface: syscall.NewCallback(streamQueryInterface),
		addRef:         syscall.NewCallback(streamAddRef),
		release:        syscall.NewCallback(streamRelease),
		read:           syscall.NewCallback(streamRead),
		write:          syscall.NewCallback(streamWrite),
		seek:           syscall.NewCallback(streamSeek),
		setSize:        syscall.NewCallback(streamSetSize),
		copyTo:         syscall.NewCallback(streamCopyTo),
		commit:         syscall.NewCallback(streamCommit),
		revert:         syscall.NewCallback(streamRevert),
		lockRegion:     syscall.NewCallback(streamLockRegion),
		unlockRegion:   syscall.NewCallback(streamUnlockRegion),
		stat:           syscall.NewCallback(streamStat),
		clone:          syscall.NewCallback(streamClone),
	}
	iidIUnknown         = windows.GUID{Data1: 0x00000000, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIDataObject      = windows.GUID{Data1: 0x0000010e, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIStream          = windows.GUID{Data1: 0x0000000c, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidIEnumFormatEtc   = windows.GUID{Data1: 0x00000103, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	enumFormatEtcVTable *iEnumFormatEtcVtbl
)

func New(_ string, accessor TransferAccessor) (Presenter, error) {
	registerClipboardFormats()
	p := &windowsPresenter{accessor: accessor, refCount: 1, vtbl: dataObjectVTable, requests: make(chan clipboardRequest)}
	p.dataObj = &iDataObject{lpVtbl: dataObjectVTable}
	p.self = unsafe.Pointer(p.dataObj)
	go p.clipboardLoop()
	return p, nil
}

func (p *windowsPresenter) PresentRemoteFiles(state cache.RemoteClipboardState) (Presentation, error) {
	result := make(chan error, 1)
	p.requests <- clipboardRequest{state: state, result: result}
	if err := <-result; err != nil {
		return Presentation{}, err
	}
	return Presentation{Handled: true}, nil
}

func (p *windowsPresenter) IsManagedPaths(_ []string) bool {
	return false
}

func (p *windowsPresenter) Close() error {
	if _, _, _ = procOleFlushClipboard.Call(); false {
	}
	return nil
}

func (p *windowsPresenter) setClipboard() error {
	obj := (*iDataObject)(p.self)
	hr, _, _ := procOleSetClipboard.Call(uintptr(unsafe.Pointer(obj)))
	if hr != 0 {
		return fmt.Errorf("OleSetClipboard failed: 0x%x", hr)
	}
	return nil
}

func (p *windowsPresenter) clipboardLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	clipboardThread = p
	if hr, _, _ := procOleInitialize.Call(0); hr != 0 && hr != 1 {
		for request := range p.requests {
			request.result <- fmt.Errorf("OleInitialize failed: 0x%x", hr)
		}
		return
	}
	for {
		select {
		case request := <-p.requests:
			p.mu.Lock()
			p.state = request.state
			p.streams = nil
			p.enums = nil
			p.mu.Unlock()
			request.result <- p.setClipboard()
		default:
			pumpPresenterMessages()
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func pumpPresenterMessages() {
	var msg windowMessage
	for {
		ret, _, _ := procPeekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, pmRemove)
		if ret == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (p *windowsPresenter) currentState() cache.RemoteClipboardState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

func registerClipboardFormats() {
	registerFormatsOnce.Do(func() {
		cfFileDescriptorW = registerClipboardFormat("FileGroupDescriptorW")
		cfFileContents = registerClipboardFormat("FileContents")
	})
}

func registerClipboardFormat(name string) uint16 {
	ptr, _ := windows.UTF16PtrFromString(name)
	ret, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("RegisterClipboardFormatW").Call(uintptr(unsafe.Pointer(ptr)))
	return uint16(ret)
}

func dataObjectQueryInterface(this uintptr, iid uintptr, object uintptr) uintptr {
	requested := (*windows.GUID)(unsafe.Pointer(iid))
	if *requested == iidIUnknown || *requested == iidIDataObject {
		*(*uintptr)(unsafe.Pointer(object)) = this
		dataObjectAddRef(this)
		return 0
	}
	*(*uintptr)(unsafe.Pointer(object)) = 0
	return 0x80004002
}

func dataObjectAddRef(this uintptr) uintptr {
	p := presenterFromIUnknown(this)
	return uintptr(atomic.AddInt32(&p.refCount, 1))
}

func dataObjectRelease(this uintptr) uintptr {
	p := presenterFromIUnknown(this)
	value := atomic.AddInt32(&p.refCount, -1)
	if value < 0 {
		atomic.StoreInt32(&p.refCount, 0)
		return 0
	}
	return uintptr(value)
}

func dataObjectGetData(this uintptr, format uintptr, medium uintptr) uintptr {
	p := presenterFromIUnknown(this)
	fe := (*formatEtc)(unsafe.Pointer(format))
	sm := (*stgMedium)(unsafe.Pointer(medium))
	state := p.currentState()
	if fe.cfFormat == cfFileContents {
		slog.Info("windows dataobject getdata", "format", clipboardFormatName(fe.cfFormat), "tymed", fe.tymed, "lindex", fe.lindex, "files", len(state.Files))
	}
	if fe.cfFormat == cfFileDescriptorW && fe.tymed&tymedHGlobal != 0 {
		h, err := buildFileGroupDescriptor(state.Files)
		if err != nil {
			return hResultFromError(err)
		}
		sm.tymed = tymedHGlobal
		sm.unionMember = h
		sm.pUnkForRelease = 0
		return 0
	}
	if fe.cfFormat == cfFileContents && fe.tymed&tymedIStream != 0 {
		if fe.lindex < 0 || fe.lindex >= int32(len(state.Files)) {
			return 0x80040064
		}
		stream := &remoteFileStream{vtbl: streamVTable, presenter: p, state: state, file: state.Files[fe.lindex], refCount: 1}
		p.mu.Lock()
		p.streams = append(p.streams, stream)
		p.mu.Unlock()
		sm.tymed = tymedIStream
		sm.unionMember = uintptr(unsafe.Pointer(stream))
		sm.pUnkForRelease = 0
		return 0
	}
	if fe.cfFormat == cfFileContents && fe.tymed&tymedHGlobal != 0 {
		if fe.lindex < 0 || fe.lindex >= int32(len(state.Files)) {
			return 0x80040064
		}
		file := state.Files[fe.lindex]
		if file.Size > maxHGlobalFile {
			return 0x80040064
		}
		payload, err := readFullRemoteFile(p.accessor, state, file)
		if err != nil {
			slog.Error("windows dataobject hglobal read failed", "file", file.Name, "error", err)
			return hResultFromError(err)
		}
		h, err := allocHGlobal(payload)
		if err != nil {
			return hResultFromError(err)
		}
		sm.tymed = tymedHGlobal
		sm.unionMember = h
		sm.pUnkForRelease = 0
		slog.Info("windows dataobject hglobal delivered", "file", file.Name, "size", len(payload))
		return 0
	}
	return 0x80040064
}

func dataObjectGetDataHere(uintptr, uintptr, uintptr) uintptr { return 0x80004001 }
func dataObjectQueryGetData(this uintptr, format uintptr) uintptr {
	_ = this
	fe := (*formatEtc)(unsafe.Pointer(format))
	if fe.cfFormat == cfFileContents {
		slog.Info("windows dataobject querygetdata", "format", clipboardFormatName(fe.cfFormat), "tymed", fe.tymed, "lindex", fe.lindex)
	}
	if fe.cfFormat == cfFileDescriptorW && fe.tymed&tymedHGlobal != 0 {
		return 0
	}
	if fe.cfFormat == cfFileContents && fe.tymed&tymedHGlobal != 0 {
		state := presenterFromIUnknown(this).currentState()
		if fe.lindex >= 0 && fe.lindex < int32(len(state.Files)) && state.Files[fe.lindex].Size <= maxHGlobalFile {
			return 0
		}
	}
	if fe.cfFormat == cfFileContents && fe.tymed&tymedIStream != 0 {
		return 0
	}
	return 0x80040064
}
func dataObjectGetCanonicalFormatEtc(_ uintptr, _ uintptr, out uintptr) uintptr {
	if out != 0 {
		fe := (*formatEtc)(unsafe.Pointer(out))
		fe.ptd = 0
	}
	return 0x00040130
}
func dataObjectSetData(uintptr, uintptr, uintptr, uintptr) uintptr { return 0x80004001 }
func dataObjectEnumFormatEtc(this uintptr, direction uintptr, out uintptr) uintptr {
	if direction != 1 {
		*(*uintptr)(unsafe.Pointer(out)) = 0
		return 0x80040064
	}
	p := presenterFromIUnknown(this)
	state := p.currentState()
	formats := make([]formatEtc, 0, len(state.Files)+1)
	formats = append(formats, formatEtc{cfFormat: cfFileDescriptorW, dwAspect: dvaspectContent, lindex: -1, tymed: tymedHGlobal})
	for idx := range state.Files {
		formats = append(formats, formatEtc{cfFormat: cfFileContents, dwAspect: dvaspectContent, lindex: int32(idx), tymed: fileContentsTymed(state.Files[idx])})
	}
	slog.Debug("windows dataobject enumformatetc", "files", len(state.Files), "formats", len(formats))
	enum := &formatEnumerator{vtbl: enumFormatEtcVTable, refCount: 1, formats: formats}
	p.mu.Lock()
	p.enums = append(p.enums, enum)
	p.mu.Unlock()
	*(*uintptr)(unsafe.Pointer(out)) = uintptr(unsafe.Pointer(enum))
	return 0
}
func dataObjectDAdvise(uintptr, uintptr, uintptr, uintptr, uintptr) uintptr { return 0x80040003 }
func dataObjectDUnadvise(uintptr, uintptr) uintptr                          { return 0x80040003 }
func dataObjectEnumDAdvise(uintptr, uintptr) uintptr                        { return 0x80040003 }

func streamQueryInterface(this uintptr, iid uintptr, object uintptr) uintptr {
	requested := (*windows.GUID)(unsafe.Pointer(iid))
	if *requested == iidIUnknown || *requested == iidIStream {
		*(*uintptr)(unsafe.Pointer(object)) = this
		streamAddRef(this)
		return 0
	}
	*(*uintptr)(unsafe.Pointer(object)) = 0
	return 0x80004002
}

func streamAddRef(this uintptr) uintptr {
	s := (*remoteFileStream)(unsafe.Pointer(this))
	return uintptr(atomic.AddInt32(&s.refCount, 1))
}

func streamRelease(this uintptr) uintptr {
	s := (*remoteFileStream)(unsafe.Pointer(this))
	value := atomic.AddInt32(&s.refCount, -1)
	if value <= 0 {
		return 0
	}
	return uintptr(value)
}

func streamRead(this uintptr, pv uintptr, cb uint32, pcbRead uintptr) uintptr {
	s := (*remoteFileStream)(unsafe.Pointer(this))
	start := s.offset
	request := int(cb)
	if remaining := s.file.Size - s.offset; remaining <= 0 {
		if pcbRead != 0 {
			*(*uint32)(unsafe.Pointer(pcbRead)) = 0
		}
		slog.Debug("windows file stream read eof", "file", s.file.Name, "offset", start, "requested", cb)
		return 1
	} else if remaining < int64(request) {
		request = int(remaining)
	}
	chunk, err := readRemoteExact(s.presenter.accessor, s.state, s.file, s.offset, request)
	if err == io.EOF && len(chunk) == 0 {
		if pcbRead != 0 {
			*(*uint32)(unsafe.Pointer(pcbRead)) = 0
		}
		slog.Debug("windows file stream read eof", "file", s.file.Name, "offset", start, "requested", cb)
		return 1
	}
	if err != nil && len(chunk) == 0 {
		slog.Error("windows file stream read failed", "file", s.file.Name, "offset", start, "requested", cb, "error", err)
		return hResultFromError(err)
	}
	if len(chunk) > 0 {
		copy(unsafe.Slice((*byte)(unsafe.Pointer(pv)), len(chunk)), chunk)
		s.offset += int64(len(chunk))
	}
	if pcbRead != 0 {
		*(*uint32)(unsafe.Pointer(pcbRead)) = uint32(len(chunk))
	}
	slog.Debug("windows file stream read", "file", s.file.Name, "offset", start, "requested", cb, "returned", len(chunk), "err", err)
	if len(chunk) == 0 {
		return 1
	}
	return 0
}

func streamWrite(uintptr, uintptr, uint32, uintptr) uintptr { return 0x80004001 }
func streamSeek(this uintptr, moveLow int64, origin uint32, newPos uintptr) uintptr {
	s := (*remoteFileStream)(unsafe.Pointer(this))
	switch origin {
	case streamSeekSet:
		s.offset = moveLow
	case 1:
		s.offset += moveLow
	case 2:
		s.offset = s.file.Size + moveLow
	default:
		return 0x80070057
	}
	if s.offset < 0 {
		s.offset = 0
	}
	if newPos != 0 {
		*(*int64)(unsafe.Pointer(newPos)) = s.offset
	}
	return 0
}
func streamSetSize(uintptr, uint64) uintptr { return 0x80004001 }
func streamCopyTo(this uintptr, dest uintptr, cb uint64, pcbRead uintptr, pcbWritten uintptr) uintptr {
	if dest == 0 {
		return 0x80070057
	}
	s := (*remoteFileStream)(unsafe.Pointer(this))
	remaining := cb
	if s.offset >= s.file.Size {
		remaining = 0
	} else if remaining > uint64(s.file.Size-s.offset) {
		remaining = uint64(s.file.Size - s.offset)
	}
	bufferSize := 64 * 1024
	buffer := make([]byte, bufferSize)
	var totalRead uint64
	var totalWritten uint64
	for remaining > 0 {
		request := bufferSize
		if uint64(request) > remaining {
			request = int(remaining)
		}
		chunk, err := readRemoteExact(s.presenter.accessor, s.state, s.file, s.offset, request)
		if err == io.EOF && len(chunk) == 0 {
			break
		}
		if err != nil && len(chunk) == 0 {
			slog.Error("windows file stream copyto read failed", "file", s.file.Name, "offset", s.offset, "requested", request, "error", err)
			return hResultFromError(err)
		}
		if len(chunk) == 0 {
			break
		}
		copy(buffer, chunk)
		var written uint32
		hr, _, _ := syscall.SyscallN((*iStream)(unsafe.Pointer(dest)).lpVtbl.write, dest, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(chunk)), uintptr(unsafe.Pointer(&written)))
		if hr != 0 {
			return hr
		}
		s.offset += int64(len(chunk))
		remaining -= uint64(len(chunk))
		totalRead += uint64(len(chunk))
		totalWritten += uint64(written)
		if written < uint32(len(chunk)) {
			break
		}
	}
	if pcbRead != 0 {
		*(*uint64)(unsafe.Pointer(pcbRead)) = totalRead
	}
	if pcbWritten != 0 {
		*(*uint64)(unsafe.Pointer(pcbWritten)) = totalWritten
	}
	slog.Debug("windows file stream copyto", "file", s.file.Name, "read", totalRead, "written", totalWritten)
	return 0
}
func streamCommit(uintptr, uint32) uintptr                       { return 0 }
func streamRevert(uintptr) uintptr                               { return 0x80004001 }
func streamLockRegion(uintptr, uint64, uint64, uint32) uintptr   { return 0x80004001 }
func streamUnlockRegion(uintptr, uint64, uint64, uint32) uintptr { return 0x80004001 }
func streamStat(this uintptr, pstatstg uintptr, grfStatFlag uint32) uintptr {
	_ = grfStatFlag
	if pstatstg == 0 {
		return 0x80070057
	}
	s := (*remoteFileStream)(unsafe.Pointer(this))
	stat := (*statstg)(unsafe.Pointer(pstatstg))
	*stat = statstg{Type: stgtyStream, CbSize: uint64(s.file.Size)}
	return 0
}
func streamClone(uintptr, uintptr) uintptr { return 0x80004001 }

func enumFormatEtcQueryInterface(this uintptr, iid uintptr, object uintptr) uintptr {
	requested := (*windows.GUID)(unsafe.Pointer(iid))
	if *requested == iidIUnknown || *requested == iidIEnumFormatEtc {
		*(*uintptr)(unsafe.Pointer(object)) = this
		enumFormatEtcAddRef(this)
		return 0
	}
	*(*uintptr)(unsafe.Pointer(object)) = 0
	return 0x80004002
}

func enumFormatEtcAddRef(this uintptr) uintptr {
	e := (*formatEnumerator)(unsafe.Pointer(this))
	return uintptr(atomic.AddInt32(&e.refCount, 1))
}

func enumFormatEtcRelease(this uintptr) uintptr {
	e := (*formatEnumerator)(unsafe.Pointer(this))
	value := atomic.AddInt32(&e.refCount, -1)
	if value <= 0 {
		return 0
	}
	return uintptr(value)
}

func enumFormatEtcNext(this uintptr, count uint32, rgelt uintptr, fetched uintptr) uintptr {
	e := (*formatEnumerator)(unsafe.Pointer(this))
	if count == 0 {
		if fetched != 0 {
			*(*uint32)(unsafe.Pointer(fetched)) = 0
		}
		return 1
	}
	returned := uint32(0)
	for returned < count && e.index < len(e.formats) {
		current := e.formats[e.index]
		target := (*formatEtc)(unsafe.Pointer(rgelt + uintptr(returned)*unsafe.Sizeof(formatEtc{})))
		*target = current
		returned++
		e.index++
	}
	if fetched != 0 {
		*(*uint32)(unsafe.Pointer(fetched)) = returned
	}
	if returned == count {
		return 0
	}
	return 1
}

func enumFormatEtcSkip(this uintptr, count uint32) uintptr {
	e := (*formatEnumerator)(unsafe.Pointer(this))
	e.index += int(count)
	if e.index > len(e.formats) {
		e.index = len(e.formats)
		return 1
	}
	return 0
}

func enumFormatEtcReset(this uintptr) uintptr {
	e := (*formatEnumerator)(unsafe.Pointer(this))
	e.index = 0
	return 0
}

func enumFormatEtcClone(this uintptr, out uintptr) uintptr {
	e := (*formatEnumerator)(unsafe.Pointer(this))
	cloneFormats := append([]formatEtc(nil), e.formats...)
	clone := &formatEnumerator{vtbl: enumFormatEtcVTable, refCount: 1, formats: cloneFormats, index: e.index}
	*(*uintptr)(unsafe.Pointer(out)) = uintptr(unsafe.Pointer(clone))
	return 0
}

func presenterFromIUnknown(this uintptr) *windowsPresenter {
	if clipboardThread != nil {
		return clipboardThread
	}
	return (*windowsPresenter)(unsafe.Pointer(this))
}

func buildFileGroupDescriptor(files []protocol.FileMeta) (uintptr, error) {
	const headerSize = 4
	entrySize := int(unsafe.Sizeof(fileDescriptorW{}))
	totalSize := headerSize + len(files)*entrySize
	hMem, _, err := procGlobalAllocPresenter.Call(ghnd, uintptr(totalSize))
	if hMem == 0 {
		return 0, fmt.Errorf("GlobalAlloc failed: %w", err)
	}
	ptr, _, err := procGlobalLockPresenter.Call(hMem)
	if ptr == 0 {
		return 0, fmt.Errorf("GlobalLock failed: %w", err)
	}
	defer procGlobalUnlockPresenter.Call(hMem)
	buffer := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), totalSize)
	binary.LittleEndian.PutUint32(buffer[:4], uint32(len(files)))
	for i, file := range files {
		desc := fileDescriptorW{dwFlags: fdFileSize | fdProgressUI}
		populateDescriptor(&desc, file)
		entryPtr := unsafe.Pointer(uintptr(ptr) + uintptr(headerSize+i*entrySize))
		*(*fileDescriptorW)(entryPtr) = desc
	}
	return hMem, nil
}

func allocHGlobal(data []byte) (uintptr, error) {
	size := uintptr(len(data))
	if size == 0 {
		size = 1
	}
	hMem, _, err := procGlobalAllocPresenter.Call(ghnd, size)
	if hMem == 0 {
		return 0, fmt.Errorf("GlobalAlloc failed: %w", err)
	}
	ptr, _, err := procGlobalLockPresenter.Call(hMem)
	if ptr == 0 {
		return 0, fmt.Errorf("GlobalLock failed: %w", err)
	}
	if len(data) > 0 {
		copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data)), data)
	}
	procGlobalUnlockPresenter.Call(hMem)
	return hMem, nil
}

func readFullRemoteFile(accessor TransferAccessor, state cache.RemoteClipboardState, file protocol.FileMeta) ([]byte, error) {
	if file.Size > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("file too large for memory transfer: %s size=%d", file.Name, file.Size)
	}
	payload := make([]byte, 0, int(file.Size))
	var offset int64
	deadline := time.Now().Add(2 * time.Minute)
	for offset < file.Size {
		remaining := file.Size - offset
		request := 512 * 1024
		if remaining < int64(request) {
			request = int(remaining)
		}
		chunk, err := accessor.ReadRemoteFile(state, file, offset, request)
		if len(chunk) > 0 {
			payload = append(payload, chunk...)
			offset += int64(len(chunk))
		}
		if err != nil && len(chunk) == 0 {
			return nil, err
		}
		if len(chunk) == 0 {
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("timeout waiting for remote file data at offset=%d", offset)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return payload, nil
}

func readRemoteExact(accessor TransferAccessor, state cache.RemoteClipboardState, file protocol.FileMeta, offset int64, size int) ([]byte, error) {
	payload := make([]byte, 0, size)
	for len(payload) < size {
		chunk, err := accessor.ReadRemoteFile(state, file, offset+int64(len(payload)), size-len(payload))
		if len(chunk) > 0 {
			payload = append(payload, chunk...)
		}
		if err != nil {
			if err == io.EOF && len(payload) > 0 {
				return payload, nil
			}
			return payload, err
		}
		if len(chunk) == 0 {
			return payload, fmt.Errorf("remote file read returned no data at offset=%d", offset+int64(len(payload)))
		}
	}
	return payload, nil
}

func clipboardFormatName(format uint16) string {
	switch format {
	case cfFileDescriptorW:
		return "FileGroupDescriptorW"
	case cfFileContents:
		return "FileContents"
	default:
		return fmt.Sprintf("format_%d", format)
	}
}

func fileContentsTymed(file protocol.FileMeta) uint32 {
	if file.Size <= maxHGlobalFile {
		return tymedIStream | tymedHGlobal
	}
	return tymedIStream
}

func populateDescriptor(desc *fileDescriptorW, file protocol.FileMeta) {
	path := file.Path
	if path != "" {
		ptr, _ := windows.UTF16PtrFromString(path)
		var data win32FileAttributeData
		ret, _, _ := procGetFileAttributesEx.Call(uintptr(unsafe.Pointer(ptr)), 0, uintptr(unsafe.Pointer(&data)))
		if ret != 0 {
			desc.dwFileAttributes = data.FileAttributes
			desc.ftCreationTime = data.CreationTime
			desc.ftLastAccessTime = data.LastAccessTime
			desc.ftLastWriteTime = data.LastWriteTime
		}
	}
	if desc.dwFileAttributes == 0 {
		desc.dwFileAttributes = 0x80
	}
	size := uint64(file.Size)
	desc.nFileSizeHigh = uint32(size >> 32)
	desc.nFileSizeLow = uint32(size)
	name := strings.ReplaceAll(filepath.ToSlash(file.Name), "/", "\\")
	utf16, _ := windows.UTF16FromString(name)
	copy(desc.cFileName[:], utf16)
}

func hResultFromError(err error) uintptr {
	if err == nil {
		return 0
	}
	if errno, ok := err.(syscall.Errno); ok {
		return uintptr(errno)
	}
	return 0x80004005
}

func init() {
	enumFormatEtcVTable = &iEnumFormatEtcVtbl{
		queryInterface: syscall.NewCallback(enumFormatEtcQueryInterface),
		addRef:         syscall.NewCallback(enumFormatEtcAddRef),
		release:        syscall.NewCallback(enumFormatEtcRelease),
		next:           syscall.NewCallback(enumFormatEtcNext),
		skip:           syscall.NewCallback(enumFormatEtcSkip),
		reset:          syscall.NewCallback(enumFormatEtcReset),
		clone:          syscall.NewCallback(enumFormatEtcClone),
	}
}
