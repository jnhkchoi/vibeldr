package main

import (
	"fmt"
	"sync"
)

// installProgress turns the install pipeline's sections and progress displays
// into one overall value for the progress bar.
//
// The pipeline announces each stage with ui.Section and reports long steps
// (the download, writing a partition) with ui.ProgressBar. Each stage has a
// share of the bar, sized roughly by how long it takes; inside a stage the
// progress display moves the bar through that share. The value never goes
// back, and an unknown section leaves it where it is.
//
// installProgress - 설치 파이프라인의 단계와 진행률 표시를 진행 막대 하나의
// 값으로 바꾼다.
//
// 파이프라인은 단계마다 ui.Section 을 찍고, 오래 걸리는 일(다운로드, 파티션
// 쓰기)은 ui.ProgressBar 로 알린다. 단계마다 걸리는 시간에 맞춰 막대의 몫을
// 나눠 두고, 단계 안에서는 진행률 표시가 그 몫 안에서 막대를 움직인다. 값은
// 뒤로 가지 않고, 모르는 단계는 막대를 그대로 둔다.
type installProgress struct {
	mu    sync.Mutex
	stage int // index into installStages, -1 before the first / installStages 의 인덱스, 첫 단계 전엔 -1
	frac  float64
	best  float64
	done  bool
}

// installStages are the sections buildFlow prints, in order, with their share
// of the bar. "Embedded DSM" takes the place of "Fetch" when the image carries
// the DSM kernel.
//
// installStages - buildFlow 가 찍는 단계, 순서대로, 막대에서의 몫과 함께.
// 이미지에 DSM 커널이 있으면 "Fetch" 자리에 "Embedded DSM" 이 온다.
var installStages = []struct {
	section string
	label   string
	share   float64
}{
	{"Fetch", "DSM 내려받기", 40},
	{"Extract", "압축 풀기", 10},
	{"Drivers", "드라이버 준비", 10},
	{"Patch", "커널·램디스크 패치", 15},
	{"Image", "이미지 만들기", 10},
	{"Write payload", "부트 디스크에 쓰기", 10},
	{"Boot menu", "부팅 메뉴", 5},
}

func newInstallProgress() *installProgress { return &installProgress{stage: -1} }

// section moves to the stage the pipeline has just announced.
// section - 파이프라인이 방금 알린 단계로 옮긴다.
func (p *installProgress) section(title string) {
	if title == "Embedded DSM" {
		title = "Fetch"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range installStages {
		if s.section == title && i >= p.stage {
			p.stage, p.frac = i, 0
			return
		}
	}
}

// progress moves the bar inside the current stage.
// progress - 지금 단계 안에서 막대를 움직인다.
func (p *installProgress) progress(label string, done, total int64) {
	if total <= 0 {
		return
	}
	f := float64(done) / float64(total)
	if f > 1 {
		f = 1
	}
	p.mu.Lock()
	p.frac = f
	p.mu.Unlock()
}

// finish is the end of the pipeline, whatever stage it stopped at.
// finish - 파이프라인의 끝. 어느 단계에서 멈췄든 상관없다.
func (p *installProgress) finish() {
	p.mu.Lock()
	p.done = true
	p.mu.Unlock()
}

// value is the overall fraction, 0 to 1, and a label for the bar.
// value - 전체 비율(0~1)과 막대에 쓸 글자.
func (p *installProgress) value() (float64, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		p.best = 1
		return 1, "완료"
	}
	if p.stage < 0 {
		return p.best, "준비 중"
	}
	var total, before float64
	for i, s := range installStages {
		total += s.share
		if i < p.stage {
			before += s.share
		}
	}
	cur := installStages[p.stage]
	v := (before + cur.share*p.frac) / total
	if v > p.best {
		p.best = v
	}
	return p.best, fmt.Sprintf("%s  %d%%", cur.label, int(p.best*100))
}
