//go:build windows

package clipboard

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"clipboard-sync/protocol"
	"golang.org/x/image/bmp"
	"golang.org/x/sys/windows"
)

const (
	cfUnicodeText = 13
	cfHDrop       = 15
	cfBitmap      = 2
	cfDIB         = 8
	cfDIBV5       = 17
	gmemeMoveable = 0x0002
	biRGB         = 0
	wmDestroy     = 0x0002
	wmRenderFmt   = 0x0305
	wmClipboard   = 0x031D
	hwndMessage   = ^uintptr(2)
	maxDIBBytes   = 64 * 1024 * 1024
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procEmptyClipboard      = user32.NewProc("EmptyClipboard")
	procEnumClipboardFmt    = user32.NewProc("EnumClipboardFormats")
	procGetClipboardData    = user32.NewProc("GetClipboardData")
	procGetClipboardFmtName = user32.NewProc("GetClipboardFormatNameW")
	procGetClipboardSeq     = user32.NewProc("GetClipboardSequenceNumber")
	procSetClipboardData    = user32.NewProc("SetClipboardData")
	procIsClipboardFormat   = user32.NewProc("IsClipboardFormatAvailable")
	procAddClipboardFormat  = user32.NewProc("RegisterClipboardFormatW")
	procAddClipboardListen  = user32.NewProc("AddClipboardFormatListener")
	procCreateWindowEx      = user32.NewProc("CreateWindowExW")
	procDefWindowProc       = user32.NewProc("DefWindowProcW")
	procDispatchMessage     = user32.NewProc("DispatchMessageW")
	procGetMessage          = user32.NewProc("GetMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procRegisterClassEx     = user32.NewProc("RegisterClassExW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procGlobalAlloc         = kernel32.NewProc("GlobalAlloc")
	procGlobalLock          = kernel32.NewProc("GlobalLock")
	procGlobalUnlock        = kernel32.NewProc("GlobalUnlock")
	procGlobalSize          = kernel32.NewProc("GlobalSize")
	procGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	procDragQueryFile       = shell32.NewProc("DragQueryFileW")
	remoteFormatOnce        sync.Once
	remoteClipboardFormatID uint32
	pngFormatID             uint32
	watchNotify             chan struct{}
	watchNotifyMu           sync.Mutex
	clipboardOwnerHwnd      uintptr
	clipboardOwnerMu        sync.Mutex
	delayedDIB              []byte
	delayedDIBMu            sync.Mutex
	wndProcCallback         = syscall.NewCallback(clipboardWndProc)
)

type windowsClipboard struct {
	mu           sync.Mutex
	pollInterval time.Duration
	last         string
	lastSeq      uint32
	ignoreSeq    uint32
	ignoreUntil  time.Time
	pendingSeq   uint32
	nextReadAt   time.Time
	retryCount   int
}

type point struct {
	X int32
	Y int32
}

type dropfiles struct {
	PFiles uint32
	Pt     point
	FNC    uint32
	Wide   uint32
}

type wndclassex struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type windowMessage struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func New(pollInterval time.Duration, fileWriter string) (Clipboard, error) {
	remoteFormatOnce.Do(func() {
		name, _ := windows.UTF16PtrFromString("ClipboardSyncRemoteMarker")
		ret, _, _ := procAddClipboardFormat.Call(uintptr(unsafe.Pointer(name)))
		remoteClipboardFormatID = uint32(ret)
		pngName, _ := windows.UTF16PtrFromString("PNG")
		pngRet, _, _ := procAddClipboardFormat.Call(uintptr(unsafe.Pointer(pngName)))
		pngFormatID = uint32(pngRet)
	})
	return &windowsClipboard{pollInterval: pollInterval}, nil
}

func (c *windowsClipboard) Read() (Data, error) {
	c.mu.Lock()
	if err := openClipboardFast(); err != nil {
		c.mu.Unlock()
		return Data{}, err
	}
	clipboardOpen := true
	closeIfOpen := func() {
		if clipboardOpen {
			closeClipboard()
			clipboardOpen = false
		}
	}
	finish := func(data Data, err error) (Data, error) {
		closeIfOpen()
		c.mu.Unlock()
		return data, err
	}

	if remoteClipboardFormatID != 0 && isClipboardFormatAvailable(remoteClipboardFormatID) {
		text, err := readClipboardFormat(remoteClipboardFormatID)
		if err == nil {
			if marker, ok := protocol.DecodeRemoteMarker(text); ok {
				data := Data{Type: DataTypeRemote, Text: text, RemoteMarker: &marker}
				data.Fingerprint = Fingerprint(data)
				return finish(data, nil)
			}
		}
	}
	if isClipboardFormatAvailable(cfHDrop) {
		files, err := readFileDropList()
		if err == nil && len(files) > 0 {
			data := Data{Type: DataTypeFiles, Files: files}
			data.Fingerprint = Fingerprint(data)
			return finish(data, nil)
		}
	}
	dib, err := readClipboardImageRaw()
	if err == nil && len(dib) > 0 {
		closeIfOpen()
		c.mu.Unlock()
		imageData, mime, err := decodeClipboardDIBImage(dib)
		if err != nil {
			return Data{}, err
		}
		data := Data{Type: DataTypeImage, Image: imageData, ImageMIME: mime}
		data.Fingerprint = Fingerprint(data)
		return data, nil
	}
	text, err := readClipboardFormat(cfUnicodeText)
	if err != nil {
		return finish(Data{}, err)
	}
	if marker, ok := protocol.DecodeRemoteMarker(text); ok {
		data := Data{Type: DataTypeRemote, Text: text, RemoteMarker: &marker}
		data.Fingerprint = Fingerprint(data)
		return finish(data, nil)
	}
	data := Data{Type: DataTypeText, Text: text}
	data.Fingerprint = Fingerprint(data)
	return finish(data, nil)
}

func (c *windowsClipboard) Write(data Data) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ignoreDuration := 1500 * time.Millisecond
	if data.Type == DataTypeImage && len(data.Image) > 1024*1024 {
		ignoreDuration = 10 * time.Second
	}
	if err := openClipboard(0); err != nil {
		return err
	}
	defer func() {
		closeClipboard()
		if data.Type == DataTypeImage {
			formats, err := clipboardFormatListSnapshot()
			if err != nil {
				slog.Error("windows image clipboard verify failed", "error", err)
			} else {
				slog.Info("windows image clipboard formats after close", "formats", formats)
			}
		}
		c.ignoreSeq = clipboardSequenceNumber()
		c.ignoreUntil = time.Now().Add(ignoreDuration)
	}()
	if _, _, err := procEmptyClipboard.Call(); err != syscall.Errno(0) {
		return fmt.Errorf("empty clipboard: %w", err)
	}

	switch data.Type {
	case DataTypeText:
		return writeUnicodeText(cfUnicodeText, data.Text)
	case DataTypeRemote:
		if data.RemoteMarker == nil {
			return fmt.Errorf("remote marker is nil")
		}
		encoded, err := protocol.EncodeRemoteMarker(*data.RemoteMarker)
		if err != nil {
			return err
		}
		if err := writeUnicodeText(cfUnicodeText, encoded); err != nil {
			return err
		}
		if remoteClipboardFormatID != 0 {
			return writeUnicodeText(remoteClipboardFormatID, encoded)
		}
		return nil
	case DataTypeFiles:
		return writeFileDropList(data.Files)
	case DataTypeImage:
		return writeClipboardImage(data.Image, data.ImageMIME)
	default:
		return fmt.Errorf("unsupported clipboard data type: %s", data.Type)
	}
}

