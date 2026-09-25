// shapes_test.go checks that a round shape really is round.
// shapes_test.go - 둥근 도형이 실제로 둥근지 확인한다.
package fbui

import "testing"

// TestCircleIsRound draws a circle and measures the filled width of each row.
// The width has to grow from the top towards the middle and shrink again.
// Flipping the row mapping narrows the middle into an hourglass, and that
// mistake is what this catches.
//
// TestCircleIsRound - 원을 그리고 각 행의 채워진 폭을 잰다. 폭은 위에서
// 가운데로 갈수록 늘고 다시 줄어야 한다. 행 매핑이 뒤집히면 가운데가
// 좁아져 모래시계가 되는데, 그 실수를 여기서 잡는다.
func TestCircleIsRound(t *testing.T) {
	const r = 20
	c := NewCanvas(64, 64)
	bg := RGB(0x000000)
	c.Fill(Rect{0, 0, 64, 64}, bg)
	fg := RGB(0xFFFFFF)
	c.Circle(32, 32, r, fg)

	widths := make([]int, 0, 2*r)
	for y := 32 - r; y < 32+r; y++ {
		n := 0
		for x := 0; x < 64; x++ {
			if c.At(x, y) != bg {
				n++
			}
		}
		widths = append(widths, n)
	}

	// The middle row has to be the widest.
	// 가운데 행이 가장 넓어야 한다.
	mid := len(widths) / 2
	for i, w := range widths {
		if w > widths[mid] {
			t.Fatalf("가운데(%d px)보다 넓은 행이 있음: y 오프셋 %d 에서 %d px", widths[mid], i, w)
		}
	}
	// The top and the bottom have to be clearly narrower than the middle.
	// 맨 위와 맨 아래는 가운데보다 확실히 좁아야 한다.
	if widths[0] >= widths[mid] {
		t.Errorf("맨 윗행이 가운데만큼 넓음 (%d vs %d) - 모서리가 안 깎였다", widths[0], widths[mid])
	}
	if widths[len(widths)-1] >= widths[mid] {
		t.Errorf("맨 아랫행이 가운데만큼 넓음 (%d vs %d)", widths[len(widths)-1], widths[mid])
	}
	// The upper half has to increase monotonically.
	// 위 절반은 단조 증가해야 한다.
	for i := 1; i <= mid; i++ {
		if widths[i] < widths[i-1] {
			t.Errorf("위 절반에서 폭이 줄어듦: y %d 에서 %d -> %d", i, widths[i-1], widths[i])
			break
		}
	}
}

// TestFillRoundKeepsInterior: the middle of a rounded rectangle has to be
// solidly filled.
//
// TestFillRoundKeepsInterior - 둥근 사각형의 가운데는 꽉 차야 한다.
func TestFillRoundKeepsInterior(t *testing.T) {
	c := NewCanvas(100, 60)
	bg := RGB(0x000000)
	c.Fill(Rect{0, 0, 100, 60}, bg)
	fg := RGB(0xFFFFFF)
	c.FillRound(Rect{10, 10, 80, 40}, 8, fg)

	for y := 20; y < 40; y++ {
		for x := 20; x < 80; x++ {
			if c.At(x, y) != fg {
				t.Fatalf("안쪽 (%d,%d) 이 안 칠해짐", x, y)
			}
		}
	}
	// Outside the corners has to be empty.
	// 모서리 바깥은 비어 있어야 한다.
	if c.At(10, 10) == fg {
		t.Error("왼쪽 위 모서리가 안 깎였다")
	}
}
