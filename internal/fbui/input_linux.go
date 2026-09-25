//go:build linux

// input_linux.go reads the mouse and the keyboard from /dev/input/event*.
//
// Which device is the mouse and which the keyboard is not decided up front.
// They are all opened and the kind of event that arrives decides. Device setups
// differ from machine to machine - a USB tablet, a PS/2 mouse - and choosing by
// name misses some of them.
//
// Both mouse conventions are accepted. A tablet gives absolute coordinates,
// which are scaled straight into screen coordinates; an ordinary mouse gives a
// relative movement, which is added to the cursor position.
//
// Each device is read by its own goroutine, so everything a device carries
// between its events - the pending absolute coordinates and the axis ranges -
// belongs to that device and to nothing else. A wireless receiver registers
// two or three event nodes at once, so shared state here is both a data race
// and a way for one node to consume another's half-finished batch.
//
// input_linux.go - /dev/input/event* 에서 마우스와 키보드를 읽는다.
//
// 어느 장치가 마우스이고 어느 것이 키보드인지 미리 고르지 않는다. 전부
// 열어놓고 들어오는 이벤트의 종류로 판단한다. 환경마다 장치 구성이 달라
// (USB 태블릿, PS/2 마우스 등) 이름으로 고르면 놓치는 경우가 생긴다.
//
// 마우스는 두 방식을 다 받는다. 태블릿은 절대좌표를 주므로 화면 좌표로
// 바로 환산하고, 일반 마우스는 상대 이동량을 주므로 커서 위치에 더한다.
//
// 장치마다 고루틴이 하나씩 붙으므로, 이벤트 사이에 들고 있는 값(모으는
// 중인 절대좌표, 축 범위)은 그 장치만의 것이다. 무선 리시버 하나가
// event 노드를 둘셋 만들기 때문에, 여기서 상태를 공유하면 자료 경합이
// 나고 한 노드가 다른 노드의 덜 끝난 묶음을 가로챈다.
package fbui

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The Linux input event types and codes, from linux/input-event-codes.h.
// 리눅스 input 이벤트 종류와 코드. linux/input-event-codes.h 의 값.
const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	relX     = 0x00
	relY     = 0x01
	relWheel = 0x08

	absX = 0x00
	absY = 0x01

	btnLeft  = 0x110
	btnTouch = 0x14a
)

// inputEventSize is the size of struct input_event: 24 bytes on 64-bit.
// inputEventSize - struct input_event 의 크기 (64 비트 기준 24 바이트).
const inputEventSize = 24

// absInfo is the axis information EVIOCGABS returns.
// absInfo - EVIOCGABS 가 돌려주는 축 정보.
type absInfo struct {
	Value, Minimum, Maximum, Fuzz, Flat, Resolution int32
}

// eviocgabs is the ioctl number for asking an axis's range.
// eviocgabs - 축 abs 의 범위를 묻는 ioctl 번호.
func eviocgabs(abs int) uintptr {
	// _IOR('E', 0x40+abs, struct input_absinfo)
	const iocRead = 2
	size := uint32(unsafe.Sizeof(absInfo{}))
	return uintptr(iocRead<<30 | uint32(size)<<16 | uint32('E')<<8 | uint32(0x40+abs))
}