func (c *windowsClipboard) Watch(out chan Data) {
	go func() {
		notify := make(chan struct{}, 1)
		watchNotifyMu.Lock()
		watchNotify = notify
		watchNotifyMu.Unlock()

		go runClipboardMessageWindow()

		for range notify {
			time.Sleep(300 * time.Millisecond)
			if c.shouldIgnoreCurrentEvent() {
				continue
			}
			data, err := c.readWithShortRetry()
			if err != nil || data.Fingerprint == "" || data.Fingerprint == c.last {
				continue
			}
			c.last = data.Fingerprint
			out <- data
		}
	}()
}

func (c *windowsClipboard) shouldIgnoreCurrentEvent() bool {
	seq := clipboardSequenceNumber()
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.ignoreUntil) {
		return true
	}
	if seq != 0 && seq == c.ignoreSeq {
		c.lastSeq = seq
		c.ignoreSeq = 0
		return true
	}
	return false
}

func (c *windowsClipboard) readWithShortRetry() (Data, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		data, err := c.Read()
		if err == nil {
			return data, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return Data{}, lastErr
}

func runClipboardMessageWindow() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, _ := windows.UTF16PtrFromString("ClipboardSyncWatcher")
	hInstance, _, _ := procGetModuleHandle.Call(0)
	wc := wndclassex{
		CbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		LpfnWndProc:   wndProcCallback,
		HInstance:     hInstance,
		LpszClassName: className,
	}
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(className)),
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		hInstance,
		0,
	)
	if hwnd == 0 {
		return
	}
	clipboardOwnerMu.Lock()
	clipboardOwnerHwnd = hwnd
	clipboardOwnerMu.Unlock()
	if ret, _, _ := procAddClipboardListen.Call(hwnd); ret == 0 {
		return
	}
	var msg windowMessage
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func clipboardWndProc(hwnd uintptr, msg uint32, wParam uintptr, lParam uintptr) uintptr {
	switch msg {
	case wmClipboard:
		watchNotifyMu.Lock()
		notify := watchNotify
		watchNotifyMu.Unlock()
		if notify != nil {
			select {
			case notify <- struct{}{}:
			default:
			}
		}
		return 0
	case wmRenderFmt:
		if uint32(wParam) == cfDIB {
			renderDelayedDIB()
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	default:
		ret, _, _ := procDefWindowProc.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	}
}

func clipboardOwnerWindow() uintptr {
	for attempt := 0; attempt < 20; attempt++ {
		clipboardOwnerMu.Lock()
		hwnd := clipboardOwnerHwnd
		clipboardOwnerMu.Unlock()
		if hwnd != 0 {
			return hwnd
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

func openClipboard(owner uintptr) error {
	return openClipboardWithRetry("write", owner, 20, 100*time.Millisecond)
}

func openClipboardFast() error {
	return openClipboardWithRetry("read", 0, 1, 0)
}

func openClipboardWithRetry(operation string, owner uintptr, attempts int, delay time.Duration) error {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		ret, _, err := procOpenClipboard.Call(owner)
		if ret != 0 {
			return nil
		}
		lastErr = err
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("open clipboard %s: %w", operation, lastErr)
}

func closeClipboard() {
	procCloseClipboard.Call()
}

func clipboardSequenceNumber() uint32 {
	ret, _, _ := procGetClipboardSeq.Call()
	return uint32(ret)
}

func isClipboardFormatAvailable(format uint32) bool {
	ret, _, _ := procIsClipboardFormat.Call(uintptr(format))
	return ret != 0
}

func readClipboardFormat(format uint32) (string, error) {
	h, _, err := procGetClipboardData.Call(uintptr(format))
	if h == 0 {
		return "", fmt.Errorf("get clipboard data: %w", err)
	}
	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		return "", fmt.Errorf("lock clipboard data: %w", err)
	}
	defer procGlobalUnlock.Call(h)
	text := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(ptr)))
	return text, nil
}

func writeUnicodeText(format uint32, text string) error {
	utf16, err := windows.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("utf16 encode: %w", err)
	}
	size := uintptr(len(utf16) * 2)
	hMem, _, err := procGlobalAlloc.Call(gmemeMoveable, size)
	if hMem == 0 {
		return fmt.Errorf("global alloc: %w", err)
	}
	ptr, _, err := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return fmt.Errorf("global lock: %w", err)
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf16)), utf16)
	procGlobalUnlock.Call(hMem)
	ret, _, err := procSetClipboardData.Call(uintptr(format), hMem)
	if ret == 0 {
		return fmt.Errorf("set clipboard data: %w", err)
	}
	return nil
}

