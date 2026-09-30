package lzma

import "fmt"

const (
	numStates        = 12
	posBitsMax       = 4
	matchMinLen      = 2
	lenToPosStates   = 4
	endPosModelIndex = 14
	fullDistances    = 1 << (endPosModelIndex / 2) // 128
	alignBits        = 4
	// posDecodersSize covers every reverse-coded distance tail below
	// endPosModelIndex, sharing one array the way the reference decoder does.
	//
	// posDecodersSize - endPosModelIndex 아래의 역순 인코딩된 distance
	// 꼬리 전부를 담는다. 참조 디코더와 같이 배열 하나를 공유한다.
	posDecodersSize = 1 + fullDistances - endPosModelIndex // 115
	// endMarker is the distance an encoder writes to say the stream is over.
	// endMarker - 인코더가 스트림의 끝을 알릴 때 쓰는 distance 값.
	endMarker = 0xFFFFFFFF
)

// lenDecoder decodes a match length. Short lengths dominate real data, so the
// format spends its cheapest codes on them: three tiers, selected by two bits.
//
// lenDecoder - 매치 길이를 디코드한다. 실제 데이터에서는 짧은 길이가 압도적
// 이라, 포맷은 가장 싼 코드를 거기 쓴다. 두 비트로 고르는 3 단계 구조다.
type lenDecoder struct {
	choice  prob
	choice2 prob
	low     [1 << posBitsMax][8]prob
	mid     [1 << posBitsMax][8]prob
	high    [256]prob
}

func (l *lenDecoder) reset() {
	l.choice, l.choice2 = probInit, probInit
	for i := range l.low {
		fill(l.low[i][:])
		fill(l.mid[i][:])
	}
	fill(l.high[:])
}

func (l *lenDecoder) decode(r *rangeDecoder, posState uint32) uint32 {
	if r.bit(&l.choice) == 0 {
		return r.tree(l.low[posState][:], 3) // lengths 2..9 / 길이 2..9
	}
	if r.bit(&l.choice2) == 0 {
		return 8 + r.tree(l.mid[posState][:], 3) // lengths 10..17 / 길이 10..17
	}
	return 16 + r.tree(l.high[:], 8) // lengths 18..273 / 길이 18..273
}

// decoder holds the whole adaptive model plus the output produced so far.
//
// The output doubles as the dictionary: a match is just a copy from earlier in
// the same buffer, so there is no separate sliding window to maintain. That
// costs memory proportional to the output, which is fine for what is decoded
// here: a DSM ramdisk or kernel, held in memory whole anyway.
//
// decoder - 적응 모델 전체와 지금까지 만들어 낸 출력을 담는다.
//
// 출력이 곧 사전이다. 매치란 같은 버퍼의 앞쪽에서 복사해 오는 것일 뿐이라
// 따로 슬라이딩 윈도를 유지할 필요가 없다. 대신 출력 크기에 비례하는 메모리를
// 쓰는데, 여기서 푸는 것은 DSM 램디스크나 커널이고 어차피 통째로 메모리에
// 올리므로 문제되지 않는다.
type decoder struct {
	h   Header
	out []byte

	state                  uint32
	rep0, rep1, rep2, rep3 uint32

	isMatch     [numStates << posBitsMax]prob
	isRep       [numStates]prob
	isRepG0     [numStates]prob
	isRepG1     [numStates]prob
	isRepG2     [numStates]prob
	isRep0Long  [numStates << posBitsMax]prob
	posSlot     [lenToPosStates][64]prob
	posDecoders [posDecodersSize]prob
	align       [1 << alignBits]prob
	litProbs    []prob

	lenDec    lenDecoder
	repLenDec lenDecoder

	posMask    uint32
	litPosMask uint32
}

func (d *decoder) reset() {
	d.state = 0
	d.rep0, d.rep1, d.rep2, d.rep3 = 0, 0, 0, 0
	fill(d.isMatch[:])
	fill(d.isRep[:])
	fill(d.isRepG0[:])
	fill(d.isRepG1[:])
	fill(d.isRepG2[:])
	fill(d.isRep0Long[:])
	for i := range d.posSlot {
		fill(d.posSlot[i][:])
	}
	fill(d.posDecoders[:])
	fill(d.align[:])
	d.litProbs = make([]prob, 0x300<<(d.h.LC+d.h.LP))
	fill(d.litProbs)
	d.lenDec.reset()
	d.repLenDec.reset()
	d.posMask = 1<<d.h.PB - 1
	d.litPosMask = 1<<d.h.LP - 1
}

// byteAt returns an already-decoded byte, counting back from the end.
// A dist of 1 is the byte written most recently.
//
// byteAt - 이미 디코드된 바이트를 끝에서부터 거슬러 세어 돌려준다.
// dist 가 1 이면 가장 최근에 쓴 바이트다.
func (d *decoder) byteAt(dist uint32) byte { return d.out[uint32(len(d.out))-dist] }

// literal decodes one byte. When the previous symbol was a match, the decoder
// knows the byte sitting one match-distance back is a good guess, so it
// predicts against that byte bit by bit until the guess turns out wrong.
//
// literal - 바이트 하나를 디코드한다. 직전 심볼이 매치였으면 매치 거리만큼
// 뒤에 있는 바이트가 좋은 추측이라는 걸 알고 있으므로, 그 바이트를 기준으로
// 비트 단위로 예측하다가 추측이 틀리는 지점에서 멈춘다.
func (d *decoder) literal(r *rangeDecoder) {
	var prev uint32
	if len(d.out) > 0 {
		prev = uint32(d.byteAt(1))
	}
	litState := (uint32(len(d.out))&d.litPosMask)<<d.h.LC + prev>>(8-uint(d.h.LC))
	probs := d.litProbs[0x300*litState:]

	sym := uint32(1)
	if d.state >= 7 {
		matchByte := uint32(d.byteAt(d.rep0 + 1))
		for sym < 0x100 {
			matchBit := (matchByte >> 7) & 1
			matchByte <<= 1
			bit := r.bit(&probs[(1+matchBit)<<8+sym])
			sym = sym<<1 | bit
			if matchBit != bit {
				break
			}
		}
	}
	for sym < 0x100 {
		sym = sym<<1 | r.bit(&probs[sym])
	}
	d.out = append(d.out, byte(sym))
}

