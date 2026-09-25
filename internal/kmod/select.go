package kmod

// addDepends follows a module's dependencies, skipping what is already in the
// set so that a cycle inside the pack cannot become an infinite loop.
//
// addDepends - 모듈의 의존성을 추적한다. 이미 담긴 것은 건너뛰므로 팩 안에
// 순환이 있어도 무한 루프에 빠지지 않는다.
func (idx Index) addDepends(name string, into map[string]bool) {
	m, ok := idx[name]
	if !ok {
		return
	}
	for _, d := range m.Depends {
		if into[d] {
			continue
		}
		into[d] = true
		idx.addDepends(d, into)
	}
}

// ByName returns the named modules plus their dependencies, in load order.
//
// Matching on devices is the proper way to choose drivers, but it cannot find
// everything. A USB stick is invisible until usb_storage is loaded, and
// usb_storage only matches once there is a USB device to match - and the
// loader's own disk sits exactly behind that door. So the few modules that
// make the rest of the hardware appear are asked for by name.
//
// A name that is not in the index is skipped without comment. The caller is
// asking for modules that would help, not for modules that must be there.
//
// ByName - 이름으로 지정한 모듈과 그 의존성을 로드 순서로 돌려준다.
//
// 장치로 매칭하는 것이 드라이버 선택의 정공법이지만, 그것만으로는 다
// 찾을 수 없다. USB 스틱은 usb_storage 로드 전에는 안 보이고, usb_storage
// 는 매칭할 USB 장치가 있어야 매칭된다. 로더 자신의 디스크가 정확히 그
// 문 뒤에 있다. 그래서 나머지 하드웨어가 나타나게 만드는 몇 개는 이름으로
// 요청한다.
//
// 인덱스에 없는 이름은 보고 없이 건너뛴다. 호출자는 "도움이 될 모듈들" 을
// 부탁하는 것이지 "반드시 있어야 할 모듈들" 을 부탁하는 게 아니다.
func (idx Index) ByName(names ...string) []Module {
	want := map[string]bool{}
	for _, n := range names {
		n = normalize(n)
		if _, ok := idx[n]; !ok {
			continue
		}
		want[n] = true
		idx.addDepends(n, want)
	}

	// Dependencies first, then the modules that depend on them. This is the
	// only order that loads.
	//
	// 의존 대상 먼저, 그 다음 의존하는 모듈. 이 순서로만 로드가 된다.
	var out []Module
	done := map[string]bool{}
	var visit func(string)
	visit = func(n string) {
		if done[n] {
			return
		}
		done[n] = true
		m, ok := idx[n]
		if !ok {
			return
		}
		for _, d := range m.Depends {
			if want[d] {
				visit(d)
			}
		}
		out = append(out, m)
	}
	for n := range want {
		visit(n)
	}
	return out
}
