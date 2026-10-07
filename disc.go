package main

// THIS IS A FOSS version. For full version (500k parms) & training pipeline, contact 1@868322.xyz
// THE SOFTWARE IS PROVIDED “AS IS”, WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

import (
	_ "embed"
	"encoding/json"
	"math"
)

//go:embed weights.json
var weightsJSON []byte

const (
	diffScale = 50.0
	stride    = 12
)

// ---------------- tiny conv net ----------------

type conv1d struct {
	w [][][]float64 // (out, in, k)
	b []float64
}

type incBlock struct {
	dw1, dw2, dw3, dw4    conv1d
	pw1a, pw1b, pw1c, pw2 conv1d
}

type discNet struct {
	base   int // width scale: C = base*8 channels (disc6mix: base=16 -> 128)
	stem   conv1d
	blocks [2]incBlock
	conv   conv1d
	fc1w   [][]float64 // (base*4, base*8)
	fc1b   []float64
	fc2w   []float64 // (base*4,)
	fc2b   []float64
}

var disc *discNet

func mustLoadDisc() *discNet {
	if disc != nil {
		return disc
	}
	type cv struct {
		W [][][]float64 `json:"w"`
		B []float64     `json:"b"`
	}
	toC := func(c cv) conv1d { return conv1d{w: c.W, b: c.B} }
	var raw struct {
		Stem   cv `json:"stem"`
		Blocks []struct {
			Dw1  cv `json:"dw1"`
			Dw2  cv `json:"dw2"`
			Dw3  cv `json:"dw3"`
			Dw4  cv `json:"dw4"`
			Pw1a cv `json:"pw1a"`
			Pw1b cv `json:"pw1b"`
			Pw1c cv `json:"pw1c"`
			Pw2  cv `json:"pw2"`
		} `json:"blocks"`
		Conv cv `json:"conv"`
		Fc1  struct {
			W [][]float64 `json:"w"`
			B []float64   `json:"b"`
		} `json:"fc1"`
		Fc2 struct {
			W [][]float64 `json:"w"`
			B []float64   `json:"b"`
		} `json:"fc2"`
	}
	if err := json.Unmarshal(weightsJSON, &raw); err != nil {
		panic("weights.json: " + err.Error())
	}
	if len(raw.Blocks) != 2 {
		panic("weights.json: want 2 Inc1D blocks")
	}
	n := &discNet{stem: toC(raw.Stem), conv: toC(raw.Conv), base: len(raw.Stem.B) / 8}
	for i := range raw.Blocks {
		b := &raw.Blocks[i]
		n.blocks[i] = incBlock{
			dw1: toC(b.Dw1), dw2: toC(b.Dw2), dw3: toC(b.Dw3), dw4: toC(b.Dw4),
			pw1a: toC(b.Pw1a), pw1b: toC(b.Pw1b), pw1c: toC(b.Pw1c), pw2: toC(b.Pw2),
		}
	}
	n.fc1w = raw.Fc1.W
	n.fc1b = raw.Fc1.B
	if len(raw.Fc2.W) != 1 {
		panic("weights.json: fc2 want 1 row")
	}
	n.fc2w = raw.Fc2.W[0]
	n.fc2b = raw.Fc2.B
	disc = n
	return n
}

func leaky(x float64) float64 {
	if x < 0 {
		return 0.01 * x
	}
	return x
}

// full conv, zero padding, stride 1 (stem / final conv)
func convFull(c *conv1d, x [][]float64) [][]float64 {
	cout, cin, K := len(c.w), len(c.w[0]), len(c.w[0][0])
	T := len(x[0])
	pad := K / 2
	y := make([][]float64, cout)
	for o := 0; o < cout; o++ {
		w := c.w[o]
		yc := make([]float64, T)
		for t := 0; t < T; t++ {
			s := c.b[o]
			for i := 0; i < cin; i++ {
				xi := x[i]
				wi := w[i]
				lo := pad - t
				if lo < 0 {
					lo = 0
				}
				hi := T + pad - t
				if hi > K {
					hi = K
				}
				for k := lo; k < hi; k++ {
					s += wi[k] * xi[t+k-pad]
				}
			}
			yc[t] = s
		}
		y[o] = yc
	}
	return y
}

