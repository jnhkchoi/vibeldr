package lzma

import "fmt"

// LZMA codes every decision as a probability-weighted bit. The model for each
// decision is an 11 bit probability that is nudged after every use, so the
// encoder and decoder stay in step without transmitting the model.
//
// LZMA 는 모든 판단을 확률 가중 비트 하나로 인코딩한다. 판단마다의 모델은
// 11 비트 확률이고 쓸 때마다 조금씩 조정되므로, 모델을 전송하지 않아도
// 인코더와 디코더가 같은 상태를 유지한다.
const (
	probBits  = 11
	probInit  = 1 << (probBits - 1) // 1024, i.e. "no idea yet" / 1024, 곧 "아직 모름"
	moveBits  = 5
	topValue  = 1 << 24
	initBytes = 5
)

// prob is one adaptive bit model.
// prob - 적응 비트 모델 하나.
type prob = uint16

func fill(p []prob) {
	for i := range p {
		p[i] = probInit
	}
}

// rangeDecoder is the arithmetic decoder underneath LZMA.
//
// Reading past the end of the input is not an error on its own: the decoder
// always runs a few bytes ahead of the data it has actually used, so a stream
// that ends exactly on its last symbol will still ask for more. The overrun
// flag lets the caller tell a clean end from a truncated one.
//
// rangeDecoder - LZMA 밑에 깔린 산술 디코더.
//
// 입력 끝을 넘어 읽는 것 자체는 오류가 아니다. 디코더는 실제로 소비한
// 데이터보다 늘 몇 바이트 앞서 달리므로, 마지막 심볼에서 정확히 끝나는
// 스트림도 더 달라고 한다. overrun 플래그가 정상 종료와 잘림을 구분해 준다.
type rangeDecoder struct {
	in      []byte
	pos     int
	rng     uint32
	code    uint32
	overrun int
}

func (r *rangeDecoder) next() uint32 {
	if r.pos >= len(r.in) {
		r.overrun++
		return 0
	}
	b := r.in[r.pos]
	r.pos++
	return uint32(b)
}

func (r *rangeDecoder) init() error {
	if len(r.in) < initBytes {
		return fmt.Errorf("lzma: stream has %d bytes, needs at least %d", len(r.in), initBytes)
	}
	// The first byte is padding produced by the encoder and is always zero.
	// 첫 바이트는 인코더가 넣는 패딩이고 항상 0 이다.
	if r.in[0] != 0 {
		return fmt.Errorf("lzma: first stream byte is 0x%02x, expected 0x00: %w", r.in[0], ErrCorrupt)
	}
	r.pos = 1
	r.rng = 0xFFFFFFFF
	for i := 0; i < 4; i++ {
		r.code = r.code<<8 | r.next()
	}
	return nil
}

func (r *rangeDecoder) normalize() {
	if r.rng < topValue {
		r.rng <<= 8
		r.code = r.code<<8 | r.next()
	}
}

func (r *rangeDecoder) bit(p *prob) uint32 {
	bound := (r.rng >> probBits) * uint32(*p)
	var bit uint32
	if r.code < bound {
		// The bit was the likely one, so make it likelier still.
		// 나온 비트가 확률 높은 쪽이었으니 그쪽을 더 높인다.
		*p += (1<<probBits - *p) >> moveBits
		r.rng = bound
	} else {
		*p -= *p >> moveBits
		r.code -= bound
		r.rng -= bound
		bit = 1
	}
	r.normalize()
	return bit
}

// direct decodes n bits that carry no model, used for the high part of a long
// match distance where the values are close to uniform.
//
// direct - 모델이 붙지 않는 n 비트를 디코드한다. 값이 거의 균등한 긴 매치
// distance 의 상위 부분에 쓴다.
func (r *rangeDecoder) direct(n int) uint32 {
	var res uint32
	for ; n > 0; n-- {
		r.rng >>= 1
		r.code -= r.rng
		t := 0 - (r.code >> 31) // all ones when the subtraction went negative / 뺄셈이 음수면 전부 1
		r.code += r.rng & t
		r.normalize()
		res = res<<1 + t + 1
	}
	return res
}

// tree decodes a value most-significant bit first through a binary tree of
// models, one model per node.
//
// tree - 노드마다 모델 하나를 둔 이진 트리를 지나며 값을 MSB 부터 디코드한다.
func (r *rangeDecoder) tree(probs []prob, bits int) uint32 {
	m := uint32(1)
	for i := 0; i < bits; i++ {
		m = m<<1 + r.bit(&probs[m])
	}
	return m - 1<<bits
}

// treeReverse walks the same shape of tree but emits bits least-significant
// first, which is how distance low bits and the alignment field are stored.
//
// treeReverse - 같은 모양의 트리를 지나되 LSB 부터 비트를 낸다. distance 의
// 하위 비트와 alignment 필드가 그렇게 저장된다.
func (r *rangeDecoder) treeReverse(probs []prob, bits int) uint32 {
	m, sym := uint32(1), uint32(0)
	for i := 0; i < bits; i++ {
		b := r.bit(&probs[m])
		m = m<<1 + b
		sym |= b << i
	}
	return sym
}

// finished reports whether the stream ended on an exact boundary, which is the
// only state in which a trailing end marker is meaningful.
//
// finished - 스트림이 정확한 경계에서 끝났는지. 끝에 붙은 end marker 가
// 의미를 갖는 유일한 상태다.
func (r *rangeDecoder) finished() bool { return r.code == 0 }
