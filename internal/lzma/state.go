package lzma

// The LZMA state machine. The kind of symbol last emitted is kept in a state
// value of 0..11 and is what selects the probability model for the next one.
// The encoder and the decoder have to agree on these functions for the stream
// to line up, so they live in one place.
//
// LZMA 상태 머신: 마지막에 낸 심볼 종류를 상태값 (0..11) 에 담아 다음 심볼의
// 확률 모델 선택에 쓴다. 인코더 / 디코더가 같은 함수를 봐야 스트림이 맞물리므로
// 여기 한 곳에 둔다.

func stateAfterLiteral(s uint32) uint32 {
	switch {
	case s < 4:
		return 0
	case s < 10:
		return s - 3
	default:
		return s - 6
	}
}

func stateAfterMatch(s uint32) uint32 {
	if s < 7 {
		return 7
	}
	return 10
}

func stateAfterRep(s uint32) uint32 {
	if s < 7 {
		return 8
	}
	return 11
}

func stateAfterShortRep(s uint32) uint32 {
	if s < 7 {
		return 9
	}
	return 11
}
