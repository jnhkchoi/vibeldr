package lzma

import (
	"encoding/binary"
	"fmt"
)

// Encode compresses raw data into an LZMA "alone" stream.
//
// Pure Go, with no xz binary involved, so it works inside a statically linked
// boot environment such as cmd/vibeldr-boot.
//
// preset is an xz style strength string ("9e" and so on), but there are only
// two real profiles. Every plain preset gives lc=3, lp=0, pb=2, dict=64MB: the
// original DSM zImage always uses that header, and kpatch.Rebuild has to write
// the recompressed result back into the same slot, so the header matches the
// original. "9-pb1" is the same with pb=1, for a kernel whose result would not
// fit otherwise. An unrecognised preset is an error.
//
// The compression ratio is worse than xz-9e - a hash-chain greedy parser,
// measured at roughly 1.5 to 2x. Recompressing vmlinux only has to fit inside
// the original size, so that is enough.
//
// Encode 는 raw 데이터를 LZMA "alone" 스트림으로 인코딩한다.
//
// 순수 Go 구현이라 `xz` 바이너리 없이 돈다. cmd/vibeldr-boot 처럼 정적으로
// 링크된 부팅 환경에서도 그대로 동작한다.
//
// preset 은 xz 스타일 강도 문자열 ("9e" 등) 이지만 실제 프로파일은 둘뿐이다.
// 일반 preset 은 모두 lc=3, lp=0, pb=2, dict=64MB 다. DSM zImage 원본이 늘 이
// 헤더를 쓰고, kpatch.Rebuild 는 재압축한 결과를 자리에 그대로 덮어써야 하므로
// 헤더가 원본과 같다. "9-pb1" 은 pb=1 만 다르고, 그렇지 않으면 칸에 안 들어가는
// 커널용이다. preset 이 인식되지 않으면 에러다.
//
// 압축률은 xz-9e 보다 나쁘다 (해시 체인 그리디 파서, 실측 1.5~2x 정도).
// vmlinux 를 재압축할 때 원본 크기 안에만 들어가면 되므로 충분하다.
func Encode(raw []byte, preset string) ([]byte, error) {
	p, err := presetFor(preset)
	if err != nil {
		return nil, err
	}

	// The 13 byte alone header: properties + dictSize (uint32 LE) + size
	// (uint64 LE).
	//
	// 13 바이트 alone 헤더: properties + dictSize(uint32 LE) + size(uint64 LE).
	var hdr [HeaderSize]byte
	hdr[0] = byte((p.PB*5+p.LP)*9 + p.LC)
	binary.LittleEndian.PutUint32(hdr[1:5], p.DictSize)
	binary.LittleEndian.PutUint64(hdr[5:13], SizeUnknown)

	e := newEncoder(p)
	if err := e.encode(raw); err != nil {
		return nil, err
	}

	out := make([]byte, 0, HeaderSize+len(e.rng.out))
	out = append(out, hdr[:]...)
	out = append(out, e.rng.out...)
	return out, nil
}

// Preset holds the encoder tuning parameters.
// Preset 은 인코더 튜닝 파라미터.
type Preset struct {
	LC, LP, PB int
	DictSize   uint32
	NiceLen    int // stop searching once a match is at least this long / 이 길이 이상이면 매치 검색을 그만둔다
	ChainDepth int // how many steps of the hash chain to walk / 해시 체인을 최대 몇 스텝까지 걷는지
}

