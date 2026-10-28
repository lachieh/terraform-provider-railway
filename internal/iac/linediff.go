// Line diff for SourceCodeDiffLines, replacing the `similar` crate that
// railwayapp/cli src/iac/functions.rs uses. Ported from similar 2.x
// 2.5.0 (Apache-2.0, Copyright (c) Armin Ronacher): the Myers
// divide-and-conquer algorithm, the Compact hook that slides ambiguous hunks,
// the Replace hook that folds adjacent deletes and inserts, and
// group_diff_ops.

package iac

// diffOp is an equal run (equal, oldLen == newLen) or a change that deletes
// oldLen lines and then inserts newLen lines.
type diffOp struct {
	equal    bool
	oldIndex int
	oldLen   int
	newIndex int
	newLen   int
}

// tokenizeLines splits text into lines that keep their terminator: "\n",
// "\r\n" or a lone "\r". A trailing fragment is a line of its own.
func tokenizeLines(text string) []string {
	var lines []string
	last := 0
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '\r':
			if index+1 < len(text) && text[index+1] == '\n' {
				lines = append(lines, text[last:index+2])
				index++
				last = index + 1
			} else {
				lines = append(lines, text[last:index+1])
				last = index + 1
			}
		case '\n':
			lines = append(lines, text[last:index+1])
			last = index + 1
		}
	}
	if last < len(text) {
		lines = append(lines, text[last:])
	}
	return lines
}

func diffLines(old, new []string) []diffOp {
	size := myersMaxD(len(old), len(new))
	diff := &myers{
		old: old,
		new: new,
		vf:  newMyersV(size),
		vb:  newMyersV(size),
		out: &compactHook{},
	}
	diff.conquer(0, len(old), 0, len(new))
	diff.out.cleanup(diff)
	capture := &replaceHook{}
	for _, op := range diff.out.ops {
		switch op.tag {
		case tagEqual:
			capture.equal(op.oldIndex, op.newIndex, op.oldLen)
		case tagDelete:
			capture.delete(op.oldIndex, op.oldLen, op.newIndex)
		case tagInsert:
			capture.insert(op.oldIndex, op.newIndex, op.newLen)
		}
	}
	capture.finish()
	return capture.ops
}

type myersV struct {
	offset int
	v      []int
}

func newMyersV(maxD int) *myersV {
	return &myersV{offset: maxD, v: make([]int, 2*maxD)}
}

func (v *myersV) at(index int) int         { return v.v[index+v.offset] }
func (v *myersV) set(index int, value int) { v.v[index+v.offset] = value }

func myersMaxD(oldLen, newLen int) int {
	return (oldLen+newLen+1)/2 + 1
}

type myers struct {
	old, new []string
	vf, vb   *myersV
	out      *compactHook
}

func (m *myers) commonPrefixLen(oldStart, oldEnd, newStart, newEnd int) int {
	count := 0
	for oldStart+count < oldEnd && newStart+count < newEnd && m.new[newStart+count] == m.old[oldStart+count] {
		count++
	}
	return count
}

func (m *myers) commonSuffixLen(oldStart, oldEnd, newStart, newEnd int) int {
	count := 0
	for oldEnd-count > oldStart && newEnd-count > newStart && m.new[newEnd-count-1] == m.old[oldEnd-count-1] {
		count++
	}
	return count
}