// depthwise conv (groups == channels), zero padding
func dwConv(c *conv1d, x [][]float64) [][]float64 {
	C := len(x)
	K := len(c.w[0][0])
	T := len(x[0])
	pad := K / 2
	y := make([][]float64, C)
	for ch := 0; ch < C; ch++ {
		w := c.w[ch][0]
		yc := make([]float64, T)
		for t := 0; t < T; t++ {
			s := c.b[ch]
			for k := 0; k < K; k++ {
				ti := t + k - pad
				if ti >= 0 && ti < T {
					s += w[k] * x[ch][ti]
				}
			}
			yc[t] = s
		}
		y[ch] = yc
	}
	return y
}

// pointwise (1x1) conv
func pwConv(c *conv1d, x [][]float64) [][]float64 {
	cout, cin := len(c.w), len(c.w[0])
	T := len(x[0])
	y := make([][]float64, cout)
	for o := 0; o < cout; o++ {
		w := c.w[o]
		b := c.b[o]
		yc := make([]float64, T)
		for t := 0; t < T; t++ {
			s := b
			for i := 0; i < cin; i++ {
				s += w[i][0] * x[i][t]
			}
			yc[t] = s
		}
		y[o] = yc
	}
	return y
}

func cat2(a, b [][]float64) [][]float64 {
	out := make([][]float64, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func leakyAll(h [][]float64) {
	for c := range h {
		for t := range h[c] {
			h[c][t] = leaky(h[c][t])
		}
	}
}

func (n *discNet) forward(x2 [][]float64) float64 {
	h := convFull(&n.stem, x2) // [C][T]
	b4, b6, b7 := n.base*4, n.base*6, n.base*7
	for bi := range n.blocks {
		b := &n.blocks[bi]
		o1 := dwConv(&b.dw1, h[0:b4])
		o2 := dwConv(&b.dw2, h[b4:b6])
		o3 := dwConv(&b.dw3, h[b6:b7])
		o4 := dwConv(&b.dw4, h[b7:])
		step1 := cat2(cat2(o1, o2), cat2(o3, o4)) // [C]
		y0 := pwConv(&b.pw1a, h)                  // C->C/4
		y1 := pwConv(&b.pw1b, step1)              // C->C/4
		y2 := pwConv(&b.pw1c, cat2(h, step1))     // 2C->C/2
		step2 := cat2(cat2(y0, y1), y2)           // [C]
		h = pwConv(&b.pw2, step2)                 // C->C
		leakyAll(h)
	}
	h = convFull(&n.conv, h)
	leakyAll(h)

	// masked GAP (mask is all-ones here)
	T := len(h[0])
	pooled := make([]float64, len(h))
	for c := range h {
		s := 0.0
		for t := 0; t < T; t++ {
			s += h[c][t]
		}
		pooled[c] = s / float64(T)
	}
	// head: 64 -> 32 (leaky) -> 1 -> sigmoid
	h1 := make([]float64, len(n.fc1w))
	for i := range n.fc1w {
		s := n.fc1b[i]
		wi := n.fc1w[i]
		for c := 0; c < len(pooled); c++ {
			s += wi[c] * pooled[c]
		}
		h1[i] = leaky(s)
	}
	s := n.fc2b[0]
	for i := range h1 {
		s += n.fc2w[i] * h1[i]
	}
	return 1.0 / (1.0 + math.Exp(-s))
}

// diffChannels: 20 Hz points (or already-subsampled) -> 2 x (T-1) diff/50.
func diffChannels(pts [][2]float64) [][]float64 {
	T := len(pts)
	x := make([][]float64, 2)
	x[0] = make([]float64, T-1)
	x[1] = make([]float64, T-1)
	for i := 0; i+1 < T; i++ {
		x[0][i] = (pts[i+1][0] - pts[i][0]) / diffScale
		x[1][i] = (pts[i+1][1] - pts[i][1]) / diffScale
	}
	return x
}

// scoreTicks: 240 Hz ticks [[x,y,d],...] -> p(human).
func scoreTicks(ticks [][]float64) float64 {
	var pts [][2]float64
	for i := 0; i < len(ticks); i += stride {
		pts = append(pts, [2]float64{ticks[i][0], ticks[i][1]})
	}
	if len(pts) < 3 {
		return 0 // degenerate; replay validation will reject anyway
	}
	return mustLoadDisc().forward(diffChannels(pts))
}
