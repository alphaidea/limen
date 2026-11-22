// Package svgcodec performs exact XML lexical compression without changing SVG values.
package svgcodec

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

// IsSVG recognises an SVG document by its root expanded name.
func IsSVG(input []byte) bool {
	input = bytes.TrimSpace(input)
	if len(input) == 0 || input[0] != '<' {
		return false
	}
	d := xml.NewDecoder(bytes.NewReader(input))
	for {
		t, err := d.Token()
		if err != nil {
			return false
		}
		if e, ok := t.(xml.StartElement); ok {
			return e.Name.Local == "svg" && (e.Name.Space == "" || e.Name.Space == "http://www.w3.org/2000/svg")
		}
	}
}

// Compress retains all character data, comments, attribute values and namespaces.
// Only redundant markup whitespace is removed. DTDs are returned unchanged.
func Compress(input []byte) ([]byte, error) { return CompressWithOptions(input, false) }

// CompressWithOptions enables exact value rewriting for standalone static SVGs.
// Embedded SVGs must use the default mode because host CSS can select raw values.
func CompressWithOptions(input []byte, standalone bool) ([]byte, error) {
	d := xml.NewDecoder(bytes.NewReader(input))
	output := make([]byte, 0, len(input))
	depth, roots := 0, 0
	opaque := false
	static := standalone
	type span struct {
		start, end int
		remove     bool
	}
	var markup []span
	textDepth := 0
	for {
		start := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("svg: %w", err)
		}
		end := int(d.InputOffset())
		switch t := token.(type) {
		case xml.StartElement:
			if textDepth > 0 || t.Name.Local == "title" || t.Name.Local == "desc" {
				textDepth++
			}
			static = static && staticElement(t)
			if depth == 0 {
				roots++
				if roots != 1 || t.Name.Local != "svg" || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
					return nil, fmt.Errorf("svg: invalid root")
				}
			}
			depth++
			mark := len(output)
			output = appendMarkup(output, input[start:end])
			if standalone {
				markup = append(markup, span{mark, len(output), false})
			}
		case xml.EndElement:
			if textDepth > 0 {
				textDepth--
			}
			depth--
			output = appendMarkup(output, input[start:end])
		case xml.Directive:
			opaque = true
			output = append(output, input[start:end]...)
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(t)) != 0 {
				return nil, fmt.Errorf("svg: text outside root")
			}
			mark := len(output)
			output = append(output, input[start:end]...)
			if standalone && textDepth == 0 && len(bytes.TrimSpace(t)) == 0 {
				markup = append(markup, span{mark, len(output), true})
			}
		default:
			if instruction, ok := token.(xml.ProcInst); ok && instruction.Target != "xml" {
				static = false
			}
			output = append(output, input[start:end]...)
		}
	}
	if roots != 1 || depth != 0 {
		return nil, fmt.Errorf("svg: incomplete document")
	}
	if static && !opaque {
		candidate := make([]byte, 0, len(output))
		offset := 0
		for _, mark := range markup {
			candidate = append(candidate, output[offset:mark.start]...)
			if !mark.remove {
				candidate = appendOptimisedMarkup(candidate, output[mark.start:mark.end])
			}
			offset = mark.end
		}
		candidate = append(candidate, output[offset:]...)
		output = candidate
	}
	if opaque || len(output) >= len(input) {
		return input, nil
	}
	return output, nil
}

func space(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func appendMarkup(dst, source []byte) []byte {
	var quote byte
	for i := 0; i < len(source); i++ {
		c := source[i]
		if quote != 0 {
			dst = append(dst, c)
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			dst = append(dst, c)
			continue
		}
		if space(c) {
			for i+1 < len(source) && space(source[i+1]) {
				i++
			}
			if i+1 < len(source) && (source[i+1] == '=' || source[i+1] == '>' || source[i+1] == '/') {
				continue
			}
			if len(dst) > 0 && dst[len(dst)-1] == '=' {
				continue
			}
			dst = append(dst, ' ')
			continue
		}
		dst = append(dst, c)
	}
	return dst
}