// presetFor maps an xz style name onto a real profile. The only use here is
// recompressing vmlinux, so every plain name gives the one profile whose header
// matches the original, and "9-pb1" gives the pb=1 variant.
//
// presetFor 은 xz 스타일 이름을 실제 프로파일로 매핑한다. 쓰임이 vmlinux 재압축
// 하나뿐이라서, 일반 이름은 모두 원본과 헤더가 맞는 프로파일 하나로 가고 "9-pb1"
// 만 pb=1 변형으로 간다.
func presetFor(name string) (Preset, error) {
	switch name {
	case "", "1", "1e", "3", "3e", "6", "6e", "9", "9e":
		return Preset{
			LC: 3, LP: 0, PB: 2,
			DictSize:   64 << 20, // 64 MiB
			NiceLen:    matchMaxLen,
			ChainDepth: 32,
		}, nil
	case "9-pb1":
		// pb=1 comes out smaller than pb=2 on this kernel data. It is for
		// models whose result overflows the original slot (DS3622xs+). The
		// kernel's unlzma reads pb from the header, so it decompresses as-is.
		//
		// pb=1: 이 커널 데이터에선 pb=2 보다 작게 나온다. 원본 칸을 넘치는
		// 모델(DS3622xs+)을 담을 때 쓴다. 커널 unlzma 는 헤더의 pb 를 읽어
		// 처리하므로 그대로 해제된다.
		return Preset{
			LC: 3, LP: 0, PB: 1,
			DictSize:   64 << 20,
			NiceLen:    matchMaxLen,
			ChainDepth: 32,
		}, nil
	}
	return Preset{}, fmt.Errorf("lzma: unknown preset %q", name)
}

const (
	matchMaxLen = 273 // 2 + 271: the top length code is 16+255, not 8+8+256 / 2 + 271: 최대 길이 코드는 8+8+256 이 아니라 16+255
	hashBits    = 20
	hashSize    = 1 << hashBits
)

// lenEncoder is the inverse of lenDecoder: low and mid per posState, high
// shared.
//
// lenEncoder 는 lenDecoder 의 역이다. posState 별로 low/mid 를 두고 high 는
// 공유한다.
type lenEncoder struct {
	choice, choice2 prob
	low             [1 << posBitsMax][8]prob
	mid             [1 << posBitsMax][8]prob
	high            [256]prob
}

func (l *lenEncoder) reset() {
	l.choice, l.choice2 = probInit, probInit
	for i := range l.low {
		fill(l.low[i][:])
		fill(l.mid[i][:])
	}
	fill(l.high[:])
}

// encode writes a length code: the real length minus matchMinLen, so 0..271.
// encode 는 길이 코드 (실제 길이 - matchMinLen, 즉 0..271) 를 쓴다.
func (l *lenEncoder) encode(rng *rangeEncoder, lenCode uint32, posState uint32) {
	if lenCode < 8 {
		rng.bit(&l.choice, 0)
		rng.tree(l.low[posState][:], 3, lenCode)
		return
	}
	rng.bit(&l.choice, 1)
	if lenCode < 16 {
		rng.bit(&l.choice2, 0)
		rng.tree(l.mid[posState][:], 3, lenCode-8)
		return
	}
	rng.bit(&l.choice2, 1)
	rng.tree(l.high[:], 8, lenCode-16)
}

// encoder holds the encoding state machine and the whole probability model. It
// mirrors decoder, so the array sizes and names are kept identical.
//
// encoder 는 인코딩 스테이트 머신과 확률 모델 전부를 담는다. decoder 와 같은
// 모양이라 배열 크기·이름을 그대로 유지한다.
type encoder struct {
	lc, lp, pb int
	dictSize   uint32
	posMask    uint32
	litPosMask uint32
	niceLen    int
	chainDepth int

	rng rangeEncoder

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

	lenEnc    lenEncoder
	repLenEnc lenEncoder
}

func newEncoder(p Preset) *encoder {
	e := &encoder{
		lc:         p.LC,
		lp:         p.LP,
		pb:         p.PB,
		dictSize:   p.DictSize,
		posMask:    1<<uint(p.PB) - 1,
		litPosMask: 1<<uint(p.LP) - 1,
		niceLen:    p.NiceLen,
		chainDepth: p.ChainDepth,
	}
	e.reset()
	return e
}