// inputDevice is one open event device and the state that only it owns.
//
// A batch of events ends at SYN, so absolute coordinates have to be held until
// then. Held on the reader instead, a second device's SYN would clear the flag
// and the first device's move would be thrown away - and two goroutines would
// be writing the same map. A Logitech receiver registers a mouse node, a
// keyboard node and a consumer-control node, which is enough for that to
// happen on an ordinary desktop.
//
// inputDevice - 열린 event 장치 하나와, 그 장치만 갖는 상태.
//
// 이벤트 묶음은 SYN 에서 끝나므로 절대좌표는 그때까지 들고 있어야 한다.
// 이것을 리더에 두면 다른 장치의 SYN 이 플래그를 지워 먼저 장치의 이동이
// 버려지고, 고루틴 둘이 같은 맵에 쓰게 된다. 로지텍 리시버는 마우스·키보드·
// 컨슈머컨트롤 노드를 한꺼번에 만들므로 평범한 데스크톱에서도 그 일이 난다.
type inputDevice struct {
	f *os.File
	// absRange is this device's X and Y ranges; hasRange says it is absolute.
	// absRange - 이 장치의 X, Y 범위. hasRange 가 절대좌표 장치인지 알려준다.
	absRange [2]absInfo
	hasRange bool
	// pending is the absolute coordinates gathered so far in this batch.
	// pending - 이번 묶음에서 여태 모은 절대좌표.
	pending [2]int32
	// hasAbs says absolute coordinates arrived in this batch.
	// hasAbs - 이번 묶음에 절대좌표가 왔는지.
	hasAbs bool
	// rel is the relative movement gathered so far in this batch, and hasRel
	// says some arrived.
	//
	// X and Y come as two separate events and the batch ends at SYN, so they
	// have to be added up and applied together. Moving on each one on its own
	// would make a diagonal drag a staircase of horizontal and vertical steps,
	// emit two events where one will do, and leave the speed of the movement
	// unknowable, since neither half is the whole.
	//
	// rel - 이번 묶음에서 여태 모은 상대 이동량. hasRel 은 그게 왔는지.
	//
	// X 와 Y 는 따로 오고 묶음은 SYN 에서 끝나므로, 합쳐서 한 번에 적용해야
	// 한다. 하나씩 바로 옮기면 대각선 끌기가 가로·세로 계단이 되고,
	// 한 번이면 될 이벤트를 두 번 내보내며, 어느 쪽도 전체가 아니라서 이동
	// 속도를 알 수 없게 된다.
	rel    [2]int32
	hasRel bool
}

// InputReader is where events from the open input devices are gathered.
// InputReader - 열린 입력 장치들에서 이벤트를 모아 주는 곳.
type InputReader struct {
	devs []*inputDevice

	mu sync.Mutex
	// x, y is the current cursor position.
	// x, y - 지금 커서 위치.
	x, y int
	// w, h is the screen size, which keeps the cursor from leaving it.
	// w, h - 화면 크기. 커서가 밖으로 못 나가게 가둔다.
	w, h int
	// down says whether the left button is held.
	// down - 왼쪽 버튼이 눌려 있는지.
	down bool
	// shift says whether shift is held, which decides whether a character key
	// comes out upper case.
	//
	// shift - 시프트가 눌려 있는지. 글자 키를 대문자로 바꿀지 정한다.
	shift bool

	// accel turns mouse counts into pixels; see pointer_accel.go.
	// accel - 마우스 카운트를 픽셀로 바꾼다. pointer_accel.go 참고.
	accel pointerAccel

	// out is the channel the assembled events leave by.
	// out - 조립된 이벤트가 나가는 통로.
	out chan Event
	// stop is the signal that halts the reader goroutines.
	// stop - 읽기 고루틴을 멈추는 신호.
	stop chan struct{}
	once sync.Once
}

// OpenInput opens every event device under /dev/input. If none of them open it
// returns an error and the caller falls back to a keyboard-only screen.
//
// OpenInput - /dev/input 아래의 모든 event 장치를 연다. 하나도 못 열면
// 오류를 돌려주고, 호출자는 키보드만 쓰는 화면으로 되돌아간다.
func OpenInput(screenW, screenH int) (*InputReader, error) {
	paths, _ := filepath.Glob("/dev/input/event*")
	r := &InputReader{
		w: screenW, h: screenH,
		x: screenW / 2, y: screenH / 2,
		out:  make(chan Event, 256),
		stop: make(chan struct{}),
	}
	for _, p := range paths {
		f, err := os.OpenFile(p, os.O_RDONLY, 0)
		if err != nil {
			continue
		}
		// A blocking read would never see the stop signal, so it is left non-blocking.
		// 읽기가 막히면 종료 신호를 못 받으므로 논블로킹으로 둔다.
		if err := unix.SetNonblock(int(f.Fd()), true); err != nil {
			f.Close()
			continue
		}
		d := &inputDevice{f: f}
		d.loadAbsRange()
		r.devs = append(r.devs, d)
	}
	if len(r.devs) == 0 {
		return nil, os.ErrNotExist
	}
	for _, d := range r.devs {
		go r.read(d)
	}
	return r, nil
}

