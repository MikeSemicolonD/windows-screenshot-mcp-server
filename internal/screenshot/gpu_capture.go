package screenshot

// GPU-accelerated window capture via the Windows.Graphics.Capture (WGC) API.
//
// WGC captures a window through the Desktop Window Manager's GPU compositor,
// which reliably reaches DirectComposition / hardware-rendered content that the
// GDI paths (BitBlt, PrintWindow) can miss or render incorrectly. It requires
// Windows 10 1803 or newer.
//
// The implementation is pure Go: it drives the WinRT activation factories and
// Direct3D 11 COM interfaces directly through their vtables, matching the rest
// of this package (no cgo). A one-shot screenshot is taken by creating a
// free-threaded frame pool and polling TryGetNextFrame, which avoids having to
// implement a WinRT event delegate.

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/screenshot-mcp-server/pkg/types"
	"golang.org/x/sys/windows"
)

var (
	combase = windows.NewLazyDLL("combase.dll")
	d3d11   = windows.NewLazyDLL("d3d11.dll")

	roInitialize           = combase.NewProc("RoInitialize")
	roGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	windowsCreateString    = combase.NewProc("WindowsCreateString")
	windowsDeleteString    = combase.NewProc("WindowsDeleteString")

	d3d11CreateDevice                    = d3d11.NewProc("D3D11CreateDevice")
	createDirect3D11DeviceFromDXGIDevice = d3d11.NewProc("CreateDirect3D11DeviceFromDXGIDevice")
)

const (
	roInitMultiThreaded = 1
	rpcEChangedMode     = 0x80010106 // RoInitialize already called with another mode

	// DirectXPixelFormat.B8G8R8A8UIntNormalized — matches our BGRA32 buffers.
	pixelFormatBGRA8 = 87

	d3dDriverHardware = 1    // D3D_DRIVER_TYPE_HARDWARE
	d3dDriverWARP     = 5    // D3D_DRIVER_TYPE_WARP (software fallback)
	d3d11SDKVersion   = 7    // D3D11_SDK_VERSION
	d3d11BGRASupport  = 0x20 // D3D11_CREATE_DEVICE_BGRA_SUPPORT

	d3d11UsageStaging  = 3       // D3D11_USAGE_STAGING
	d3d11CPUAccessRead = 0x20000 // D3D11_CPU_ACCESS_READ
	d3d11MapRead       = 1       // D3D11_MAP_READ

	ptrSize = unsafe.Sizeof(uintptr(0))
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic("gpu_capture: invalid GUID " + s + ": " + err.Error())
	}
	return g
}

var (
	iidGraphicsCaptureItem        = mustGUID("{79C3F95B-31F7-4EC2-A464-632EF5D30760}")
	iidGraphicsCaptureItemInterop = mustGUID("{3628E81B-3CAC-4C60-B7F4-23CE0E0C3356}")
	iidFramePoolStatics2          = mustGUID("{589B103F-6BBC-5DF5-A991-02E28B3B66D5}")
	iidDxgiInterfaceAccess        = mustGUID("{A9B3D012-3DF2-4EE3-B8D1-8695F457D3C1}")
	iidTexture2D                  = mustGUID("{6F15AAF2-D208-4E89-9AB4-489535D34F9C}")
	iidDXGIDevice                 = mustGUID("{54EC77FA-1377-44E6-8C32-88FD5F44C84C}")
	iidClosable                   = mustGUID("{30D5A829-7FA4-4026-83BB-D75BAE4EA99E}")
	iidCaptureSession2            = mustGUID("{2C39AE40-7D2E-5044-804E-8B6799D4CF9E}")
	iidCaptureSession3            = mustGUID("{F2CDD966-22AE-5EA1-9596-3A289344C3BE}")
)

// sizeInt32 mirrors Windows.Graphics.SizeInt32.
type sizeInt32 struct{ Width, Height int32 }

// d3dTexture2DDesc mirrors D3D11_TEXTURE2D_DESC (SampleDesc inlined).
type d3dTexture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