func (m *myers) findMiddleSnake(oldStart, oldEnd, newStart, newEnd int) (int, int, bool) {
	n := oldEnd - oldStart
	mm := newEnd - newStart
	delta := n - mm
	odd := delta&1 == 1
	m.vf.set(1, 0)
	m.vb.set(1, 0)
	dMax := myersMaxD(n, mm)
	for d := 0; d < dMax; d++ {
		for k := d; k >= -d; k -= 2 {
			var x int
			if k == -d || (k != d && m.vf.at(k-1) < m.vf.at(k+1)) {
				x = m.vf.at(k + 1)
			} else {
				x = m.vf.at(k-1) + 1
			}
			y := x - k
			x0, y0 := x, y
			if x < n && y >= 0 && y < mm {
				x += m.commonPrefixLen(oldStart+x, oldEnd, newStart+y, newEnd)
			}
			m.vf.set(k, x)
			if odd && abs(k-delta) <= d-1 {
				if m.vf.at(k)+m.vb.at(-(k-delta)) >= n {
					return x0 + oldStart, y0 + newStart, true
				}
			}
		}
		for k := d; k >= -d; k -= 2 {
			var x int
			if k == -d || (k != d && m.vb.at(k-1) < m.vb.at(k+1)) {
				x = m.vb.at(k + 1)
			} else {
				x = m.vb.at(k-1) + 1
			}
			y := x - k
			if x < n && y >= 0 && y < mm {
				advance := m.commonSuffixLen(oldStart, oldStart+n-x, newStart, newStart+mm-y)
				x += advance
				y += advance
			}
			m.vb.set(k, x)
			if !odd && abs(k-delta) <= d {
				if m.vb.at(k)+m.vf.at(-(k-delta)) >= n {
					return n - x + oldStart, mm - y + newStart, true
				}
			}
		}
	}
	return 0, 0, false
}