// loadAbsRange asks an absolute-coordinate device for its X and Y ranges up front.
// loadAbsRange - 절대좌표 장치면 X, Y 축의 범위를 미리 물어둔다.
func (d *inputDevice) loadAbsRange() {
	var xi, yi absInfo
	if err := ioctlPtr(d.f.Fd(), eviocgabs(absX), unsafe.Pointer(&xi)); err != nil {
		return
	}
	if err := ioctlPtr(d.f.Fd(), eviocgabs(absY), unsafe.Pointer(&yi)); err != nil {
		return
	}
	if xi.Maximum <= xi.Minimum || yi.Maximum <= yi.Minimum {
		return
	}
	d.absRange = [2]absInfo{xi, yi}
	d.hasRange = true
}

// Events is the channel the assembled events come out of.
// Events - 조립된 이벤트가 나오는 통로.
func (r *InputReader) Events() <-chan Event { return r.out }

// Cursor is the current cursor position.
// Cursor - 지금 커서 위치.
func (r *InputReader) Cursor() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.x, r.y
}

// Close stops reading and closes the devices.
// Close - 읽기를 멈추고 장치를 닫는다.
func (r *InputReader) Close() {
	r.once.Do(func() {
		close(r.stop)
		for _, d := range r.devs {
			d.f.Close()
		}
	})
}

// read reads from one device and assembles its events.
// read - 장치 하나에서 이벤트를 읽어 조립한다.
func (r *InputReader) read(d *inputDevice) {
	buf := make([]byte, inputEventSize*32)
	fd := int(d.f.Fd())
	for {
		select {
		case <-r.stop:
			return
		default:
		}

		// Being non-blocking, it returns at once when there is no data. Waiting on
		// poll before reading is what keeps this from becoming a busy loop.
		//
		// 논블로킹이라 데이터가 없으면 바로 돌아온다. poll 로 기다렸다가
		// 읽어야 바쁜 대기가 되지 않는다.
		pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(pfd, 200)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}

		got, err := d.f.Read(buf)
		if err != nil {
			if err == unix.EAGAIN {
				continue
			}
			return
		}
		for off := 0; off+inputEventSize <= got; off += inputEventSize {
			r.decode(d, buf[off:off+inputEventSize])
		}
	}
}

// decode turns one input_event into one of our events.
// decode - input_event 하나를 우리 이벤트로 옮긴다.
func (r *InputReader) decode(d *inputDevice, b []byte) {
	// The first 16 bytes are the timestamp and are skipped.
	// 앞 16 바이트는 타임스탬프라 건너뛴다.
	typ := binary.LittleEndian.Uint16(b[16:18])
	code := binary.LittleEndian.Uint16(b[18:20])
	val := int32(binary.LittleEndian.Uint32(b[20:24]))

	switch typ {
	case evRel:
		switch code {
		case relX:
			d.rel[0] += val
			d.hasRel = true
		case relY:
			d.rel[1] += val
			d.hasRel = true
		case relWheel:
			// The wheel is one notch per event and has nothing to add up.
			// 휠은 한 이벤트가 한 칸이라 모을 것이 없다.
			r.emit(Event{Kind: EventWheel, Delta: int(val), X: r.cx(), Y: r.cy()})
		}

	case evAbs:
		// Only a device that answered with a real axis range is treated as
		// absolute. Without that check a device that reports some unrelated
		// absolute axis would teleport the cursor.
		//
		// 축 범위를 제대로 돌려준 장치만 절대좌표로 본다. 그 확인이 없으면
		// 엉뚱한 절대축을 보내는 장치가 커서를 순간이동시킨다.
		if !d.hasRange {
			return
		}
		switch code {
		case absX:
			d.pending[0] = val
			d.hasAbs = true
		case absY:
			d.pending[1] = val
			d.hasAbs = true
		}

	case evKey:
		switch {
		case code == btnLeft || code == btnTouch:
			// A tablet can report a press as BTN_TOUCH.
			// 태블릿은 누름을 BTN_TOUCH 로 보내기도 한다.
			if val == 1 {
				r.setDown(true)
				r.emit(Event{Kind: EventMouseDown, X: r.cx(), Y: r.cy()})
			} else if val == 0 {
				r.setDown(false)
				r.emit(Event{Kind: EventMouseUp, X: r.cx(), Y: r.cy()})
			}
		case val == 1 || val == 2:
			// A key press, and its auto-repeat.
			// 키 누름과 자동 반복.
			r.emitKey(code)
		case val == 0:
			// A release is only used to clear the shift state.
			// 뗌은 시프트 상태를 푸는 데만 쓴다.
			r.releaseKey(code)
		}

	case evSyn:
		// One batch is finished, so the movement gathered in it is applied now.
		// 한 묶음이 끝났다. 그 안에서 모은 이동을 이제 적용한다.
		if d.hasRel {
			dx, dy := d.rel[0], d.rel[1]
			d.rel = [2]int32{}
			d.hasRel = false
			r.moveByCounts(dx, dy)
		}
		if d.hasAbs {
			d.hasAbs = false
			r.moveTo(
				scaleAxis(d.pending[0], d.absRange[0], r.w),
				scaleAxis(d.pending[1], d.absRange[1], r.h),
			)
		}
	}
}