func readFileDropList() ([]string, error) {
	hDrop, _, err := procGetClipboardData.Call(cfHDrop)
	if hDrop == 0 {
		return nil, fmt.Errorf("get hdrop: %w", err)
	}
	count, _, _ := procDragQueryFile.Call(hDrop, 0xFFFFFFFF, 0, 0)
	files := make([]string, 0, count)
	for i := uint32(0); i < uint32(count); i++ {
		length, _, _ := procDragQueryFile.Call(hDrop, uintptr(i), 0, 0)
		buffer := make([]uint16, length+1)
		procDragQueryFile.Call(hDrop, uintptr(i), uintptr(unsafe.Pointer(&buffer[0])), uintptr(length+1))
		files = append(files, windows.UTF16ToString(buffer))
	}
	return files, nil
}

func writeFileDropList(files []string) error {
	if len(files) == 0 {
		return fmt.Errorf("no files to write")
	}
	for i, path := range files {
		files[i] = filepath.Clean(path)
	}
	payload := strings.Join(files, "\x00") + "\x00\x00"
	utf16, err := windows.UTF16FromString(payload)
	if err != nil {
		return fmt.Errorf("encode file list: %w", err)
	}
	header := dropfiles{PFiles: uint32(unsafe.Sizeof(dropfiles{})), Wide: 1}
	totalSize := uintptr(unsafe.Sizeof(header)) + uintptr(len(utf16)*2)
	hMem, _, err := procGlobalAlloc.Call(gmemeMoveable, totalSize)
	if hMem == 0 {
		return fmt.Errorf("global alloc hdrop: %w", err)
	}
	ptr, _, err := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return fmt.Errorf("global lock hdrop: %w", err)
	}
	*(*dropfiles)(unsafe.Pointer(ptr)) = header
	dataPtr := unsafe.Pointer(ptr + uintptr(unsafe.Sizeof(header)))
	copy(unsafe.Slice((*uint16)(dataPtr), len(utf16)), utf16)
	procGlobalUnlock.Call(hMem)
	ret, _, err := procSetClipboardData.Call(cfHDrop, hMem)
	if ret == 0 {
		return fmt.Errorf("set hdrop: %w", err)
	}
	return nil
}