func (m *myers) conquer(oldStart, oldEnd, newStart, newEnd int) {
	prefix := m.commonPrefixLen(oldStart, oldEnd, newStart, newEnd)
	if prefix > 0 {
		m.out.equal(oldStart, newStart, prefix)
	}
	oldStart += prefix
	newStart += prefix

	suffix := m.commonSuffixLen(oldStart, oldEnd, newStart, newEnd)
	suffixOld, suffixNew := oldEnd-suffix, newEnd-suffix
	oldEnd -= suffix
	newEnd -= suffix

	switch {
	case oldStart >= oldEnd && newStart >= newEnd:
	case newStart >= newEnd:
		m.out.delete(oldStart, oldEnd-oldStart, newStart)
	case oldStart >= oldEnd:
		m.out.insert(oldStart, newStart, newEnd-newStart)
	default:
		if xStart, yStart, ok := m.findMiddleSnake(oldStart, oldEnd, newStart, newEnd); ok {
			m.conquer(oldStart, xStart, newStart, yStart)
			m.conquer(xStart, oldEnd, yStart, newEnd)
		} else {
			m.out.delete(oldStart, oldEnd-oldStart, newStart)
			m.out.insert(oldStart, newStart, newEnd-newStart)
		}
	}

	if suffix > 0 {
		m.out.equal(suffixOld, suffixNew, suffix)
	}
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

type opTag int

const (
	tagEqual opTag = iota
	tagDelete
	tagInsert
)

// rawOp is similar's DiffOp before the Replace hook: an equal run (oldLen ==
// newLen), a delete (newLen == 0) or an insert (oldLen == 0).
type rawOp struct {
	tag      opTag
	oldIndex int
	oldLen   int
	newIndex int
	newLen   int
}

func (o rawOp) oldEnd() int   { return o.oldIndex + o.oldLen }
func (o rawOp) newEnd() int   { return o.newIndex + o.newLen }
func (o rawOp) isEmpty() bool { return o.oldLen == 0 && o.newLen == 0 }

func (o *rawOp) adjust(offset, length int) {
	o.oldIndex += offset
	o.newIndex += offset
	switch o.tag {
	case tagEqual:
		o.oldLen += length
		o.newLen += length
	case tagDelete:
		o.oldLen += length
	case tagInsert:
		o.newLen += length
	}
}

func (o *rawOp) shiftLeft(n int)   { o.adjust(-n, 0) }
func (o *rawOp) shiftRight(n int)  { o.adjust(n, 0) }
func (o *rawOp) growLeft(n int)    { o.adjust(-n, n) }
func (o *rawOp) growRight(n int)   { o.adjust(0, n) }
func (o *rawOp) shrinkLeft(n int)  { o.adjust(0, -n) }
func (o *rawOp) shrinkRight(n int) { o.adjust(n, -n) }

func equalOp(oldIndex, newIndex, length int) rawOp {
	return rawOp{tag: tagEqual, oldIndex: oldIndex, oldLen: length, newIndex: newIndex, newLen: length}
}

// compactHook records the Myers edit script, then slides each delete and
// insert up and down to merge it with neighbouring edits.
type compactHook struct {
	ops []rawOp
}

func (h *compactHook) equal(oldIndex, newIndex, length int) {
	h.ops = append(h.ops, equalOp(oldIndex, newIndex, length))
}

func (h *compactHook) delete(oldIndex, oldLen, newIndex int) {
	h.ops = append(h.ops, rawOp{tag: tagDelete, oldIndex: oldIndex, oldLen: oldLen, newIndex: newIndex})
}

func (h *compactHook) insert(oldIndex, newIndex, newLen int) {
	h.ops = append(h.ops, rawOp{tag: tagInsert, oldIndex: oldIndex, newIndex: newIndex, newLen: newLen})
}

func (h *compactHook) cleanup(m *myers) {
	for _, tag := range []opTag{tagDelete, tagInsert} {
		for pointer := 0; pointer < len(h.ops); pointer++ {
			if h.ops[pointer].tag == tag {
				pointer = h.shiftUp(m, pointer)
				pointer = h.shiftDown(m, pointer)
			}
		}
	}
}

func (h *compactHook) remove(index int) {
	h.ops = append(h.ops[:index], h.ops[index+1:]...)
}

func (h *compactHook) insertAt(index int, op rawOp) {
	h.ops = append(h.ops, rawOp{})
	copy(h.ops[index+1:], h.ops[index:])
	h.ops[index] = op
}

func (h *compactHook) shiftUp(m *myers, pointer int) int {
	for pointer >= 1 {
		prev, this := h.ops[pointer-1], h.ops[pointer]
		switch {
		case prev.tag == tagEqual:
			suffix := m.commonSuffixLen(prev.oldIndex, prev.oldEnd(), this.newIndex, this.newEnd())
			if suffix > 0 {
				if pointer+1 < len(h.ops) && h.ops[pointer+1].tag == tagEqual {
					h.ops[pointer+1].growLeft(suffix)
				} else {
					length := suffix
					if this.tag == tagDelete {
						length = prev.oldLen - suffix
					}
					h.insertAt(pointer+1, equalOp(prev.oldEnd()-suffix, this.newEnd()-suffix, length))
				}
				h.ops[pointer].shiftLeft(suffix)
				h.ops[pointer-1].shrinkLeft(suffix)
				if h.ops[pointer-1].isEmpty() {
					h.remove(pointer - 1)
					pointer--
				}
			} else if h.ops[pointer-1].isEmpty() {
				h.remove(pointer - 1)
				pointer--
			} else {
				return pointer
			}
		case prev.tag != this.tag:
			h.ops[pointer-1], h.ops[pointer] = this, prev
			pointer--
		case this.tag == tagInsert:
			h.ops[pointer-1].growRight(this.newLen)
			h.remove(pointer)
			pointer--
		default:
			h.ops[pointer-1].growRight(this.oldLen)
			h.remove(pointer)
			pointer--
		}
	}
	return pointer
}

func (h *compactHook) shiftDown(m *myers, pointer int) int {
	for pointer+1 < len(h.ops) {
		this, next := h.ops[pointer], h.ops[pointer+1]
		switch {
		case next.tag == tagEqual:
			prefix := m.commonPrefixLen(next.oldIndex, next.oldEnd(), this.newIndex, this.newEnd())
			if prefix > 0 {
				if pointer >= 1 && h.ops[pointer-1].tag == tagEqual {
					h.ops[pointer-1].growRight(prefix)
				} else {
					h.insertAt(pointer, equalOp(next.oldIndex, this.newIndex, prefix))
					pointer++
				}
				h.ops[pointer].shiftRight(prefix)
				h.ops[pointer+1].shrinkRight(prefix)
				if h.ops[pointer+1].isEmpty() {
					h.remove(pointer + 1)
				}
			} else if h.ops[pointer+1].isEmpty() {
				h.remove(pointer + 1)
			} else {
				return pointer
			}
		case next.tag != this.tag:
			h.ops[pointer], h.ops[pointer+1] = next, this
			pointer++
		case this.tag == tagInsert:
			h.ops[pointer].growRight(next.newLen)
			h.remove(pointer + 1)
		default:
			h.ops[pointer].growRight(next.oldLen)
			h.remove(pointer + 1)
		}
	}
	return pointer
}

// replaceHook folds a run of deletes and inserts between equal runs into one
// change, and merges adjacent equal runs.
type replaceHook struct {
	ops []diffOp
	del *diffOp
	ins *diffOp
	eq  *diffOp
}

func (h *replaceHook) flushEqual() {
	if h.eq != nil {
		h.ops = append(h.ops, *h.eq)
		h.eq = nil
	}
}

func (h *replaceHook) flushDeleteInsert() {
	switch {
	case h.del != nil && h.ins != nil:
		h.ops = append(h.ops, diffOp{oldIndex: h.del.oldIndex, oldLen: h.del.oldLen, newIndex: h.ins.newIndex, newLen: h.ins.newLen})
	case h.del != nil:
		h.ops = append(h.ops, *h.del)
	case h.ins != nil:
		h.ops = append(h.ops, *h.ins)
	}
	h.del, h.ins = nil, nil
}

func (h *replaceHook) equal(oldIndex, newIndex, length int) {
	h.flushDeleteInsert()
	if h.eq != nil {
		h.eq.oldLen += length
		h.eq.newLen += length
		return
	}
	h.eq = &diffOp{equal: true, oldIndex: oldIndex, oldLen: length, newIndex: newIndex, newLen: length}
}

func (h *replaceHook) delete(oldIndex, oldLen, newIndex int) {
	h.flushEqual()
	if h.del != nil {
		h.del.oldLen += oldLen
		return
	}
	h.del = &diffOp{oldIndex: oldIndex, oldLen: oldLen, newIndex: newIndex}
}

func (h *replaceHook) insert(oldIndex, newIndex, newLen int) {
	h.flushEqual()
	if h.ins != nil {
		h.ins.newLen += newLen
		return
	}
	h.ins = &diffOp{oldIndex: oldIndex, newIndex: newIndex, newLen: newLen}
}

func (h *replaceHook) finish() {
	h.flushEqual()
	h.flushDeleteInsert()
}

// groupDiffOps isolates change clusters, keeping n lines of context around
// each and dropping equal runs longer than that.
func groupDiffOps(ops []diffOp, n int) [][]diffOp {
	if len(ops) == 0 {
		return nil
	}
	ops = append([]diffOp(nil), ops...)
	if first := &ops[0]; first.equal {
		offset := max(first.oldLen-n, 0)
		first.oldIndex += offset
		first.newIndex += offset
		first.oldLen -= offset
		first.newLen -= offset
	}
	if last := &ops[len(ops)-1]; last.equal {
		trim := max(last.oldLen-n, 0)
		last.oldLen -= trim
		last.newLen -= trim
	}

	var groups [][]diffOp
	var pending []diffOp
	for _, op := range ops {
		if op.equal && op.oldLen > n*2 {
			pending = append(pending, diffOp{equal: true, oldIndex: op.oldIndex, oldLen: n, newIndex: op.newIndex, newLen: n})
			groups = append(groups, pending)
			offset := max(op.oldLen-n, 0)
			pending = []diffOp{{
				equal:    true,
				oldIndex: op.oldIndex + offset,
				oldLen:   op.oldLen - offset,
				newIndex: op.newIndex + offset,
				newLen:   op.oldLen - offset,
			}}
			continue
		}
		pending = append(pending, op)
	}
	if len(pending) > 0 && !(len(pending) == 1 && pending[0].equal) {
		groups = append(groups, pending)
	}
	return groups
}
