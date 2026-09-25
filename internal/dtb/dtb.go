// Package dtb reads and writes the flattened device tree (etc/model.dtb).
//
// There is one inside the DSM ramdisk, and it is how the system works out
// where its disks are: every bay is an internal_slot node carrying the PCI
// address and port of a controller. Those addresses belong to a real
// appliance, so on any other hardware every bay reads as empty.
//
// What happens here is narrow. No tree is built from scratch. The original
// that ships with each model already describes that model's bay count, LEDs
// and expansion units correctly, so only the addresses are rewritten.
//
// Package dtb - flattened device tree (etc/model.dtb) 읽기/쓰기.
//
// DSM 램디스크 안에 하나 들어있다. 시스템이 자기 디스크가 어디 있는지
// 알아내는 근거다: 각 베이가 internal_slot 노드이고, 노드에 컨트롤러의 PCI
// 주소와 포트가 적혀 있다. 그 주소들이 실제 어플라이언스 것이라, 다른
// 하드웨어에선 모든 베이가 비어있게 나온다.
//
// 여기서 하는 일은 좁다. dtb 를 새로 만들지 않는다. 모델별로 딸려오는
// 원본이 이미 그 모델의 베이 수, LED, 확장 유닛을 제대로 서술하고 있어서,
// 주소만 다시 쓴다.
package dtb

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Magic is the first word of every flattened device tree.
// Magic - 모든 flattened device tree 의 첫 워드.
const Magic = 0xd00dfeed

const (
	tokenBeginNode = 1
	tokenEndNode   = 2
	tokenProp      = 3
	tokenNop       = 4
	tokenEnd       = 9
)

const (
	headerSize     = 40
	minVersion     = 16
	defaultVersion = 17
)

// Property is one name/value pair. The format leaves the value as untyped
// bytes; device tree convention is what decides whether it is a string, a
// string list or a number.
//
// Property - name/value 쌍 하나. 값은 포맷 상 untyped 바이트다. device tree
// 는 관례로 그 값이 문자열인지, 문자열 리스트인지, 숫자인지를 구분한다.
type Property struct {
	Name  string
	Value []byte
}

// String reads the value as a NUL-terminated string.
// String - 값을 NUL 종료 문자열로 해석.
func (p Property) String() string {
	return strings.TrimRight(string(p.Value), "\x00")
}

// Node is one entry in the tree. The order things appeared in is kept, so a
// tree that is read and written back untouched comes out byte-identical.
//
// Node - 트리 안의 엔트리 하나. 나타난 순서를 유지해서, 손대지 않은 트리는
// 그대로 round-trip 하면 byte-identical 로 나온다.
type Node struct {
	Name     string
	Props    []Property
	Children []*Node
}

// Tree is a complete device tree.
// Tree - 완전한 device tree.
type Tree struct {
	Root        *Node
	ReserveMap  []ReserveEntry
	BootCPUID   uint32
	Version     uint32
	CompVersion uint32
}

// ReserveEntry is a region of memory the kernel must leave alone. DSM's trees
// carry none, but the block itself is part of the format and is preserved
// verbatim.
//
// ReserveEntry - 커널이 손대면 안 되는 메모리 영역. DSM 트리는 이 값이
// 비어있지만, 이 블록 자체는 포맷의 일부이므로 원문 그대로 보존된다.
type ReserveEntry struct {
	Address uint64
	Size    uint64
}

// Prop looks a property up by name.
// Prop - 이름으로 property 조회.
func (n *Node) Prop(name string) (*Property, bool) {
	for i := range n.Props {
		if n.Props[i].Name == name {
			return &n.Props[i], true
		}
	}
	return nil, false
}

// GetString reads a property whose value is a string.
// GetString - string 값 property 조회.
func (n *Node) GetString(name string) (string, bool) {
	p, ok := n.Prop(name)
	if !ok {
		return "", false
	}
	return p.String(), true
}

// SetString writes a string property, adding it when it is not there. The
// trailing NUL is appended here so the caller cannot forget it.
//
// SetString - string 값 property 를 쓴다 (없으면 추가). 끝의 NUL 은 여기서
// 붙여 준다.
func (n *Node) SetString(name, value string) {
	if p, ok := n.Prop(name); ok {
		p.Value = append([]byte(value), 0)
		return
	}
	n.Props = append(n.Props, Property{Name: name, Value: append([]byte(value), 0)})
}

// Child looks up a direct child by exact name.
// Child - 정확한 이름의 직속 자식 조회.
func (n *Node) Child(name string) *Node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// ChildrenWithPrefix returns the direct children whose names start with a
// prefix. It is how numbered nodes such as internal_slot@1 are found.
//
// ChildrenWithPrefix - 이름이 prefix 로 시작하는 직속 자식들. internal_slot@1
// 처럼 번호가 붙은 노드들을 이 방식으로 찾는다.
func (n *Node) ChildrenWithPrefix(prefix string) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// Walk visits every node depth-first, parents before children.
// Walk - 모든 노드를 depth-first 로 방문 (부모 먼저).
func (n *Node) Walk(fn func(path string, node *Node)) {
	n.walk("", fn)
}