func (e *encoder) reset() {
	e.state = 0
	e.rep0, e.rep1, e.rep2, e.rep3 = 0, 0, 0, 0
	fill(e.isMatch[:])
	fill(e.isRep[:])
	fill(e.isRepG0[:])
	fill(e.isRepG1[:])
	fill(e.isRepG2[:])
	fill(e.isRep0Long[:])
	for i := range e.posSlot {
		fill(e.posSlot[i][:])
	}
	fill(e.posDecoders[:])
	fill(e.align[:])
	e.litProbs = make([]prob, 0x300<<uint(e.lc+e.lp))
	fill(e.litProbs)
	e.lenEnc.reset()
	e.repLenEnc.reset()
}

// literal writes one byte. The inverse of decoder.literal.
//
// With state >= 7 (just after a match) the byte one rep0 back is taken as the
// prediction, and each bit moves between two probability subsets on the
// assumption that this is the byte. From the moment the prediction is wrong,
// the remaining bits are written through the unpredicted path.
//
// literal 은 한 바이트를 쓴다. decoder.literal 의 역이다.
//
// state >= 7 (매치 뒤) 이면 rep0 뒤에 있는 바이트를 예측 후보로 삼아 비트마다
// "이 바이트일 것" 이라고 두 개의 확률 서브셋을 넘나든다. 예측이 어긋난 순간부터
// 남은 비트는 예측 없는 경로로 이어 쓴다.
func (e *encoder) literal(data []byte, pos int) {
	var prev uint32
	if pos > 0 {
		prev = uint32(data[pos-1])
	}
	litState := (uint32(pos)&e.litPosMask)<<uint(e.lc) + prev>>(8-uint(e.lc))
	probs := e.litProbs[0x300*litState:]

	b := uint32(data[pos])
	sym := uint32(1)

	if e.state >= 7 {
		mb := uint32(data[pos-int(e.rep0)-1])
		for i := 7; i >= 0; i-- {
			matchBit := (mb >> uint(i)) & 1
			bit := (b >> uint(i)) & 1
			e.rng.bit(&probs[(1+matchBit)<<8+sym], bit)
			sym = sym<<1 | bit
			if matchBit != bit {
				for j := i - 1; j >= 0; j-- {
					bit2 := (b >> uint(j)) & 1
					e.rng.bit(&probs[sym], bit2)
					sym = sym<<1 | bit2
				}
				return
			}
		}
		return
	}

	for i := 7; i >= 0; i-- {
		bit := (b >> uint(i)) & 1
		e.rng.bit(&probs[sym], bit)
		sym = sym<<1 | bit
	}
}

// writeDistance writes the distance part of a new-distance match - the slot and
// its tail. The inverse of decoder.distance. dist is in stored form (the real
// distance minus 1) and lenCode is for computing lenState (the real length
// minus matchMinLen).
//
// writeDistance 는 새-distance 매치의 거리부(슬롯 + 꼬리) 를 쓴다.
// decoder.distance 의 역이다. dist 는 저장 형식 (실제 거리 - 1) 이고,
// lenCode 는 lenState 계산용 (실제 길이 - matchMinLen) 이다.
func (e *encoder) writeDistance(dist uint32, lenCode uint32) {
	lenState := lenCode
	if lenState > lenToPosStates-1 {
		lenState = lenToPosStates - 1
	}

	var slot uint32
	if dist < 4 {
		slot = dist
	} else {
		n := uint32(31)
		for (dist>>n)&1 == 0 {
			n--
		}
		slot = 2*n + ((dist >> (n - 1)) & 1)
	}
	e.rng.tree(e.posSlot[lenState][:], 6, slot)

	if slot < 4 {
		return
	}
	directBits := int(slot>>1) - 1
	base := (uint32(2) | (slot & 1)) << uint(directBits)
	tail := dist - base

	if slot < endPosModelIndex {
		e.rng.treeReverse(e.posDecoders[base-slot:], directBits, tail)
	} else {
		high := tail >> alignBits
		low := tail & (1<<alignBits - 1)
		e.rng.direct(high, directBits-alignBits)
		e.rng.treeReverse(e.align[:], alignBits, low)
	}
}