func init() {
	_ = sha256.Size
	_ = hex.EncodedLen
}

func readClipboardImageRaw() ([]byte, error) {
	for _, format := range []uint32{cfDIBV5, cfDIB} {
		if !isClipboardFormatAvailable(format) {
			continue
		}
		dib, err := readClipboardBinary(format)
		if err != nil {
			return nil, err
		}
		return dib, nil
	}
	return nil, fmt.Errorf("no image format in clipboard")
}

func decodeClipboardDIBImage(dib []byte) ([]byte, string, error) {
	bmpPayload, err := dibToBMP(dib)
	if err != nil {
		return nil, "", err
	}
	img, err := bmp.Decode(bytes.NewReader(bmpPayload))
	if err != nil {
		return nil, "", fmt.Errorf("decode bmp from clipboard: %w", err)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, "", fmt.Errorf("encode png from clipboard image: %w", err)
	}
	return out.Bytes(), "image/png", nil
}

func writeClipboardImage(imageBytes []byte, mime string) error {
	if len(imageBytes) == 0 {
		return fmt.Errorf("image payload is empty")
	}
	img, err := decodeClipboardImagePayload(imageBytes, mime)
	if err != nil {
		return err
	}
	bounds := img.Bounds()
	slog.Info("windows image clipboard decode", "mime", mime, "payload_size", len(imageBytes), "width", bounds.Dx(), "height", bounds.Dy())
	var bmpBuffer bytes.Buffer
	if err := bmp.Encode(&bmpBuffer, img); err != nil {
		return fmt.Errorf("encode clipboard image bmp: %w", err)
	}
	dib, err := bmpToDIB(bmpBuffer.Bytes())
	if err != nil {
		return err
	}
	slog.Info("windows image clipboard dib prepared", append(dibDiagnostics(dib), "bmp_size", bmpBuffer.Len(), "dib_size", len(dib))...)
	if len(dib) > maxDIBBytes {
		return fmt.Errorf("image DIB is too large for immediate clipboard write: %d bytes", len(dib))
	}
	hMem, err := allocClipboardBytes(dib)
	if err != nil {
		return err
	}
	ret, _, callErr := procSetClipboardData.Call(cfDIB, hMem)
	if ret == 0 {
		return fmt.Errorf("set clipboard image: %w", callErr)
	}
	return nil
}