// distance decodes how far back a match starts. It takes the raw length code
// because short and long matches have different distance statistics.
//
// distance - 매치가 얼마나 뒤에서 시작하는지를 디코드한다. 길이 코드를 그대로
// 받는 이유는 짧은 매치와 긴 매치의 거리 통계가 다르기 때문이다.
func (d *decoder) distance(r *rangeDecoder, length uint32) uint32 {
	lenState := length
	if lenState > lenToPosStates-1 {
		lenState = lenToPosStates - 1
	}
	slot := r.tree(d.posSlot[lenState][:], 6)
	if slot < 4 {
		return slot
	}
	directBits := int(slot>>1) - 1
	dist := (2 | slot&1) << directBits
	if slot < endPosModelIndex {
		dist += r.treeReverse(d.posDecoders[dist-slot:], directBits)
	} else {
		dist += r.direct(directBits-alignBits) << alignBits
		dist += r.treeReverse(d.align[:], alignBits)
	}
	return dist
}

func (d *decoder) copyMatch(dist, length uint32) error {
	if dist > uint32(len(d.out)) {
		return fmt.Errorf("lzma: match reaches %d bytes back but only %d are decoded: %w", dist, len(d.out), ErrCorrupt)
	}
	// Copied one byte at a time on purpose: a match may overlap its own
	// output, which is how LZMA encodes a repeating pattern.
	//
	// 한 바이트씩 복사하는 것은 의도적이다. 매치가 자기 출력과 겹칠 수 있고,
	// LZMA 는 반복 패턴을 바로 그렇게 인코딩한다.
	for ; length > 0; length-- {
		d.out = append(d.out, d.byteAt(dist))
	}
	return nil
}

func (d *decoder) run(src []byte) error {
	d.reset()
	r := &rangeDecoder{in: src}
	if err := r.init(); err != nil {
		return err
	}
	if d.h.SizeKnown {
		if d.h.Size > 1<<40 {
			return fmt.Errorf("lzma: declared size %d is implausible", d.h.Size)
		}
		d.out = make([]byte, 0, d.h.Size)
	}

	for {
		if d.h.SizeKnown && uint64(len(d.out)) >= d.h.Size {
			return nil
		}
		// A stream that has run dry stops here. Whether that was the end or a
		// truncation is decided by the caller, comparing against the header.
		//
		// 입력이 마른 스트림은 여기서 멈춘다. 그게 정상 종료인지 잘림인지는
		// 호출자가 헤더와 대조해 판단한다.
		if r.overrun > 0 {
			return nil
		}

		posState := uint32(len(d.out)) & d.posMask
		if r.bit(&d.isMatch[d.state<<posBitsMax+posState]) == 0 {
			d.literal(r)
			d.state = stateAfterLiteral(d.state)
			continue
		}

		var length uint32
		if r.bit(&d.isRep[d.state]) != 0 {
			// A repeated distance: the common case of returning to a place
			// the stream has already referenced.
			//
			// 반복 distance. 스트림이 이미 참조한 자리로 다시 돌아가는,
			// 가장 흔한 경우다.
			if len(d.out) == 0 {
				return fmt.Errorf("lzma: repeat match before any output: %w", ErrCorrupt)
			}
			if r.bit(&d.isRepG0[d.state]) == 0 {
				if r.bit(&d.isRep0Long[d.state<<posBitsMax+posState]) == 0 {
					d.state = stateAfterShortRep(d.state)
					d.out = append(d.out, d.byteAt(d.rep0+1))
					continue
				}
			} else {
				var dist uint32
				if r.bit(&d.isRepG1[d.state]) == 0 {
					dist = d.rep1
				} else {
					if r.bit(&d.isRepG2[d.state]) == 0 {
						dist = d.rep2
					} else {
						dist = d.rep3
						d.rep3 = d.rep2
					}
					d.rep2 = d.rep1
				}
				d.rep1, d.rep0 = d.rep0, dist
			}
			length = d.repLenDec.decode(r, posState)
			d.state = stateAfterRep(d.state)
		} else {
			d.rep3, d.rep2, d.rep1 = d.rep2, d.rep1, d.rep0
			length = d.lenDec.decode(r, posState)
			d.state = stateAfterMatch(d.state)
			d.rep0 = d.distance(r, length)
			if d.rep0 == endMarker {
				if !r.finished() {
					return fmt.Errorf("lzma: end marker at an unaligned position: %w", ErrCorrupt)
				}
				return nil
			}
			if d.rep0 >= d.h.DictSize {
				return fmt.Errorf("lzma: distance %d exceeds the %d byte dictionary: %w", d.rep0, d.h.DictSize, ErrCorrupt)
			}
		}

		length += matchMinLen
		if d.h.SizeKnown {
			if left := d.h.Size - uint64(len(d.out)); uint64(length) > left {
				length = uint32(left)
			}
		}
		if err := d.copyMatch(d.rep0+1, length); err != nil {
			return err
		}
	}
}

// The state transition functions (stateAfterLiteral / Match / Rep / ShortRep)
// live in state.go so the encoder can share them.
//
// 상태 전이 함수들 (stateAfterLiteral / Match / Rep / ShortRep) 은
// state.go 에 있고 인코더와 공유한다.
