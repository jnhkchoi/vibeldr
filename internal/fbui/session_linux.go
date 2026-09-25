//go:build linux

// session_linux.go ties the framebuffer and the input devices together into the
// loop that runs the wizard.
//
// Drawing happens only when something changed. The cursor is the exception: on
// every move only two areas, the one left behind and the new one, go to the
// screen. Flushing the whole screen every frame would use up the framebuffer
// bandwidth and make the cursor feel sticky.
//
// session_linux.go - 프레임버퍼와 입력 장치를 묶어 마법사를 돌리는 루프.
//
// 그리기는 "바뀐 게 있을 때만" 한다. 커서는 예외로, 움직일 때마다 지나간
// 자리와 새 자리 두 곳만 화면으로 옮긴다. 화면 전체를 매 프레임 옮기면
// 프레임버퍼 대역폭을 다 쓰고 커서가 끈적해진다.
package fbui

import (
	"fmt"
	"os"
	"time"
)

// Session is one open screen with its input devices.
// Session - 열린 화면과 입력 장치 한 벌.
type Session struct {
	fb     *Framebuffer
	input  *InputReader
	canvas *Canvas
	cursor *Cursor
}

// Info is a one-line summary of the screen setup, for the log.
// Info - 화면 구성 한 줄 요약. 로그에 남긴다.
func (s *Session) Info() string {
	mouse := "입력 장치 없음"
	if s.input != nil {
		mouse = fmt.Sprintf("입력 장치 %d 개", len(s.input.devs))
	}
	return s.fb.Info() + ", " + mouse
}

// OpenSession opens the framebuffer and the input devices. With no framebuffer
// it returns an error and the caller falls back to a text-only screen.
//
// OpenSession - 프레임버퍼와 입력 장치를 연다. 프레임버퍼가 없으면
// 오류를 돌려주고, 호출자는 글자만 쓰는 화면으로 되돌아간다.
func OpenSession(fbPath string) (*Session, error) {
	fb, err := OpenFramebuffer(fbPath)
	if err != nil {
		return nil, err
	}
	s := &Session{
		fb:     fb,
		canvas: fb.NewCanvas(),
		cursor: NewCursor(),
	}
	// The screen comes up even with no input device. Without a mouse it is
	// driven from the keyboard alone, and without that nothing can be done -
	// but none of that is a reason not to put the screen up.
	//
	// 입력 장치는 없어도 화면은 띄운다. 마우스가 없으면 키보드만으로
	// 조작하게 되고, 그것조차 없으면 아무것도 못 하지만 그건 화면을
	// 띄우지 않을 이유는 아니다.
	if in, err := OpenInput(fb.W, fb.H); err == nil {
		s.input = in
		s.cursor.MoveTo(fb.W/2, fb.H/2)
	} else {
		s.cursor.SetVisible(false)
	}
	return s, nil
}

// Canvas is what gets drawn on.
// Canvas - 그릴 대상.
func (s *Session) Canvas() *Canvas { return s.canvas }

// Size is the screen size.
// Size - 화면 크기.
func (s *Session) Size() (int, int) { return s.fb.W, s.fb.H }

// Close closes the devices.
// Close - 장치를 닫는다.
func (s *Session) Close() {
	if s.input != nil {
		s.input.Close()
	}
	if s.fb != nil {
		s.fb.Close()
	}
}

// Present moves the canvas to the screen, putting the cursor on briefly and
// taking it back off.
//
// Present - 캔버스를 화면으로 옮긴다. 커서를 잠깐 얹었다가 되돌린다.
func (s *Session) Present() {
	s.cursor.Draw(s.canvas)
	s.fb.Flush(s.canvas)
	s.cursor.Restore(s.canvas)
}

// presentCursorMove is for when only the cursor moved: it flushes the area left
// behind and the new one, nothing else.
//
// presentCursorMove - 커서만 움직였을 때. 지나간 자리와 새 자리만 옮긴다.
func (s *Session) presentCursorMove(old Rect) {
	s.fb.FlushRect(s.canvas, old)
	s.cursor.Draw(s.canvas)
	s.fb.FlushRect(s.canvas, s.cursor.LastBounds())
	s.cursor.Restore(s.canvas)
}

// Run takes and handles input until the wizard is done.
//
// A non-nil tick is called periodically while there is no input; the install
// progress screen uses it to update how far along it is. A tick returning true
// redraws the screen.
//
// Run - 마법사가 끝날 때까지 입력을 받아 처리한다.
//
// tick 이 nil 이 아니면 입력이 없는 동안 주기적으로 불린다. 설치 진행
// 화면이 진행도를 갱신하는 데 쓴다. tick 이 true 를 돌려주면 화면을 다시
// 그린다.
func (s *Session) Run(w *Wizard, tick func() bool) {
	w.Draw()
	s.Present()

	var events <-chan Event
	if s.input != nil {
		events = s.input.Events()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for !w.Done() {
		select {
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if e.Kind == EventMouseMove {
				old := s.cursor.LastBounds()
				s.cursor.MoveTo(e.X, e.Y)
				w.Handle(e)
				if w.Dirty() {
					w.Draw()
					s.Present()
				} else {
					s.presentCursorMove(old)
				}
				continue
			}
			w.Handle(e)
			if w.Dirty() {
				w.Draw()
				s.Present()
			}

		case <-ticker.C:
			if tick != nil && tick() {
				w.Draw()
				s.Present()
			} else if w.Dirty() {
				w.Draw()
				s.Present()
			}
		}
	}
}

// Available checks up front whether this machine can run the framebuffer GUI.
// It only looks for the device rather than opening it, so it is cheap.
//
// Available - 이 기계에서 프레임버퍼 GUI 를 쓸 수 있는지 미리 본다.
// 장치를 열어보지 않고 존재 여부만 확인하므로 값싸다.
func Available(fbPath string) bool {
	if fbPath == "" {
		fbPath = "/dev/fb0"
	}
	st, err := os.Stat(fbPath)
	return err == nil && st.Mode()&os.ModeDevice != 0
}
