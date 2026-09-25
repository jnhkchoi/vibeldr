//go:build linux

// fb_linux.go puts the canvas out to a Linux framebuffer device.
//
// It opens /dev/fb0, asks it for the resolution and the pixel format, mmaps the
// whole of the screen memory and moves the canvas onto it. The pixel format
// differs from machine to machine - 32bpp BGRA is common, but 24bpp and 16bpp
// turn up - so the bit positions the device reports are read and used as given.
//
// With no framebuffer the open fails and the caller falls back to a text-only
// screen.
//
// fb_linux.go - 리눅스 프레임버퍼 장치로 캔버스를 내보낸다.
//
// /dev/fb0 을 열어 해상도와 픽셀 형식을 물어보고, 화면 메모리를 통째로
// mmap 해서 그 위에 캔버스를 옮긴다. 픽셀 형식은 기계마다 다르므로
// (32bpp BGRA 가 흔하지만 24bpp 나 16bpp 도 있다) 장치가 보고한 비트
// 위치를 그대로 읽어 변환한다.
//
// 프레임버퍼가 없으면 여는 단계에서 실패하고, 호출자는 글자만 쓰는
// 화면으로 되돌아간다.
package fbui

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The ioctl numbers, from linux/fb.h; they are fixed and do not vary by
// architecture. Only the two Get calls exist here - the mode GRUB left set is
// taken as it is and never changed.
//
// ioctl 번호. linux/fb.h 의 값이며 아키텍처와 무관하게 고정이다. Get 두 개만
// 둔다. GRUB 이 세워 둔 모드를 그대로 받아 쓰고 바꾸지 않는다.
const (
	fbioGetVScreenInfo = 0x4600
	fbioGetFScreenInfo = 0x4602
)

// fbBitfield is where one colour component sits inside a pixel.
// fbBitfield - 한 색 성분이 픽셀 안에서 차지하는 위치.
type fbBitfield struct {
	Offset   uint32
	Length   uint32
	MSBRight uint32
}

// fbVarScreenInfo is the changeable screen information. Its layout has to match
// struct fb_var_screeninfo in linux/fb.h.
//
// fbVarScreenInfo - 바꿀 수 있는 화면 정보. linux/fb.h 의
// struct fb_var_screeninfo 와 같은 배치여야 한다.
type fbVarScreenInfo struct {
	Xres, Yres               uint32
	XresVirtual, YresVirtual uint32
	Xoffset, Yoffset         uint32
	BitsPerPixel             uint32
	Grayscale                uint32
	Red, Green, Blue, Transp fbBitfield
	Nonstd                   uint32
	Activate                 uint32
	Height, Width            uint32
	AccelFlags               uint32
	Pixclock                 uint32
	LeftMargin, RightMargin  uint32
	UpperMargin, LowerMargin uint32
	HsyncLen, VsyncLen       uint32
	Sync, Vmode, Rotate      uint32
	Colorspace               uint32
	Reserved                 [4]uint32
}

// fbFixScreenInfo is the fixed screen information. What is needed from it here
// is the bytes per row (LineLength) and the size of the screen memory.
//
// fbFixScreenInfo - 바꿀 수 없는 화면 정보. 여기서 필요한 것은 한 행의
// 바이트 수(LineLength) 와 화면 메모리 크기다.
type fbFixScreenInfo struct {
	ID         [16]byte
	SmemStart  uint64
	SmemLen    uint32
	Type       uint32
	TypeAux    uint32
	Visual     uint32
	Xpanstep   uint16
	Ypanstep   uint16
	Ywrapstep  uint16
	_          uint16
	LineLength uint32
	MmioStart  uint64
	MmioLen    uint32
	Accel      uint32
	Caps       uint16
	Reserved   [2]uint16
	_          uint16
}

// Framebuffer is an open framebuffer device.
// Framebuffer - 열린 프레임버퍼 장치.
type Framebuffer struct {
	f   *os.File
	mem []byte

	// W, H is the screen size in pixels.
	// W, H - 화면 크기(픽셀).
	W, H int
	// bpp is bits per pixel; 32, 24 and 16 are handled.
	// bpp - 픽셀당 비트. 32, 24, 16 을 다룬다.
	bpp int
	// stride is the bytes per row, which can be more than width times pixel size.
	// stride - 한 행의 바이트 수. 화면 폭 * 픽셀 크기보다 클 수 있다.
	stride int

	// The bit position of each component, exactly as the device reported it.
	// 각 성분의 비트 위치. 장치가 보고한 값 그대로.
	rOff, rLen uint32
	gOff, gLen uint32
	bOff, bLen uint32
}

