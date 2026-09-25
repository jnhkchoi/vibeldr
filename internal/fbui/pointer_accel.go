package fbui

import "math"

// pointer_accel.go turns mouse counts into cursor pixels.
//
// A mouse reports counts, not pixels, and how many counts an inch of movement
// is depends on the mouse: 400 on an old one, 1000 on an ordinary one, 4000 on
// a gaming one. evdev does not say which. On a desktop libinput looks the model
// up in udev's hardware database and normalises to 1000; there is no such
// database here, and nothing else in this environment handles the pointer, so
// the conversion is ours to make.
//
// Taking one count as one pixel is right for a 1000-count mouse and unusable on
// a 4000-count one, where an inch of movement crosses a 1280-pixel screen three
// times.
//
// So the speed of the movement decides the factor, which is what every desktop
// does. Slow movement is scaled down, so a high-count mouse can still be aimed;
// fast movement is scaled up, so a low-count mouse can still cross the screen.
//
// This sits on its own, away from the evdev reading, so that it can be tested
// anywhere rather than only on Linux.
//
// pointer_accel.go - 마우스 카운트를 커서 픽셀로 바꾼다.
//
// 마우스는 픽셀이 아니라 카운트를 보내고, 1 인치가 몇 카운트인지는 마우스마다
// 다르다. 옛날 것은 400, 보통은 1000, 게이밍은 4000 이다. evdev 는 그걸
// 알려주지 않는다. 데스크톱에서는 libinput 이 udev 하드웨어 DB 에서 모델을
// 찾아 1000 기준으로 환산하는데, 여기엔 그 DB 가 없고 포인터를 대신 처리해 줄
// 것도 없다. 그래서 환산은 우리 몫이다.
//
// 1 카운트를 1 픽셀로 쓰면 1000 카운트 마우스에는 맞지만 4000 카운트
// 마우스에서는 못 쓴다. 1 인치를 밀면 1280 픽셀 화면을 세 번 가로지른다.
//
// 그래서 이동 속도로 배율을 정한다. 어느 데스크톱이든 하는 방식이다. 천천히
// 움직이면 줄여서 고감도 마우스로도 조준이 되게 하고, 빠르게 움직이면 키워서
// 저감도 마우스로도 화면을 건널 수 있게 한다.
//
// evdev 읽기와 떼어 둔 이유는 리눅스에서만이 아니라 어디서든 테스트할 수 있게
// 하기 위해서다.

// How a mouse count becomes a pixel. One frame of a normal drag is around 5 to
// 15 counts, so the fast end is set above that and only a deliberate flick
// reaches it.
//
// 카운트를 픽셀로 바꾸는 값들. 보통 끌기 한 프레임이 5~15 카운트라, 빠른 쪽
// 기준은 그보다 위에 두어 일부러 튕길 때만 닿게 했다.
const (
	// mouseSlowFactor is the factor at a standstill, for aiming.
	// mouseSlowFactor - 거의 멈춰 있을 때의 배율. 조준용.
	mouseSlowFactor = 0.40
	// mouseFastFactor is the factor at mouseFastAt and above, for reach.
	// mouseFastFactor - mouseFastAt 이상에서의 배율. 멀리 가기용.
	mouseFastFactor = 2.20
	// mouseFastAt is the per-frame movement, in counts, at which the factor
	// reaches mouseFastFactor.
	//
	// mouseFastAt - 배율이 mouseFastFactor 에 닿는 한 프레임 이동량(카운트).
	mouseFastAt = 26.0
)

// accelFactor is the factor for one batch that moved this far in counts.
// It rises straight from mouseSlowFactor to mouseFastFactor and stops there.
//
// accelFactor - 그 묶음이 이만큼(카운트) 움직였을 때의 배율.
// mouseSlowFactor 에서 mouseFastFactor 까지 직선으로 오르고 거기서 멈춘다.
func accelFactor(speed float64) float64 {
	t := speed / mouseFastAt
	if t > 1 {
		t = 1
	}
	return mouseSlowFactor + (mouseFastFactor-mouseSlowFactor)*t
}

// pointerAccel keeps what is left of a pixel between batches.
//
// Scaled movement is not a whole number of pixels. Throwing the fraction away
// would stop slow movement altogether: at a factor of 0.4 a one-count nudge is
// 0.4 pixels, truncates to zero, and the cursor never moves however long it is
// pushed. Carrying it over means the pixel arrives on the third nudge instead
// of never.
//
// pointerAccel - 묶음 사이에 남은 픽셀의 소수 부분을 들고 있는다.
//
// 배율을 곱하면 픽셀 수가 정수로 떨어지지 않는다. 소수를 버리면 느린 이동이
// 아예 막힌다. 배율 0.4 에서 1 카운트는 0.4 픽셀이고 잘라내면 0 이라, 아무리
// 밀어도 커서가 안 움직인다. 이월하면 영영 안 오는 대신 세 번째 밀 때 1 픽셀이
// 나온다.
type pointerAccel struct {
	accX, accY float64
}

// step takes one batch of counts and gives the whole pixels to move by.
// step - 카운트 한 묶음을 받아 옮길 정수 픽셀을 돌려준다.
func (p *pointerAccel) step(dxc, dyc int32) (int, int) {
	if dxc == 0 && dyc == 0 {
		return 0, 0
	}
	f := accelFactor(math.Hypot(float64(dxc), float64(dyc)))
	p.accX += float64(dxc) * f
	p.accY += float64(dyc) * f
	dx := int(p.accX)
	dy := int(p.accY)
	p.accX -= float64(dx)
	p.accY -= float64(dy)
	return dx, dy
}