func (e *encoder) encodeLiteral(data []byte, pos int, posState uint32) {
	e.rng.bit(&e.isMatch[e.state<<posBitsMax+posState], 0)
	e.literal(data, pos)
	e.state = stateAfterLiteral(e.state)
}

// encodeMatch writes a new distance and a length. realLen is the real number of
// bytes copied (at least matchMinLen) and dist is the real distance
// (pos - matchPos).
//
// encodeMatch 는 새 distance 와 길이를 쓴다. realLen 은 실제 복사 바이트 수
// (matchMinLen 이상) 이고, dist 는 실제 거리 (pos - matchPos) 다.
func (e *encoder) encodeMatch(dist uint32, realLen int, posState uint32) {
	e.rng.bit(&e.isMatch[e.state<<posBitsMax+posState], 1)
	e.rng.bit(&e.isRep[e.state], 0)
	lenCode := uint32(realLen) - matchMinLen
	e.lenEnc.encode(&e.rng, lenCode, posState)
	e.writeDistance(dist-1, lenCode)

	e.rep3, e.rep2, e.rep1 = e.rep2, e.rep1, e.rep0
	e.rep0 = dist - 1
	e.state = stateAfterMatch(e.state)
}

// encodeRep reuses rep cache slot repIdx (0..3). realLen is the number of bytes
// copied. With repIdx == 0 and realLen == 1 it is written as a short rep
// (isRep0Long = 0), which is cheaper than a literal.
//
// encodeRep 는 rep 캐시 슬롯 repIdx (0..3) 을 재사용한다. realLen 은 복사
// 바이트 수다. repIdx==0 이고 realLen==1 이면 short-rep (isRep0Long=0) 으로
// 쓴다. 리터럴보다 싸다.
func (e *encoder) encodeRep(repIdx int, realLen int, posState uint32) {
	e.rng.bit(&e.isMatch[e.state<<posBitsMax+posState], 1)
	e.rng.bit(&e.isRep[e.state], 1)

	if repIdx == 0 {
		e.rng.bit(&e.isRepG0[e.state], 0)
		if realLen == 1 {
			e.rng.bit(&e.isRep0Long[e.state<<posBitsMax+posState], 0)
			e.state = stateAfterShortRep(e.state)
			return
		}
		e.rng.bit(&e.isRep0Long[e.state<<posBitsMax+posState], 1)
	} else {
		e.rng.bit(&e.isRepG0[e.state], 1)
		var dist uint32
		if repIdx == 1 {
			e.rng.bit(&e.isRepG1[e.state], 0)
			dist = e.rep1
		} else {
			e.rng.bit(&e.isRepG1[e.state], 1)
			if repIdx == 2 {
				e.rng.bit(&e.isRepG2[e.state], 0)
				dist = e.rep2
			} else {
				e.rng.bit(&e.isRepG2[e.state], 1)
				dist = e.rep3
				e.rep3 = e.rep2
			}
			e.rep2 = e.rep1
		}
		e.rep1, e.rep0 = e.rep0, dist
	}

	e.repLenEnc.encode(&e.rng, uint32(realLen)-matchMinLen, posState)
	e.state = stateAfterRep(e.state)
}

// encodeEndMarker writes the special match (dist = 0xFFFFFFFF, len 2) that
// marks the end of the stream. The decoder stops when it meets
// rep0 == endMarker.
//
// encodeEndMarker 는 특수 매치 (dist = 0xFFFFFFFF, len 2) 를 써서 스트림
// 종료를 표시한다. 디코더가 rep0 == endMarker 를 만나 종료한다.
func (e *encoder) encodeEndMarker(posState uint32) {
	e.rng.bit(&e.isMatch[e.state<<posBitsMax+posState], 1)
	e.rng.bit(&e.isRep[e.state], 0)
	e.lenEnc.encode(&e.rng, 0, posState) // real length 2, the minimum / 실제 길이 2 (최소)
	e.writeDistance(0xFFFFFFFF, 0)
}