func (n *Node) walk(parent string, fn func(string, *Node)) {
	path := parent + "/" + n.Name
	if n.Name == "" && parent == "" {
		path = "/"
	}
	fn(path, n)
	base := path
	if base == "/" {
		base = ""
	}
	for _, c := range n.Children {
		c.walk(base, fn)
	}
}

// Parse reads a flattened device tree.
// Parse - flattened device tree 읽기.
func Parse(data []byte) (*Tree, error) {
	if len(data) < headerSize {
		return nil, fmt.Errorf("dtb: %d bytes is too short for a header", len(data))
	}
	be := binary.BigEndian
	if got := be.Uint32(data); got != Magic {
		return nil, fmt.Errorf("dtb: magic is 0x%08x, expected 0x%08x", got, Magic)
	}
	total := be.Uint32(data[4:])
	if int(total) > len(data) {
		return nil, fmt.Errorf("dtb: header claims %d bytes but only %d are present", total, len(data))
	}
	// Anything past totalsize is padding from whoever stored the tree.
	// totalsize 뒤는 이 트리를 저장한 쪽이 붙인 패딩이다.
	data = data[:total]

	t := &Tree{
		Version:     be.Uint32(data[20:]),
		CompVersion: be.Uint32(data[24:]),
		BootCPUID:   be.Uint32(data[28:]),
	}
	if t.Version < minVersion {
		return nil, fmt.Errorf("dtb: version %d is older than %d and lacks the offsets we need", t.Version, minVersion)
	}

	offStruct := be.Uint32(data[8:])
	offStrings := be.Uint32(data[12:])
	offRsv := be.Uint32(data[16:])
	sizeStrings := be.Uint32(data[32:])
	sizeStruct := be.Uint32(data[36:])

	if int(offStruct+sizeStruct) > len(data) || int(offStrings+sizeStrings) > len(data) {
		return nil, fmt.Errorf("dtb: struct or strings block runs past the end of the tree")
	}
	strs := data[offStrings : offStrings+sizeStrings]

	for off := int(offRsv); off+16 <= len(data); off += 16 {
		addr, size := be.Uint64(data[off:]), be.Uint64(data[off+8:])
		if addr == 0 && size == 0 {
			break
		}
		t.ReserveMap = append(t.ReserveMap, ReserveEntry{addr, size})
	}

	p := &parser{data: data[offStruct : offStruct+sizeStruct], strs: strs}
	root, err := p.parse()
	if err != nil {
		return nil, err
	}
	t.Root = root
	return t, nil
}

type parser struct {
	data []byte
	strs []byte
	pos  int
}

func (p *parser) u32() (uint32, error) {
	if p.pos+4 > len(p.data) {
		return 0, fmt.Errorf("dtb: struct block ended mid-token at offset %d", p.pos)
	}
	v := binary.BigEndian.Uint32(p.data[p.pos:])
	p.pos += 4
	return v, nil
}

func (p *parser) cstr() (string, error) {
	start := p.pos
	for p.pos < len(p.data) && p.data[p.pos] != 0 {
		p.pos++
	}
	if p.pos >= len(p.data) {
		return "", fmt.Errorf("dtb: unterminated name at offset %d", start)
	}
	s := string(p.data[start:p.pos])
	p.pos = align4(p.pos + 1)
	return s, nil
}

func (p *parser) stringAt(off uint32) (string, error) {
	if int(off) >= len(p.strs) {
		return "", fmt.Errorf("dtb: property name offset %d is past the strings block", off)
	}
	rest := p.strs[off:]
	for i, b := range rest {
		if b == 0 {
			return string(rest[:i]), nil
		}
	}
	return "", fmt.Errorf("dtb: unterminated property name at strings offset %d", off)
}