func setDelayedDIB(dib []byte) {
	delayedDIBMu.Lock()
	delayedDIB = append(delayedDIB[:0], dib...)
	delayedDIBMu.Unlock()
}

func renderDelayedDIB() {
	delayedDIBMu.Lock()
	dib := append([]byte(nil), delayedDIB...)
	delayedDIBMu.Unlock()
	if len(dib) == 0 {
		return
	}
	hMem, err := allocClipboardBytes(dib)
	if err != nil {
		return
	}
	procSetClipboardData.Call(cfDIB, hMem)
}

func imageDIBSize(img image.Image) int {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	stride := ((width*3 + 3) / 4) * 4
	return 40 + stride*height
}

func dibDiagnostics(dib []byte) []any {
	if len(dib) < 40 {
		return []any{"dib_error", "too_small", "dib_size", len(dib)}
	}
	return []any{
		"header_size", binary.LittleEndian.Uint32(dib[0:4]),
		"dib_width", int32(binary.LittleEndian.Uint32(dib[4:8])),
		"dib_height", int32(binary.LittleEndian.Uint32(dib[8:12])),
		"planes", binary.LittleEndian.Uint16(dib[12:14]),
		"bit_count", binary.LittleEndian.Uint16(dib[14:16]),
		"compression", binary.LittleEndian.Uint32(dib[16:20]),
		"size_image", binary.LittleEndian.Uint32(dib[20:24]),
	}
}

func clipboardFormatList() []string {
	var formats []string
	format := uintptr(0)
	for {
		next, _, _ := procEnumClipboardFmt.Call(format)
		if next == 0 {
			break
		}
		formats = append(formats, clipboardFormatName(uint32(next)))
		format = next
	}
	return formats
}

func clipboardFormatListSnapshot() ([]string, error) {
	if err := openClipboardWithRetry("verify", 0, 3, 50*time.Millisecond); err != nil {
		return nil, err
	}
	defer closeClipboard()
	return clipboardFormatList(), nil
}

func clipboardFormatName(format uint32) string {
	switch format {
	case cfUnicodeText:
		return "CF_UNICODETEXT(13)"
	case cfHDrop:
		return "CF_HDROP(15)"
	case cfBitmap:
		return "CF_BITMAP(2)"
	case cfDIB:
		return "CF_DIB(8)"
	case cfDIBV5:
		return "CF_DIBV5(17)"
	}
	buffer := make([]uint16, 128)
	ret, _, _ := procGetClipboardFmtName.Call(uintptr(format), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if ret == 0 {
		return fmt.Sprintf("FORMAT_%d", format)
	}
	return fmt.Sprintf("%s(%d)", windows.UTF16ToString(buffer[:int(ret)]), format)
}

func writeClipboardPNG(imageBytes []byte, img image.Image, mime string) error {
	if pngFormatID == 0 {
		return fmt.Errorf("png clipboard format is unavailable")
	}
	payload := imageBytes
	if !strings.EqualFold(mime, "image/png") || !isPNGPayload(imageBytes) {
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			return fmt.Errorf("encode large clipboard image png: %w", err)
		}
		payload = out.Bytes()
	}
	hMem, err := allocClipboardBytes(payload)
	if err != nil {
		return err
	}
	ret, _, callErr := procSetClipboardData.Call(uintptr(pngFormatID), hMem)
	if ret == 0 {
		return fmt.Errorf("set clipboard png image: %w", callErr)
	}
	return nil
}

func isPNGPayload(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
}

func decodeClipboardImagePayload(imageBytes []byte, mime string) (image.Image, error) {
	if strings.EqualFold(mime, "image/bmp") {
		img, err := bmp.Decode(bytes.NewReader(imageBytes))
		if err == nil {
			return img, nil
		}
	}
	img, err := png.Decode(bytes.NewReader(imageBytes))
	if err == nil {
		return img, nil
	}
	if bmpImg, bmpErr := bmp.Decode(bytes.NewReader(imageBytes)); bmpErr == nil {
		return bmpImg, nil
	}
	return nil, fmt.Errorf("decode image payload: %w", err)
}

