package lzma

// The LZMA range (arithmetic) encoder. It is the exact counterpart of
// rangeDecoder and uses the same probability model, probInit, moveBits and
// topValue.
//
// The algorithm is the old one from the reference implementation (the LZMA
// SDK):
//
//   - low is 64 bits so it can absorb a carry. When the top 8 bits move on, a
//     settled top byte is emitted; while the carry is still undecided the byte
//     is held in cache.
//   - shiftLow carries all of that settle-or-defer logic.
//   - normalize shifts up 8 bits at a time whenever rng drops below topValue.
//
// LZMA range (arithmetic) 인코더. rangeDecoder 와 정확히 짝을 이루며, 같은
// 확률 모델 / probInit / moveBits / topValue 를 쓴다.
//
// 참조 구현(LZMA SDK) 의 오래된 알고리즘 그대로다:
//
//   - low 은 64 비트로 캐리를 흡수한다. 상위 8 비트가 넘어가면 "확정된"
//     상위 바이트를 방출하고, 아직 캐리가 확정 안 됐으면 cache 에 쌓아둔다.
//   - shiftLow 이 그 확정/지연 로직 전부를 담당한다.
//   - normalize 는 rng 가 topValue 아래로 내려갈 때 8 비트씩 밀어 올린다.

type rangeEncoder struct {
	low       uint64
	rng       uint32
	cache     byte
	cacheSize uint64
	out       []byte
}

func (r *rangeEncoder) init() {
	r.low = 0
	r.rng = 0xFFFFFFFF
	r.cache = 0
	r.cacheSize = 1
	r.out = r.out[:0]
}

// shiftLow emits the top byte of low. While it is still undecided the byte is
// held in cache and only the count grows. The moment the carry settles - the
// top byte is not 0xFF and the value went past 32 bits, producing a carry bit -
// every held byte is flushed at once.
//
// shiftLow 은 low 의 최상위 바이트를 방출한다. 아직 확정 안 됐으면 cache 에
// 넣어두고 크기만 늘린다. 캐리가 확정되는 순간(low 상위 바이트가 0xFF 도 아니고
// 32 비트를 넘겨 캐리 비트를 만들어 냈으면) 밀린 바이트들을 한꺼번에 흘려보낸다.
func (r *rangeEncoder) shiftLow() {
	if uint32(r.low) < 0xFF000000 || r.low >= 1<<32 {
		temp := r.cache
		for {
			r.out = append(r.out, temp+byte(r.low>>32))
			temp = 0xFF
			r.cacheSize--
			if r.cacheSize == 0 {
				break
			}
		}
		r.cache = byte((r.low >> 24) & 0xFF)
	}
	r.cacheSize++
	r.low = (r.low << 8) & 0xFFFFFFFF
}

func (r *rangeEncoder) normalize() {
	if r.rng < topValue {
		r.rng <<= 8
		r.shiftLow()
	}
}

// bit encodes one bit through the probability model p. It is the exact inverse
// of rangeDecoder.bit.
//
// bit - 확률 모델 p 를 지나 한 비트를 인코딩한다. rangeDecoder.bit 의 정확한 역.
func (r *rangeEncoder) bit(p *prob, bit uint32) {
	bound := (r.rng >> probBits) * uint32(*p)
	if bit == 0 {
		r.rng = bound
		*p += (1<<probBits - *p) >> moveBits
	} else {
		r.low += uint64(bound)
		r.rng -= bound
		*p -= *p >> moveBits
	}
	r.normalize()
}

// direct emits n unmodelled bits, most significant first. It is used for the
// high part of a distance and is the inverse of rangeDecoder.direct.
//
// direct 는 모델 없는 n 비트를 MSB 부터 방출한다. distance 상위 부분에 쓰이고
// rangeDecoder.direct 의 역이다.
func (r *rangeEncoder) direct(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		r.rng >>= 1
		if (v>>uint(i))&1 == 1 {
			r.low += uint64(r.rng)
		}
		r.normalize()
	}
}

// tree writes a value most significant bit first through a binary tree of
// probabilities. The inverse of rangeDecoder.tree.
//
// tree 는 이진 트리 확률 배열을 지나 값을 MSB 부터 쓴다. rangeDecoder.tree 의 역.
func (r *rangeEncoder) tree(probs []prob, bits int, v uint32) {
	m := uint32(1)
	for i := bits - 1; i >= 0; i-- {
		bit := (v >> uint(i)) & 1
		r.bit(&probs[m], bit)
		m = m<<1 | bit
	}
}

// treeReverse writes a value least significant bit first through the same
// shape of tree. The inverse of rangeDecoder.treeReverse.
//
// treeReverse 는 같은 모양의 트리에 값을 LSB 부터 쓴다.
// rangeDecoder.treeReverse 의 역.
func (r *rangeEncoder) treeReverse(probs []prob, bits int, v uint32) {
	m := uint32(1)
	for i := 0; i < bits; i++ {
		bit := (v >> uint(i)) & 1
		r.bit(&probs[m], bit)
		m = m<<1 | bit
	}
}

// finish flushes the five remaining bytes of low so the decoder arrives at
// code == 0 at the end. It pairs with the decoder's finished() check.
//
// finish 는 남아있는 low 바이트 5 개를 흘려보내서 디코더가 마지막에 code == 0
// 로 도달하도록 한다. 디코더의 finished() 검사와 짝이다.
func (r *rangeEncoder) finish() {
	for i := 0; i < 5; i++ {
		r.shiftLow()
	}
}
