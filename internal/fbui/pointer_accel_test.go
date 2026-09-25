package fbui

import "testing"

// pointer_accel_test.go checks that mouse counts turn into pixels the way the
// cursor needs them to. The three things that matter are that slow movement
// is not lost, that fast movement reaches further than slow movement of the
// same total, and that the factor stays inside its two ends.
//
// pointer_accel_test.go - 마우스 카운트가 커서에 필요한 대로 픽셀이 되는지
// 확인한다. 중요한 건 셋이다. 느린 이동이 사라지지 않을 것, 같은 총량이라도
// 빠른 이동이 더 멀리 갈 것, 배율이 두 끝 사이를 벗어나지 않을 것.

// TestAccelFactorStaysInRange checks that the factor stays between its two
// ends. Going past mouseFastFactor, however fast the movement, would send the
// cursor flying off the screen at the slightest flick.
//
// TestAccelFactorStaysInRange - 배율이 두 끝 사이에 머무는지. 아무리 빨라도
// mouseFastFactor 를 넘으면 조금만 튕겨도 커서가 화면 밖으로 날아간다.
func TestAccelFactorStaysInRange(t *testing.T) {
	for _, speed := range []float64{0, 0.5, 1, 5, 15, 26, 100, 5000} {
		got := accelFactor(speed)
		if got < mouseSlowFactor || got > mouseFastFactor {
			t.Errorf("accelFactor(%v) = %v, 범위 [%v, %v] 밖",
				speed, got, mouseSlowFactor, mouseFastFactor)
		}
	}
}

// TestAccelFactorRises checks that the factor never drops as speed rises. A
// dip in the middle would slow the cursor at one particular speed and make it
// feel like it catches.
//
// TestAccelFactorRises - 빨라질수록 배율이 줄지 않는지. 중간이 꺼지면 특정
// 속도에서만 커서가 느려져 손에 걸리는 느낌이 난다.
func TestAccelFactorRises(t *testing.T) {
	prev := accelFactor(0)
	for speed := 1.0; speed <= 60; speed++ {
		got := accelFactor(speed)
		if got < prev {
			t.Fatalf("속도 %v 에서 배율이 낮아짐: %v -> %v", speed, prev, got)
		}
		prev = got
	}
}

// TestSlowMovementIsNotLost checks that slow movement, one count at a time,
// does become pixels in the end. Dropping the fraction would give 0 pixels
// forever at a factor of 0.4 and freeze the cursor; the carry prevents that.
//
// TestSlowMovementIsNotLost - 1 카운트씩 미는 느린 이동이 결국 픽셀이 되는지.
// 소수를 버리면 배율 0.4 에서 영영 0 픽셀이라 커서가 굳는다. 이월이 그걸 막는다.
func TestSlowMovementIsNotLost(t *testing.T) {
	var p pointerAccel
	total := 0
	for i := 0; i < 10; i++ {
		dx, _ := p.step(1, 0)
		total += dx
	}
	if total == 0 {
		t.Fatal("1 카운트씩 열 번 밀었는데 커서가 한 픽셀도 안 움직임")
	}
	// Ten nudges should come close to 4 pixels (0.4 * 10). It need not be exact,
	// but with the carry working it is between 1 and 6.
	//
	// 열 번 밀면 4 픽셀(0.4 * 10)에 가까워야 한다. 완전히 같을 필요는 없지만
	// 이월이 동작한다면 1 이상 6 이하다.
	if total < 1 || total > 6 {
		t.Errorf("이월 결과가 이상함: %d 픽셀", total)
	}
}

// TestFastMovementGoesFurther checks that, for the same total count, moving
// fast in one go reaches further. If this is reversed the acceleration runs
// backwards.
//
// TestFastMovementGoesFurther - 총 카운트가 같아도 한 번에 빠르게 움직인
// 쪽이 더 멀리 가는지. 이게 뒤집히면 가속이 거꾸로 걸린 것이다.
func TestFastMovementGoesFurther(t *testing.T) {
	var slow pointerAccel
	slowTotal := 0
	for i := 0; i < 40; i++ {
		dx, _ := slow.step(1, 0)
		slowTotal += dx
	}

	var fast pointerAccel
	fastTotal := 0
	for i := 0; i < 2; i++ {
		dx, _ := fast.step(20, 0)
		fastTotal += dx
	}

	if fastTotal <= slowTotal {
		t.Errorf("빠른 이동이 더 멀리 가야 한다: 느림 %d, 빠름 %d", slowTotal, fastTotal)
	}
}

// TestNoMovementEmitsNothing checks that a zero count does nothing at all. A
// SYN arrives even for a button press alone, so this path is taken often.
//
// TestNoMovementEmitsNothing - 0 카운트가 들어오면 아무 일도 없어야 한다.
// SYN 은 버튼만 눌러도 오므로 이 경로가 자주 호출된다.
func TestNoMovementEmitsNothing(t *testing.T) {
	var p pointerAccel
	p.accX, p.accY = 0.9, 0.9
	dx, dy := p.step(0, 0)
	if dx != 0 || dy != 0 {
		t.Errorf("0 카운트인데 (%d, %d) 이동", dx, dy)
	}
	if p.accX != 0.9 || p.accY != 0.9 {
		t.Errorf("0 카운트인데 이월분이 변함: %v %v", p.accX, p.accY)
	}
}

// TestDirectionIsKept checks that the sign never flips. int() truncates
// towards zero when the carry is negative, so this is checked on its own.
//
// TestDirectionIsKept - 부호가 뒤집히지 않는지. 음수 쪽 이월 처리에서
// int() 가 0 방향으로 자르므로 따로 확인한다.
func TestDirectionIsKept(t *testing.T) {
	var p pointerAccel
	x, y := 0, 0
	for i := 0; i < 20; i++ {
		dx, dy := p.step(-5, 5)
		x += dx
		y += dy
	}
	if x >= 0 {
		t.Errorf("왼쪽으로 밀었는데 x = %d", x)
	}
	if y <= 0 {
		t.Errorf("아래로 밀었는데 y = %d", y)
	}
}