func readClipboardBinary(format uint32) ([]byte, error) {
	h, _, err := procGetClipboardData.Call(uintptr(format))
	if h == 0 {
		return nil, fmt.Errorf("get clipboard binary data: %w", err)
	}
	size, _, _ := procGlobalSize.Call(h)
	if size == 0 {
		return nil, fmt.Errorf("clipboard binary size is zero")
	}
	ptr, _, err := procGlobalLock.Call(h)
	if ptr == 0 {
		return nil, fmt.Errorf("lock clipboard binary data: %w", err)
	}
	defer procGlobalUnlock.Call(h)
	data := make([]byte, int(size))
	copy(data, unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(size)))
	return data, nil
}

func allocClipboardBytes(data []byte) (uintptr, error) {
	hMem, _, err := procGlobalAlloc.Call(gmemeMoveable, uintptr(len(data)))
	if hMem == 0 {
		return 0, fmt.Errorf("global alloc clipboard bytes: %w", err)
	}
	ptr, _, err := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return 0, fmt.Errorf("global lock clipboard bytes: %w", err)
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data)), data)
	procGlobalUnlock.Call(hMem)
	return hMem, nil
}

func dibToBMP(dib []byte) ([]byte, error) {
	if len(dib) < 40 {
		return nil, fmt.Errorf("clipboard DIB is too small")
	}
	headerSize := binary.LittleEndian.Uint32(dib[0:4])
	if int(headerSize) > len(dib) || headerSize < 40 {
		return nil, fmt.Errorf("clipboard DIB header is invalid")
	}
	bitCount := binary.LittleEndian.Uint16(dib[14:16])
	compression := binary.LittleEndian.Uint32(dib[16:20])
	if compression != biRGB {
		return nil, fmt.Errorf("unsupported DIB compression: %d", compression)
	}
	colorTableSize := uint32(0)
	if bitCount <= 8 {
		colorsUsed := binary.LittleEndian.Uint32(dib[32:36])
		if colorsUsed == 0 {
			colorsUsed = 1 << bitCount
		}
		colorTableSize = colorsUsed * 4
	}
	pixelOffset := 14 + headerSize + colorTableSize
	fileSize := 14 + len(dib)
	bmpPayload := make([]byte, fileSize)
	bmpPayload[0] = 'B'
	bmpPayload[1] = 'M'
	binary.LittleEndian.PutUint32(bmpPayload[2:6], uint32(fileSize))
	binary.LittleEndian.PutUint32(bmpPayload[10:14], pixelOffset)
	copy(bmpPayload[14:], dib)
	return bmpPayload, nil
}

func bmpToDIB(bmpPayload []byte) ([]byte, error) {
	if len(bmpPayload) < 14 {
		return nil, fmt.Errorf("bmp payload is too small")
	}
	if bmpPayload[0] != 'B' || bmpPayload[1] != 'M' {
		return nil, fmt.Errorf("bmp payload signature is invalid")
	}
	return append([]byte(nil), bmpPayload[14:]...), nil
}

func imageToDIB24(img image.Image) []byte {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	stride := ((width*3 + 3) / 4) * 4
	pixelSize := stride * height
	dib := make([]byte, 40+pixelSize)
	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], uint32(width))
	binary.LittleEndian.PutUint32(dib[8:12], uint32(height))
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 24)
	binary.LittleEndian.PutUint32(dib[16:20], biRGB)
	binary.LittleEndian.PutUint32(dib[20:24], uint32(pixelSize))

	for y := 0; y < height; y++ {
		row := dib[40+(height-1-y)*stride:]
		for x := 0; x < width; x++ {
			r16, g16, b16, a16 := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			a := uint32(a16 >> 8)
			r := uint32(r16 >> 8)
			g := uint32(g16 >> 8)
			b := uint32(b16 >> 8)
			if a < 255 {
				r = (r*a + 255*(255-a)) / 255
				g = (g*a + 255*(255-a)) / 255
				b = (b*a + 255*(255-a)) / 255
			}
			offset := x * 3
			row[offset] = byte(b)
			row[offset+1] = byte(g)
			row[offset+2] = byte(r)
		}
	}
	return dib
}