// encode runs the LZ parse and feeds the raw data into the range encoder.
// encode 는 LZ 파싱을 돌려 raw 데이터를 range 인코더에 넣는다.
func (e *encoder) encode(data []byte) error {
	e.rng.init()
	e.reset()

	mf := newMatchFinder(data, int(e.dictSize), e.chainDepth)

	pos := 0
	for pos < len(data) {
		posState := uint32(pos) & e.posMask
		remaining := len(data) - pos

		// 1) rep0..rep3 candidates. The distance is already in the stream, so
		//    the code for it is cheap.
		//
		// 1) rep0..rep3 매치 후보 - 이미 스트림에 있는 거리라 코드가 싸다.
		bestRepLen, bestRepIdx := findBestRep(data, pos, e.rep0, e.rep1, e.rep2, e.rep3)

		// 2) New-distance candidates, from the hash chain.
		// 2) 새 distance 매치 후보 (해시 체인).
		matchDist, matchLen := 0, 0
		if remaining >= 4 {
			matchDist, matchLen = mf.findBest(pos, 4)
		}

		// Decide: kind = 0 literal, 1 match, 2 rep. The default is one literal
		// byte.
		//
		// 결정: kind = 0(lit) / 1(match) / 2(rep). 기본은 리터럴 1 바이트.
		kind, emitLen := 0, 1
		emitDist, emitRep := 0, 0

		if bestRepLen >= 2 {
			kind, emitLen, emitRep = 2, bestRepLen, bestRepIdx
		} else if bestRepIdx == 0 && bestRepLen == 1 {
			// short-rep0 candidate, cheaper than a literal. Used unless a
			// match is better.
			//
			// short-rep0 후보 - 리터럴보다 싸다. 매치가 더 좋지 않으면 쓴다.
			kind, emitLen, emitRep = 2, 1, 0
		}

		if matchLen >= 4 && matchLen > emitLen {
			kind, emitLen, emitDist = 1, matchLen, matchDist
		}

		// Emit the symbol.
		// 심볼을 발행한다.
		switch kind {
		case 0:
			e.encodeLiteral(data, pos, posState)
		case 1:
			e.encodeMatch(uint32(emitDist), emitLen, posState)
		case 2:
			e.encodeRep(emitRep, emitLen, posState)
		}

		// Update the hash chain: every position consumed this round goes into
		// the table, so that later positions can reference them. That holds
		// even when a match was consumed - the search is skipped, the insert
		// is not.
		//
		// 해시 체인 갱신: 이번에 소비한 모든 위치를 hash 테이블에 넣어둔다
		// (그래야 이후 위치가 이 위치들을 참조 가능). 매치를 소비했더라도
		// 마찬가지다. 검색만 안 하고 삽입은 한다.
		for i := 0; i < emitLen; i++ {
			mf.insert(pos + i)
		}
		pos += emitLen
	}

	// End marker (dist = 0xFFFFFFFF). A SizeUnknown stream marks its end here.
	// End marker (dist=0xFFFFFFFF). SizeUnknown 스트림은 이걸로 끝을 표시한다.
	posState := uint32(pos) & e.posMask
	e.encodeEndMarker(posState)

	e.rng.finish()
	return nil
}