// d3dMappedSubresource mirrors D3D11_MAPPED_SUBRESOURCE.
type d3dMappedSubresource struct {
	PData      uintptr
	RowPitch   uint32
	DepthPitch uint32
}

// comCall invokes method idx on a COM/WinRT interface pointer via its vtable.
func comCall(this uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(this))
	method := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*ptrSize))
	ret, _, _ := syscall.SyscallN(method, append([]uintptr{this}, args...)...)
	return ret
}

// comRelease calls IUnknown::Release.
func comRelease(this uintptr) {
	if this != 0 {
		comCall(this, 2)
	}
}

// comClose calls IClosable::Close on an object that implements it, then
// releases the IClosable interface. Closing frees GPU resources promptly.
func comClose(this uintptr) {
	if this == 0 {
		return
	}
	var closable uintptr
	hr := comCall(this, 0, uintptr(unsafe.Pointer(&iidClosable)), uintptr(unsafe.Pointer(&closable)))
	if !failed(hr) && closable != 0 {
		comCall(closable, 6) // IClosable::Close
		comCall(closable, 2) // Release
	}
}

// failed reports whether an HRESULT indicates failure.
func failed(hr uintptr) bool { return int32(hr) < 0 }

// newHString creates a WinRT HSTRING. The caller must WindowsDeleteString it.
func newHString(s string) (uintptr, error) {
	u16, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var hs uintptr
	hr, _, _ := windowsCreateString.Call(
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(len(u16)-1), // length excludes the terminating null
		uintptr(unsafe.Pointer(&hs)),
	)
	if failed(hr) {
		return 0, fmt.Errorf("WindowsCreateString failed: 0x%08X", uint32(hr))
	}
	return hs, nil
}

