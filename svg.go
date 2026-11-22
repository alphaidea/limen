package limen

// SVG is the Scalable Vector Graphics format.
const SVG Format = "svg"

// StandaloneSVG enables exact numeric and colour spelling reductions for static
// SVG image documents. Do not use for inline SVG affected by host CSS or scripts.
// Documents with styles, scripts, animation or unsupported elements fall back to
// XML lexical compression. Coordinates and path commands retain their precision.
func StandaloneSVG() Option {
	return func(settings *options) { settings.svgStandalone = true }
}
