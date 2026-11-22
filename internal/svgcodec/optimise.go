package svgcodec

import (
	"bytes"
	"encoding/xml"
	"strings"
)

// staticElement limits semantic rewrites to self-contained, non-scripted artwork.
func staticElement(e xml.StartElement) bool {
	if e.Name.Space != "http://www.w3.org/2000/svg" && e.Name.Space != "" {
		return false
	}
	switch e.Name.Local {
	case "svg", "g", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon", "defs", "linearGradient", "radialGradient", "stop", "clipPath", "mask", "title", "desc":
	default:
		return false
	}
	for _, a := range e.Attr {
		n := a.Name.Local
		if strings.HasPrefix(n, "on") || n == "style" || n == "class" || n == "href" || a.Name.Space != "" && a.Name.Space != "xmlns" {
			return false
		}
		if strings.Contains(a.Value, "url(") && !strings.HasPrefix(strings.TrimSpace(a.Value), "url(#") {
			return false
		}
	}
	return true
}

// appendOptimisedMarkup changes only attribute spellings with exact numeric values.
func appendOptimisedMarkup(out, source []byte) []byte {
	mark := len(out)
	for i := 0; i < len(source); {
		if source[i] != '=' {
			out = append(out, source[i])
			i++
			continue
		}
		end := i
		for end > 0 && space(source[end-1]) {
			end--
		}
		start := end
		for start > 0 && !space(source[start-1]) {
			start--
		}
		name := string(source[start:end])
		out = append(out, '=')
		i++
		for i < len(source) && space(source[i]) {
			i++
		}
		if i == len(source) {
			return append(out[:mark], source...)
		}
		quote := source[i]
		out = append(out, quote)
		i++
		start = i
		for i < len(source) && source[i] != quote {
			i++
		}
		value := source[start:i]
		replacement := value
		switch name {
		case "d":
			replacement = compactNumbers(value, true)
		case "points", "viewBox":
			replacement = compactNumbers(value, false)
		case "fill", "stroke", "stop-color":
			if len(value) == 7 && value[0] == '#' && value[1] == value[2] && value[3] == value[4] && value[5] == value[6] {
				replacement = []byte{'#', value[1], value[3], value[5]}
			}
			if bytes.EqualFold(replacement, []byte("#f00")) {
				replacement = []byte("red")
			}
		}
		if len(replacement) < len(value) {
			out = append(out, replacement...)
		} else {
			out = append(out, value...)
		}
		if i < len(source) {
			out = append(out, quote)
			i++
		}
	}
	return out
}

// compactNumbers preserves every command and number; arcs use their original
// flag grammar. No coordinate arithmetic or precision rounding is performed.
func compactNumbers(value []byte, path bool) []byte {
	if bytes.ContainsAny(value, "aA&") {
		return value
	}
	out := make([]byte, 0, len(value))
	previousNumber := false
	command := byte(0)
	count := 0
	validGroup := func() bool {
		if !path {
			return true
		}
		arity := 0
		switch command {
		case 'M', 'm', 'L', 'l', 'T', 't':
			arity = 2
		case 'H', 'h', 'V', 'v':
			arity = 1
		case 'C', 'c':
			arity = 6
		case 'S', 's', 'Q', 'q':
			arity = 4
		case 'Z', 'z':
			return count == 0
		}
		return arity > 0 && count > 0 && count%arity == 0
	}
	for i := 0; i < len(value); {
		c := value[i]
		if c == ',' {
			if !previousNumber {
				return value
			}
			next := i + 1
			for next < len(value) && space(value[next]) {
				next++
			}
			if next == len(value) || !strings.ContainsRune("+-.0123456789", rune(value[next])) {
				return value
			}
			i++
			continue
		}
		if space(c) {
			i++
			continue
		}
		if path && strings.ContainsRune("MmZzLlHhVvCcSsQqTt", rune(c)) {
			if command == 0 && c != 'M' && c != 'm' || command != 0 && !validGroup() {
				return value
			}
			command = c
			count = 0
			out = append(out, c)
			previousNumber = false
			i++
			continue
		}
		start := i
		if c == '+' || c == '-' {
			i++
		}
		digits := 0
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
			digits++
		}
		if i < len(value) && value[i] == '.' {
			i++
			for i < len(value) && value[i] >= '0' && value[i] <= '9' {
				i++
				digits++
			}
		}
		if digits == 0 {
			return value
		}
		if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
			i++
			if i < len(value) && (value[i] == '+' || value[i] == '-') {
				i++
			}
			exp := i
			for i < len(value) && value[i] >= '0' && value[i] <= '9' {
				i++
			}
			if i == exp {
				return value
			}
		}
		number := decimal(string(value[start:i]))
		count++
		if previousNumber && !(path && number[0] == '-') {
			out = append(out, ' ')
		}
		out = append(out, number...)
		previousNumber = true
	}
	if path && !validGroup() {
		return value
	}
	return out
}

func decimal(s string) string {
	// Exponents retain their spelling to avoid changing browser conversion paths.
	if strings.ContainsAny(s, "eE") {
		return s
	}
	sign := ""
	if s[0] == '+' {
		s = s[1:]
	} else if s[0] == '-' {
		sign = "-"
		s = s[1:]
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		s = "0"
	}
	return sign + s
}