// activationFactory resolves a WinRT activation factory for the given runtime
// class, returning the requested interface.
func activationFactory(classID string, iid *windows.GUID) (uintptr, error) {
	hs, err := newHString(classID)
	if err != nil {
		return 0, err
	}
	defer windowsDeleteString.Call(hs)

	var factory uintptr
	hr, _, _ := roGetActivationFactory.Call(
		hs,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if failed(hr) {
		return 0, fmt.Errorf("RoGetActivationFactory(%s) failed: 0x%08X", classID, uint32(hr))
	}
	return factory, nil
}

// createD3DDevice creates a Direct3D 11 device, preferring a hardware adapter
// and falling back to the WARP software renderer.
func createD3DDevice() (device, context uintptr, err error) {
	for _, driverType := range []uintptr{d3dDriverHardware, d3dDriverWARP} {
		var dev, ctx uintptr
		hr, _, _ := d3d11CreateDevice.Call(
			0,                         // pAdapter
			driverType,                // DriverType
			0,                         // Software module
			uintptr(d3d11BGRASupport), // Flags
			0,                         // pFeatureLevels
			0,                         // FeatureLevels count
			uintptr(d3d11SDKVersion),  // SDKVersion
			uintptr(unsafe.Pointer(&dev)),
			0, // pFeatureLevel (out, unused)
			uintptr(unsafe.Pointer(&ctx)),
		)
		if !failed(hr) {
			return dev, ctx, nil
		}
		err = fmt.Errorf("D3D11CreateDevice failed: 0x%08X", uint32(hr))
	}
	return 0, 0, err
}

// gpuSession is a reusable Windows.Graphics.Capture pipeline. The Direct3D
// device, frame pool, and capture session are created once and kept alive so a
// burst of captures pays the (large) setup cost only on the first frame. All
// methods must run on the goroutine that created the session, because WinRT
// requires the calls to stay on the single OS thread locked at creation.
type gpuSession struct {
	device    uintptr
	context   uintptr
	item      uintptr
	framePool uintptr
	session   uintptr
}

// CaptureGPU captures a window using the Windows.Graphics.Capture API. handle
// must be a top-level window handle. The capture is GPU-composited by the
// Desktop Window Manager, so it reliably reproduces DirectComposition and
// hardware-rendered content. Requires Windows 10 1803 or newer.
func (e *WindowsScreenshotEngine) CaptureGPU(handle uintptr, options *types.CaptureOptions) (*types.ScreenshotBuffer, error) {
	sess, err := e.NewGPUSession(handle, options)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	buffer, err := sess.Capture()
	if err != nil {
		return nil, err
	}
	if info, infoErr := e.getWindowInfo(handle); infoErr == nil {
		buffer.WindowInfo = *info
	}
	return buffer, nil
}

// NewGPUSession builds the full WGC pipeline for a window and starts capturing.
// The returned session must be Close()d on the same goroutine. It locks the OS
// thread for its lifetime, since WinRT calls cannot migrate between threads.
func (e *WindowsScreenshotEngine) NewGPUSession(handle uintptr, options *types.CaptureOptions) (types.GPUCaptureSession, error) {
	if handle == 0 {
		return nil, fmt.Errorf("invalid window handle")
	}
	if options == nil {
		options = types.DefaultCaptureOptions()
	}

	// WinRT calls must stay on a single, initialized OS thread.
	runtime.LockOSThread()

	s := &gpuSession{}
	// On any setup failure, release whatever we acquired and unlock the thread.
	fail := func(err error) (types.GPUCaptureSession, error) {
		s.releaseAll()
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("Windows.Graphics.Capture failed: %w", err)
	}

	if hr, _, _ := roInitialize.Call(uintptr(roInitMultiThreaded)); failed(hr) && uint32(hr) != rpcEChangedMode {
		return fail(fmt.Errorf("RoInitialize failed: 0x%08X", uint32(hr)))
	}

	// --- Direct3D 11 device ---
	device, context, err := createD3DDevice()
	if err != nil {
		return fail(err)
	}
	s.device, s.context = device, context

	var dxgiDevice uintptr
	if hr := comCall(device, 0, uintptr(unsafe.Pointer(&iidDXGIDevice)), uintptr(unsafe.Pointer(&dxgiDevice))); failed(hr) {
		return fail(fmt.Errorf("QueryInterface(IDXGIDevice) failed: 0x%08X", uint32(hr)))
	}
	defer comRelease(dxgiDevice)

	// Wrap the DXGI device as a WinRT IDirect3DDevice for the frame pool.
	var rtDevice uintptr
	if hr, _, _ := createDirect3D11DeviceFromDXGIDevice.Call(dxgiDevice, uintptr(unsafe.Pointer(&rtDevice))); failed(hr) {
		return fail(fmt.Errorf("CreateDirect3D11DeviceFromDXGIDevice failed: 0x%08X", uint32(hr)))
	}
	defer comRelease(rtDevice)

	// --- GraphicsCaptureItem for the target window ---
	interop, err := activationFactory("Windows.Graphics.Capture.GraphicsCaptureItem", &iidGraphicsCaptureItemInterop)
	if err != nil {
		return fail(err)
	}
	defer comRelease(interop)

	// IGraphicsCaptureItemInterop::CreateForWindow (vtable index 3).
	if hr := comCall(interop, 3, handle, uintptr(unsafe.Pointer(&iidGraphicsCaptureItem)), uintptr(unsafe.Pointer(&s.item))); failed(hr) {
		return fail(fmt.Errorf("CreateForWindow failed (window may not be capturable): 0x%08X", uint32(hr)))
	}

	var size sizeInt32
	// IGraphicsCaptureItem::get_Size (vtable index 7).
	if hr := comCall(s.item, 7, uintptr(unsafe.Pointer(&size))); failed(hr) {
		return fail(fmt.Errorf("GraphicsCaptureItem.Size failed: 0x%08X", uint32(hr)))
	}
	if size.Width <= 0 || size.Height <= 0 {
		return fail(fmt.Errorf("capture item reported an empty size (%dx%d)", size.Width, size.Height))
	}

	// --- Free-threaded frame pool + capture session ---
	statics2, err := activationFactory("Windows.Graphics.Capture.Direct3D11CaptureFramePool", &iidFramePoolStatics2)
	if err != nil {
		return fail(err)
	}
	defer comRelease(statics2)

	// SizeInt32 is an 8-byte struct, passed by value in one register.
	sizeArg := uintptr(uint32(size.Width)) | uintptr(uint32(size.Height))<<32
	// IDirect3D11CaptureFramePoolStatics2::CreateFreeThreaded (vtable index 6).
	if hr := comCall(statics2, 6, rtDevice, uintptr(pixelFormatBGRA8), 2, sizeArg, uintptr(unsafe.Pointer(&s.framePool))); failed(hr) {
		return fail(fmt.Errorf("CreateFreeThreaded frame pool failed: 0x%08X", uint32(hr)))
	}

	// IDirect3D11CaptureFramePool::CreateCaptureSession (vtable index 10).
	if hr := comCall(s.framePool, 10, s.item, uintptr(unsafe.Pointer(&s.session))); failed(hr) {
		return fail(fmt.Errorf("CreateCaptureSession failed: 0x%08X", uint32(hr)))
	}

	configureSession(s.session, options)

	// IGraphicsCaptureSession::StartCapture (vtable index 6).
	if hr := comCall(s.session, 6); failed(hr) {
		return fail(fmt.Errorf("StartCapture failed: 0x%08X", uint32(hr)))
	}

	return s, nil
}

// Capture grabs the latest composited frame and returns it as a BGRA32 buffer.
func (s *gpuSession) Capture() (*types.ScreenshotBuffer, error) {
	frame, err := waitForFrame(s.framePool)
	if err != nil {
		return nil, fmt.Errorf("Windows.Graphics.Capture failed: %w", err)
	}
	defer comRelease(frame)
	defer comClose(frame)

	// IDirect3D11CaptureFrame::get_Surface (vtable index 6).
	var surface uintptr
	if hr := comCall(frame, 6, uintptr(unsafe.Pointer(&surface))); failed(hr) {
		return nil, fmt.Errorf("CaptureFrame.Surface failed: 0x%08X", uint32(hr))
	}
	defer comRelease(surface)

	// IDirect3DSurface -> IDirect3DDxgiInterfaceAccess -> ID3D11Texture2D.
	var access uintptr
	if hr := comCall(surface, 0, uintptr(unsafe.Pointer(&iidDxgiInterfaceAccess)), uintptr(unsafe.Pointer(&access))); failed(hr) {
		return nil, fmt.Errorf("QueryInterface(IDirect3DDxgiInterfaceAccess) failed: 0x%08X", uint32(hr))
	}
	defer comRelease(access)

	var texture uintptr
	// IDirect3DDxgiInterfaceAccess::GetInterface (vtable index 3).
	if hr := comCall(access, 3, uintptr(unsafe.Pointer(&iidTexture2D)), uintptr(unsafe.Pointer(&texture))); failed(hr) {
		return nil, fmt.Errorf("GetInterface(ID3D11Texture2D) failed: 0x%08X", uint32(hr))
	}
	defer comRelease(texture)

	buffer, err := readTexture(s.device, s.context, texture)
	if err != nil {
		return nil, err
	}
	buffer.Timestamp = time.Now()
	return buffer, nil
}

// Close releases the GPU resources and unlocks the capture thread.
func (s *gpuSession) Close() error {
	s.releaseAll()
	runtime.UnlockOSThread()
	return nil
}

// releaseAll frees every COM resource the session holds, in reverse order of
// acquisition. It is safe to call with partially-initialised fields.
func (s *gpuSession) releaseAll() {
	comClose(s.session)
	comRelease(s.session)
	comClose(s.framePool)
	comRelease(s.framePool)
	comRelease(s.item)
	comRelease(s.context)
	comRelease(s.device)
	*s = gpuSession{}
}

// waitForFrame polls the frame pool until a frame is available or it times out.
func waitForFrame(framePool uintptr) (uintptr, error) {
	deadline := time.Now().Add(2500 * time.Millisecond)
	for {
		var frame uintptr
		// IDirect3D11CaptureFramePool::TryGetNextFrame (vtable index 7).
		if hr := comCall(framePool, 7, uintptr(unsafe.Pointer(&frame))); failed(hr) {
			return 0, fmt.Errorf("TryGetNextFrame failed: 0x%08X", uint32(hr))
		}
		if frame != 0 {
			return frame, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timed out waiting for a capture frame")
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// configureSession applies best-effort session tweaks. Both interfaces are
// version-gated, so a missing one is silently ignored.
func configureSession(session uintptr, options *types.CaptureOptions) {
	// IGraphicsCaptureSession2 (Windows 10 2004+): cursor capture toggle.
	var s2 uintptr
	if hr := comCall(session, 0, uintptr(unsafe.Pointer(&iidCaptureSession2)), uintptr(unsafe.Pointer(&s2))); !failed(hr) && s2 != 0 {
		cursor := uintptr(0)
		if options.IncludeCursor {
			cursor = 1
		}
		comCall(s2, 7, cursor) // put_IsCursorCaptureEnabled
		comRelease(s2)
	}
	// IGraphicsCaptureSession3 (Windows 11): drop the capture border.
	var s3 uintptr
	if hr := comCall(session, 0, uintptr(unsafe.Pointer(&iidCaptureSession3)), uintptr(unsafe.Pointer(&s3))); !failed(hr) && s3 != 0 {
		comCall(s3, 7, 0) // put_IsBorderRequired(false)
		comRelease(s3)
	}
}

// readTexture copies a GPU texture into a CPU-readable staging texture and
// returns its pixels as a top-down BGRA32 buffer.
func readTexture(device, context, texture uintptr) (*types.ScreenshotBuffer, error) {
	var desc d3dTexture2DDesc
	comCall(texture, 10, uintptr(unsafe.Pointer(&desc))) // ID3D11Texture2D::GetDesc

	staging := desc
	staging.MipLevels = 1
	staging.ArraySize = 1
	staging.SampleCount = 1
	staging.SampleQuality = 0
	staging.Usage = d3d11UsageStaging
	staging.BindFlags = 0
	staging.CPUAccessFlags = d3d11CPUAccessRead
	staging.MiscFlags = 0

	var stagingTex uintptr
	// ID3D11Device::CreateTexture2D (vtable index 5).
	if hr := comCall(device, 5, uintptr(unsafe.Pointer(&staging)), 0, uintptr(unsafe.Pointer(&stagingTex))); failed(hr) {
		return nil, fmt.Errorf("CreateTexture2D(staging) failed: 0x%08X", uint32(hr))
	}
	defer comRelease(stagingTex)

	comCall(context, 47, stagingTex, texture) // ID3D11DeviceContext::CopyResource

	var mapped d3dMappedSubresource
	// ID3D11DeviceContext::Map (vtable index 14).
	if hr := comCall(context, 14, stagingTex, 0, uintptr(d3d11MapRead), 0, uintptr(unsafe.Pointer(&mapped))); failed(hr) {
		return nil, fmt.Errorf("Map(staging texture) failed: 0x%08X", uint32(hr))
	}
	defer comCall(context, 15, stagingTex, 0) // ID3D11DeviceContext::Unmap

	width := int(desc.Width)
	height := int(desc.Height)
	if width <= 0 || height <= 0 || mapped.PData == 0 {
		return nil, fmt.Errorf("captured texture is empty (%dx%d)", width, height)
	}

	stride := width * 4
	data := make([]byte, stride*height)
	srcPitch := int(mapped.RowPitch) // GPU row pitch can exceed width*4
	for y := 0; y < height; y++ {
		row := (*[1 << 30]byte)(unsafe.Pointer(mapped.PData + uintptr(y*srcPitch)))[:stride:stride]
		copy(data[y*stride:(y+1)*stride], row)
	}

	return &types.ScreenshotBuffer{
		Data:       data,
		Width:      width,
		Height:     height,
		Stride:     stride,
		Format:     "BGRA32",
		DPI:        96,
		SourceRect: types.Rectangle{Width: width, Height: height},
	}, nil
}
