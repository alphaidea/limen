package jpegcodec

import (
	"cmp"
	"slices"
)

type huffman struct {
	counts [16]byte
	values []byte
	codes  [256]uint32 // Canonical code followed by five bits for its length.
	lookup [256]uint16 // Symbol followed by five bits for its length.
	limit  [17]int
	base   [17]int
}

func (table *huffman) build() bool {
	code, index := 0, 0
	for i, count := range table.counts {
		length := i + 1
		table.base[length] = index - code
		table.limit[length] = -1
		if code+int(count) >= 1<<length {
			return false
		}
		for range count {
			symbol := table.values[index]
			if table.codes[symbol] != 0 {
				return false
			}
			table.codes[symbol] = uint32(code<<5 | length)
			if length <= 8 {
				first := code << (8 - length)
				for prefix := first; prefix < first+1<<(8-length); prefix++ {
					table.lookup[prefix] = uint16(int(symbol)<<5 | length)
				}
			}
			code++
			index++
			table.limit[length] = code - 1
		}
		code <<= 1
	}
	return true
}

type huffmanNode struct {
	weight int
	parent int
	symbol byte
}

// optimalTable builds a Huffman tree, then limits lengths to the 16 bits
// allowed by JPEG. A dummy leaf reserves the forbidden all-ones code.
func optimalTable(frequency *[256]int) huffman {
	var nodes [513]huffmanNode
	n := 1
	nodes[0].weight = 1
	for symbol, count := range frequency {
		if count != 0 {
			nodes[n] = huffmanNode{weight: count, symbol: byte(symbol)}
			n++
		}
	}
	slices.SortStableFunc(nodes[1:n], func(a, b huffmanNode) int {
		return cmp.Compare(a.weight, b.weight)
	})

	leaf, branch, end := 0, n, n
	take := func() int {
		if leaf < n && (branch == end || nodes[leaf].weight <= nodes[branch].weight) {
			leaf++
			return leaf - 1
		}
		branch++
		return branch - 1
	}
	for end < 2*n-1 {
		left, right := take(), take()
		nodes[left].parent, nodes[right].parent = end, end
		nodes[end].weight = nodes[left].weight + nodes[right].weight
		end++
	}

	var lengths [257]int
	for i := range n {
		length := 0
		for j := i; nodes[j].parent != 0; j = nodes[j].parent {
			length++
		}
		lengths[length]++
	}
	for length := len(lengths) - 1; length > 16; length-- {
		for lengths[length] != 0 {
			shorter := length - 2
			for lengths[shorter] == 0 {
				shorter--
			}
			lengths[length] -= 2
			lengths[length-1]++
			lengths[shorter]--
			lengths[shorter+1] += 2
		}
	}
	for length := 16; length > 0; length-- {
		if lengths[length] != 0 {
			lengths[length]--
			break
		}
	}

	var table huffman
	for i := range table.counts {
		table.counts[i] = byte(lengths[i+1])
	}
	table.values = make([]byte, n-1)
	for i := range table.values {
		table.values[i] = nodes[n-1-i].symbol
	}
	table.build()
	return table
}