// findBestRep returns the longest rep0..rep3 match starting at pos. A result of
// (0, 0) means no match.
//
// findBestRep 은 pos 에서 시작하는 rep0..rep3 매치 중 가장 긴 것을 돌려준다.
// 반환 값이 (0, 0) 이면 매치가 없다는 뜻이다.
func findBestRep(data []byte, pos int, rep0, rep1, rep2, rep3 uint32) (int, int) {
	if pos == 0 {
		return 0, 0
	}
	reps := [4]uint32{rep0, rep1, rep2, rep3}
	bestLen, bestIdx := 0, 0
	maxL := len(data) - pos
	if maxL > matchMaxLen {
		maxL = matchMaxLen
	}
	for i, r := range reps {
		d := int(r) + 1
		if d > pos {
			continue
		}
		// Skip when the first byte differs - the common case, rejected fast.
		// 첫 바이트가 안 맞으면 스킵 (가장 흔한 경우 빠르게 걸러낸다).
		if data[pos] != data[pos-d] {
			continue
		}
		l := 1
		for l < maxL && data[pos+l] == data[pos-d+l] {
			l++
		}
		if l > bestLen {
			bestLen = l
			bestIdx = i
		}
	}
	return bestLen, bestIdx
}

// matchFinder is a hash-4 chain over a dictSize sliding window. hashTable holds
// the last position each 4-byte prefix appeared at, and earlier appearances are
// linked through prev.
//
// matchFinder 는 dictSize 슬라이딩 창 위의 hash-4 체인이다. 각 4-바이트
// 프리픽스가 마지막에 나타났던 위치를 hashTable 에 두고, 그 앞에 나타난
// 것들은 prev 로 이어진다.
type matchFinder struct {
	data      []byte
	hashTable []int32
	prev      []int32
	depth     int
	dictSize  int
}

func newMatchFinder(data []byte, dictSize, depth int) *matchFinder {
	mf := &matchFinder{
		data:      data,
		hashTable: make([]int32, hashSize),
		prev:      make([]int32, len(data)+1),
		depth:     depth,
		dictSize:  dictSize,
	}
	for i := range mf.hashTable {
		mf.hashTable[i] = -1
	}
	return mf
}

func hash4(data []byte, p int) uint32 {
	v := uint32(data[p]) | uint32(data[p+1])<<8 | uint32(data[p+2])<<16 | uint32(data[p+3])<<24
	return (v * 2654435761) >> (32 - hashBits)
}

func (mf *matchFinder) insert(pos int) {
	if pos+4 > len(mf.data) {
		return
	}
	h := hash4(mf.data, pos)
	mf.prev[pos] = mf.hashTable[h]
	mf.hashTable[h] = int32(pos)
}

// findBest returns the longest match starting at pos as (dist, len), where
// dist is pos - matchPos. Anything shorter than minLen is ignored, and len == 0
// means no match.
//
// findBest 는 pos 에서 시작하는 최장 매치를 (dist, len) 로 돌려준다.
// dist 는 pos - matchPos 다. minLen 미만은 무시하고, len==0 은 매치 없음이다.
func (mf *matchFinder) findBest(pos int, minLen int) (int, int) {
	if pos+4 > len(mf.data) {
		return 0, 0
	}
	limit := pos - mf.dictSize
	if limit < 0 {
		limit = 0
	}
	maxLen := len(mf.data) - pos
	if maxLen > matchMaxLen {
		maxLen = matchMaxLen
	}

	h := hash4(mf.data, pos)
	cur := int(mf.hashTable[h])

	bestLen := minLen - 1
	if bestLen < 3 {
		bestLen = 3 // the hash is 4 bytes, so the shortest match is 4 / 최소 매치 4
	}
	bestDist := 0

	for depth := mf.depth; cur >= limit && depth > 0; depth-- {
		// Early out: if the byte at the current bestLen differs, this
		// candidate cannot extend any further.
		//
		// 조기 종료: 현재 bestLen 위치의 바이트가 다르면 이 후보로는 더
		// 못 늘린다.
		if cur+bestLen < len(mf.data) && mf.data[cur+bestLen] == mf.data[pos+bestLen] {
			l := 0
			for l < maxLen && mf.data[pos+l] == mf.data[cur+l] {
				l++
			}
			if l > bestLen {
				bestLen = l
				bestDist = pos - cur
				if l >= maxLen {
					break
				}
			}
		}
		cur = int(mf.prev[cur])
	}

	if bestDist == 0 {
		return 0, 0
	}
	return bestDist, bestLen
}
