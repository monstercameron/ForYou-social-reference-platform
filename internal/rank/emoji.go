package rank

import "unicode"

// styleEmojiRanges is the fixed fy-rubric-1 approximation of Unicode 15 Extended_Pictographic without skin-tone modifiers or regional indicators.
var styleEmojiRanges = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x00a9, Hi: 0x00a9, Stride: 1}, {Lo: 0x00ae, Hi: 0x00ae, Stride: 1},
		{Lo: 0x203c, Hi: 0x203c, Stride: 1}, {Lo: 0x2049, Hi: 0x2049, Stride: 1},
		{Lo: 0x2122, Hi: 0x2122, Stride: 1}, {Lo: 0x2139, Hi: 0x2139, Stride: 1},
		{Lo: 0x2194, Hi: 0x2199, Stride: 1}, {Lo: 0x21a9, Hi: 0x21aa, Stride: 1},
		{Lo: 0x231a, Hi: 0x231b, Stride: 1}, {Lo: 0x2328, Hi: 0x2328, Stride: 1},
		{Lo: 0x2388, Hi: 0x2388, Stride: 1}, {Lo: 0x23cf, Hi: 0x23cf, Stride: 1},
		{Lo: 0x23e9, Hi: 0x23f3, Stride: 1}, {Lo: 0x23f8, Hi: 0x23fa, Stride: 1},
		{Lo: 0x24c2, Hi: 0x24c2, Stride: 1}, {Lo: 0x25aa, Hi: 0x25ab, Stride: 1},
		{Lo: 0x25b6, Hi: 0x25b6, Stride: 1}, {Lo: 0x25c0, Hi: 0x25c0, Stride: 1},
		{Lo: 0x25fb, Hi: 0x25fe, Stride: 1}, {Lo: 0x2600, Hi: 0x2605, Stride: 1},
		{Lo: 0x2607, Hi: 0x2612, Stride: 1}, {Lo: 0x2614, Hi: 0x2685, Stride: 1},
		{Lo: 0x2690, Hi: 0x2705, Stride: 1}, {Lo: 0x2708, Hi: 0x2712, Stride: 1},
		{Lo: 0x2714, Hi: 0x2714, Stride: 1}, {Lo: 0x2716, Hi: 0x2716, Stride: 1},
		{Lo: 0x271d, Hi: 0x271d, Stride: 1}, {Lo: 0x2721, Hi: 0x2721, Stride: 1},
		{Lo: 0x2728, Hi: 0x2728, Stride: 1}, {Lo: 0x2733, Hi: 0x2734, Stride: 1},
		{Lo: 0x2744, Hi: 0x2744, Stride: 1}, {Lo: 0x2747, Hi: 0x2747, Stride: 1},
		{Lo: 0x274c, Hi: 0x274c, Stride: 1}, {Lo: 0x274e, Hi: 0x274e, Stride: 1},
		{Lo: 0x2753, Hi: 0x2755, Stride: 1}, {Lo: 0x2757, Hi: 0x2757, Stride: 1},
		{Lo: 0x2763, Hi: 0x2767, Stride: 1}, {Lo: 0x2795, Hi: 0x2797, Stride: 1},
		{Lo: 0x27a1, Hi: 0x27a1, Stride: 1}, {Lo: 0x27b0, Hi: 0x27b0, Stride: 1},
		{Lo: 0x27bf, Hi: 0x27bf, Stride: 1}, {Lo: 0x2934, Hi: 0x2935, Stride: 1},
		{Lo: 0x2b05, Hi: 0x2b07, Stride: 1}, {Lo: 0x2b1b, Hi: 0x2b1c, Stride: 1},
		{Lo: 0x2b50, Hi: 0x2b50, Stride: 1}, {Lo: 0x2b55, Hi: 0x2b55, Stride: 1},
		{Lo: 0x3030, Hi: 0x3030, Stride: 1}, {Lo: 0x303d, Hi: 0x303d, Stride: 1},
		{Lo: 0x3297, Hi: 0x3297, Stride: 1}, {Lo: 0x3299, Hi: 0x3299, Stride: 1},
	},
	R32: []unicode.Range32{
		{Lo: 0x1f000, Hi: 0x1f0ff, Stride: 1}, {Lo: 0x1f10d, Hi: 0x1f10f, Stride: 1},
		{Lo: 0x1f12f, Hi: 0x1f12f, Stride: 1}, {Lo: 0x1f16c, Hi: 0x1f171, Stride: 1},
		{Lo: 0x1f17e, Hi: 0x1f17f, Stride: 1}, {Lo: 0x1f18e, Hi: 0x1f18e, Stride: 1},
		{Lo: 0x1f191, Hi: 0x1f19a, Stride: 1}, {Lo: 0x1f1ad, Hi: 0x1f1e5, Stride: 1},
		{Lo: 0x1f201, Hi: 0x1f20f, Stride: 1}, {Lo: 0x1f21a, Hi: 0x1f21a, Stride: 1},
		{Lo: 0x1f22f, Hi: 0x1f22f, Stride: 1}, {Lo: 0x1f232, Hi: 0x1f23a, Stride: 1},
		{Lo: 0x1f23c, Hi: 0x1f23f, Stride: 1}, {Lo: 0x1f249, Hi: 0x1f3fa, Stride: 1},
		{Lo: 0x1f400, Hi: 0x1f53d, Stride: 1}, {Lo: 0x1f546, Hi: 0x1f64f, Stride: 1},
		{Lo: 0x1f680, Hi: 0x1f6ff, Stride: 1}, {Lo: 0x1f774, Hi: 0x1f77f, Stride: 1},
		{Lo: 0x1f7d5, Hi: 0x1f7ff, Stride: 1}, {Lo: 0x1f80c, Hi: 0x1f80f, Stride: 1},
		{Lo: 0x1f848, Hi: 0x1f84f, Stride: 1}, {Lo: 0x1f85a, Hi: 0x1f85f, Stride: 1},
		{Lo: 0x1f888, Hi: 0x1f88f, Stride: 1}, {Lo: 0x1f8ae, Hi: 0x1f8ff, Stride: 1},
		{Lo: 0x1f90c, Hi: 0x1f93a, Stride: 1}, {Lo: 0x1f93c, Hi: 0x1f945, Stride: 1},
		{Lo: 0x1f947, Hi: 0x1faff, Stride: 1}, {Lo: 0x1fc00, Hi: 0x1fffd, Stride: 1},
	},
	LatinOffset: 2,
}

// styleEmoji reports whether r belongs to the fixed fy-rubric-1 emoji table.
func styleEmoji(r rune) bool {
	return unicode.Is(styleEmojiRanges, r)
}