// OpenFramebuffer opens the framebuffer device; an empty path means /dev/fb0.
// OpenFramebuffer - 프레임버퍼 장치를 연다. path 가 비면 /dev/fb0.
func OpenFramebuffer(path string) (*Framebuffer, error) {
	if path == "" {
		path = "/dev/fb0"
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("fbui: %s 열기: %w", path, err)
	}

	var vi fbVarScreenInfo
	if err := ioctlPtr(f.Fd(), fbioGetVScreenInfo, unsafe.Pointer(&vi)); err != nil {
		f.Close()
		return nil, fmt.Errorf("fbui: %s 화면 정보: %w", path, err)
	}
	var fi fbFixScreenInfo
	if err := ioctlPtr(f.Fd(), fbioGetFScreenInfo, unsafe.Pointer(&fi)); err != nil {
		f.Close()
		return nil, fmt.Errorf("fbui: %s 고정 정보: %w", path, err)
	}

	switch vi.BitsPerPixel {
	case 16, 24, 32:
	default:
		f.Close()
		return nil, fmt.Errorf("fbui: %s 는 %d bpp 라 다루지 않음", path, vi.BitsPerPixel)
	}
	if vi.Xres == 0 || vi.Yres == 0 {
		f.Close()
		return nil, fmt.Errorf("fbui: %s 해상도가 %dx%d", path, vi.Xres, vi.Yres)
	}

	size := int(fi.SmemLen)
	if size <= 0 {
		size = int(fi.LineLength) * int(vi.Yres)
	}
	mem, err := unix.Mmap(int(f.Fd()), 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("fbui: %s mmap(%d): %w", path, size, err)
	}

	fb := &Framebuffer{
		f: f, mem: mem,
		W: int(vi.Xres), H: int(vi.Yres),
		bpp:    int(vi.BitsPerPixel),
		stride: int(fi.LineLength),
		rOff:   vi.Red.Offset, rLen: vi.Red.Length,
		gOff: vi.Green.Offset, gLen: vi.Green.Length,
		bOff: vi.Blue.Offset, bLen: vi.Blue.Length,
	}
	if fb.stride == 0 {
		fb.stride = fb.W * fb.bpp / 8
	}
	// Some devices report every component length as 0. Left alone that makes
	// every colour black, so the usual values for the bpp go in instead.
	//
	// 장치가 성분 길이를 0 으로 보고하는 경우가 있다. 그대로 두면 색이
	// 전부 검정이 되므로 bpp 에 맞는 통상값으로 채운다.
	if fb.rLen == 0 && fb.gLen == 0 && fb.bLen == 0 {
		if fb.bpp == 16 {
			fb.rOff, fb.rLen = 11, 5
			fb.gOff, fb.gLen = 5, 6
			fb.bOff, fb.bLen = 0, 5
		} else {
			fb.rOff, fb.rLen = 16, 8
			fb.gOff, fb.gLen = 8, 8
			fb.bOff, fb.bLen = 0, 8
		}
	}
	return fb, nil
}

// Size is the screen size.
// Size - 화면 크기.
func (fb *Framebuffer) Size() (int, int) { return fb.W, fb.H }

// Info is a one-line human-readable summary, for the log.
// Info - 사람이 읽을 수 있는 한 줄 요약. 로그에 남긴다.
func (fb *Framebuffer) Info() string {
	return fmt.Sprintf("%dx%d %dbpp stride=%d r=%d/%d g=%d/%d b=%d/%d",
		fb.W, fb.H, fb.bpp, fb.stride,
		fb.rOff, fb.rLen, fb.gOff, fb.gLen, fb.bOff, fb.bLen)
}

// NewCanvas is a canvas the size of this screen.
// NewCanvas - 이 화면 크기에 맞는 캔버스.
func (fb *Framebuffer) NewCanvas() *Canvas { return NewCanvas(fb.W, fb.H) }

// pack turns one colour into this device's pixel value.
// pack - 색 하나를 이 장치의 픽셀 값으로.
func (fb *Framebuffer) pack(r, g, b uint8) uint32 {
	return uint32(r)>>(8-fb.rLen)<<fb.rOff |
		uint32(g)>>(8-fb.gLen)<<fb.gOff |
		uint32(b)>>(8-fb.bLen)<<fb.bOff
}

// Flush moves the whole canvas to the screen.
// Flush - 캔버스 전체를 화면으로 옮긴다.
func (fb *Framebuffer) Flush(c *Canvas) {
	fb.FlushRect(c, Rect{0, 0, c.W, c.H})
}

// FlushRect moves one area of the canvas. Moving the whole screen every time is
// slow, so this is used whenever only part of it changed.
//
// FlushRect - 캔버스의 한 영역만 옮긴다. 화면 전체를 매번 옮기면 느려서,
// 바뀐 곳만 알 때 쓴다.
func (fb *Framebuffer) FlushRect(c *Canvas, r Rect) {
	r = intersect(r, Rect{0, 0, c.W, c.H})
	r = intersect(r, Rect{0, 0, fb.W, fb.H})
	if r.W <= 0 || r.H <= 0 {
		return
	}
	pixBytes := fb.bpp / 8
	for y := r.Y; y < r.Y+r.H; y++ {
		src := y*c.Stride + r.X*4
		dst := y*fb.stride + r.X*pixBytes
		for x := 0; x < r.W; x++ {
			v := fb.pack(c.Pix[src], c.Pix[src+1], c.Pix[src+2])
			switch fb.bpp {
			case 32:
				fb.mem[dst] = byte(v)
				fb.mem[dst+1] = byte(v >> 8)
				fb.mem[dst+2] = byte(v >> 16)
				fb.mem[dst+3] = byte(v >> 24)
			case 24:
				fb.mem[dst] = byte(v)
				fb.mem[dst+1] = byte(v >> 8)
				fb.mem[dst+2] = byte(v >> 16)
			case 16:
				fb.mem[dst] = byte(v)
				fb.mem[dst+1] = byte(v >> 8)
			}
			src += 4
			dst += pixBytes
		}
	}
}

// Close unmaps the memory and closes the device.
// Close - mmap 을 풀고 장치를 닫는다.
func (fb *Framebuffer) Close() error {
	if fb.mem != nil {
		unix.Munmap(fb.mem)
		fb.mem = nil
	}
	if fb.f != nil {
		err := fb.f.Close()
		fb.f = nil
		return err
	}
	return nil
}

// ioctlPtr is an ioctl that passes a pointer to a struct.
// ioctlPtr - 구조체 포인터를 넘기는 ioctl.
func ioctlPtr(fd uintptr, req uintptr, p unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(p))
	if errno != 0 {
		return errno
	}
	return nil
}