func (p *parser) parse() (*Node, error) {
	var root *Node
	var stack []*Node

	for {
		tok, err := p.u32()
		if err != nil {
			return nil, err
		}
		switch tok {
		case tokenNop:
			continue

		case tokenBeginNode:
			name, err := p.cstr()
			if err != nil {
				return nil, err
			}
			n := &Node{Name: name}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("dtb: a second root node appeared at offset %d", p.pos)
				}
				root = n
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, n)
			}
			stack = append(stack, n)

		case tokenEndNode:
			if len(stack) == 0 {
				return nil, fmt.Errorf("dtb: node end without a matching start at offset %d", p.pos)
			}
			stack = stack[:len(stack)-1]

		case tokenProp:
			length, err := p.u32()
			if err != nil {
				return nil, err
			}
			nameOff, err := p.u32()
			if err != nil {
				return nil, err
			}
			if p.pos+int(length) > len(p.data) {
				return nil, fmt.Errorf("dtb: property value of %d bytes runs past the struct block", length)
			}
			name, err := p.stringAt(nameOff)
			if err != nil {
				return nil, err
			}
			value := append([]byte(nil), p.data[p.pos:p.pos+int(length)]...)
			p.pos = align4(p.pos + int(length))
			if len(stack) == 0 {
				return nil, fmt.Errorf("dtb: property %q appeared outside any node", name)
			}
			cur := stack[len(stack)-1]
			cur.Props = append(cur.Props, Property{Name: name, Value: value})

		case tokenEnd:
			if len(stack) != 0 {
				return nil, fmt.Errorf("dtb: tree ended with %d nodes still open", len(stack))
			}
			if root == nil {
				return nil, fmt.Errorf("dtb: tree contains no root node")
			}
			return root, nil

		default:
			return nil, fmt.Errorf("dtb: unknown token 0x%08x at offset %d", tok, p.pos-4)
		}
	}
}

func align4(n int) int { return (n + 3) &^ 3 }

// Bytes serialises the tree. The strings block is rebuilt from scratch, so a
// caller can rename or add properties without having to think about offsets.
//
// Bytes - 트리 직렬화. strings 블록은 처음부터 다시 구성하므로, 호출자가
// property 이름을 바꾸거나 추가해도 offset 을 신경쓸 필요 없이 그대로 반영된다.
func (t *Tree) Bytes() ([]byte, error) {
	if t.Root == nil {
		return nil, fmt.Errorf("dtb: tree has no root node")
	}
	version := t.Version
	if version == 0 {
		version = defaultVersion
	}
	comp := t.CompVersion
	if comp == 0 {
		comp = minVersion
	}

	s := &strtab{offsets: make(map[string]uint32)}
	var st []byte
	st = writeNode(st, t.Root, s)
	st = appendU32(st, tokenEnd)

	var rsv []byte
	for _, e := range t.ReserveMap {
		rsv = appendU64(rsv, e.Address)
		rsv = appendU64(rsv, e.Size)
	}
	rsv = appendU64(rsv, 0)
	rsv = appendU64(rsv, 0)

	// The reserve map must start on an 8 byte boundary; a 40 byte header
	// already satisfies that.
	//
	// reserve map 은 8 바이트 경계에서 시작해야 하는데, 40 바이트 헤더가
	// 이미 그 조건을 만족한다.
	offRsv := uint32(headerSize)
	offStruct := offRsv + uint32(len(rsv))
	offStrings := offStruct + uint32(len(st))
	total := offStrings + uint32(len(s.data))

	out := make([]byte, headerSize, total)
	be := binary.BigEndian
	be.PutUint32(out[0:], Magic)
	be.PutUint32(out[4:], total)
	be.PutUint32(out[8:], offStruct)
	be.PutUint32(out[12:], offStrings)
	be.PutUint32(out[16:], offRsv)
	be.PutUint32(out[20:], version)
	be.PutUint32(out[24:], comp)
	be.PutUint32(out[28:], t.BootCPUID)
	be.PutUint32(out[32:], uint32(len(s.data)))
	be.PutUint32(out[36:], uint32(len(st)))

	out = append(out, rsv...)
	out = append(out, st...)
	out = append(out, s.data...)
	return out, nil
}

func writeNode(out []byte, n *Node, s *strtab) []byte {
	out = appendU32(out, tokenBeginNode)
	out = append(out, n.Name...)
	out = append(out, 0)
	out = pad4(out)

	for _, p := range n.Props {
		out = appendU32(out, tokenProp)
		out = appendU32(out, uint32(len(p.Value)))
		out = appendU32(out, s.add(p.Name))
		out = append(out, p.Value...)
		out = pad4(out)
	}
	for _, c := range n.Children {
		out = writeNode(out, c, s)
	}
	return appendU32(out, tokenEndNode)
}

// strtab builds the strings block, reusing an offset when a name repeats.
// Property names repeat constantly between sibling nodes, so this keeps a tree
// of near-identical bays from growing for no reason.
//
// strtab - strings 블록을 만든다. 같은 이름이 다시 나오면 offset 을
// 재사용한다. property 이름은 형제 노드 간에 계속 반복되므로, 이걸로 거의
// 동일한 여러 베이가 있는 트리가 불필요하게 커지지 않게 한다.
type strtab struct {
	data    []byte
	offsets map[string]uint32
}

func (s *strtab) add(name string) uint32 {
	if off, ok := s.offsets[name]; ok {
		return off
	}
	off := uint32(len(s.data))
	s.data = append(s.data, name...)
	s.data = append(s.data, 0)
	s.offsets[name] = off
	return off
}

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendU64(b []byte, v uint64) []byte {
	return appendU32(appendU32(b, uint32(v>>32)), uint32(v))
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