// scaleAxis maps one absolute axis onto screen pixels.
// scaleAxis - 절대좌표 한 축을 화면 픽셀로.
func scaleAxis(v int32, info absInfo, size int) int {
	span := int(info.Maximum - info.Minimum)
	if span <= 0 {
		return 0
	}
	p := int(v-info.Minimum) * (size - 1) / span
	if p < 0 {
		p = 0
	}
	if p >= size {
		p = size - 1
	}
	return p
}

func (r *InputReader) cx() int { r.mu.Lock(); defer r.mu.Unlock(); return r.x }
func (r *InputReader) cy() int { r.mu.Lock(); defer r.mu.Unlock(); return r.y }

func (r *InputReader) setDown(v bool) {
	r.mu.Lock()
	r.down = v
	r.mu.Unlock()
}

// moveByCounts applies one batch of mouse counts to the cursor.
//
// The factor comes from how far the batch moved, so a slow movement is scaled
// down and a fast one up; see the mouseSlowFactor block for why. What is left
// of a pixel is carried to the next batch so that slow movement is not lost to
// truncation. The cursor stays inside the screen.
//
// moveByCounts - 마우스 카운트 한 묶음을 커서에 적용한다.
//
// 배율은 그 묶음이 얼마나 움직였는지에서 나온다. 느리면 줄이고 빠르면 키운다
// (이유는 mouseSlowFactor 쪽 주석에 있다). 픽셀의 나머지는 다음 묶음으로
// 이월해서 느린 이동이 잘려 없어지지 않게 한다. 커서는 화면 안에 가둔다.
func (r *InputReader) moveByCounts(dxc, dyc int32) {
	r.mu.Lock()
	dx, dy := r.accel.step(dxc, dyc)
	if dx == 0 && dy == 0 {
		r.mu.Unlock()
		return
	}
	r.x = clamp(r.x+dx, 0, r.w-1)
	r.y = clamp(r.y+dy, 0, r.h-1)
	x, y := r.x, r.y
	r.mu.Unlock()
	r.emit(Event{Kind: EventMouseMove, X: x, Y: y})
}

// moveTo is a move to an absolute position; an unchanged value emits nothing.
// moveTo - 절대 위치로. 값이 그대로면 이벤트를 내지 않는다.
func (r *InputReader) moveTo(x, y int) {
	r.mu.Lock()
	x = clamp(x, 0, r.w-1)
	y = clamp(y, 0, r.h-1)
	if x == r.x && y == r.y {
		r.mu.Unlock()
		return
	}
	r.x, r.y = x, y
	r.mu.Unlock()
	r.emit(Event{Kind: EventMouseMove, X: x, Y: y})
}

// emit puts an event on the channel, dropping it when the channel is full -
// keeping up with the latest state beats letting mouse movement pile up behind.
//
// emit - 이벤트를 통로에 넣는다. 가득 차면 버린다 - 마우스 이동이 밀려
// 쌓이는 것보다 최신 상태로 따라가는 편이 낫다.
func (r *InputReader) emit(e Event) {
	select {
	case r.out <- e:
	default:
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
