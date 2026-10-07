package main

// fasttn.go — thread-the-needle engine, line-by-line twin of engine.js
// (browser semantics).  All trig/hypot are hand-rolled so Go and JS are
// bit-identical: only IEEE f64 basic ops + floor/fmod, no fused ops.
//
// NOTE: build for amd64 (this container).  If ever ported to arm64, compile
// with -gcflags=all=-d=fma=0 — arm64 gc fuses a*b+c into FMA, which would
// break bit-parity with JS (hypot below already uses temps, the rest of the
// expressions does not).

import "math"

// ---------------- fdlibm-style kernels (mirror engine.js) ----------------

const (
	invpio2 = 6.36619772367581382433e-01
	pio2_1  = 1.57079632673412561417e+00
	pio2_1t = 6.07710050650619224932e-11
	pio2_2  = 6.07710050630396597660e-11
	pio2_2t = 2.02226624879595063154e-21
	toint   = 6755399441055744.0 // 1.5*2^52

	s1 = -1.66666666666666324348e-01
	s2 = 8.33333333332248946124e-03
	s3 = -1.98412698298579493134e-04
	s4 = 2.75573137070700676789e-06
	s5 = -2.50507602534068634195e-08
	s6 = 1.58969099521155010221e-10

	c1 = 4.16666666666666019037e-02
	c2 = -1.38888888888741095749e-03
	c3 = 2.48015872894767294178e-05
	c4 = -2.75573143513906633035e-07
	c5 = 2.08757232129817482790e-09
	c6 = -1.13596475577881948265e-11
)

// hypot = sqrt(x*x+y*y); temps prevent any FMA fusion (arm64).
func hypot(x, y float64) float64 {
	xx := x * x
	yy := y * y
	return math.Sqrt(xx + yy)
}

// reduce: x mod pi/2 -> r in [-pi/4, pi/4], quadrant q = n mod 4.
// Magic round via +1.5*2^52 (exact for |x| < 2^51); q uses fmod (trunc
// semantics) so JS `%` and Go agree on negatives.
func reduce(x float64) (r, q float64) {
	t := x*invpio2 + toint
	n := t - toint
	r = x - n*pio2_1
	r = r - n*pio2_1t
	r = r - n*pio2_2
	r = r - n*pio2_2t
	q = math.Mod(t, 4)
	if q < 0 {
		q += 4
	}
	return
}

func ksin(r float64) float64 {
	z := r * r
	w := s1 + z*(s2+z*(s3+z*(s4+z*(s5+z*s6))))
	return r + r*w
}

func kcos(r float64) float64 {
	z := r * r
	p := c1 + z*(c2+z*(c3+z*(c4+z*(c5+z*c6))))
	return 1.0 - z*0.5 + z*z*p
}

func fsin(x float64) float64 {
	r, q := reduce(x)
	switch q {
	case 1:
		return kcos(r)
	case 2:
		return -ksin(r)
	case 3:
		return -kcos(r)
	}
	return ksin(r)
}

func fcos(x float64) float64 {
	r, q := reduce(x)
	switch q {
	case 1:
		return -ksin(r)
	case 2:
		return -kcos(r)
	case 3:
		return ksin(r)
	}
	return kcos(r)
}

// ---------------- mulberry32 (bit-exact vs JS) ----------------

type rng32 struct{ a uint32 }

func (r *rng32) next() float64 {
	r.a += 0x6D2B79F5
	t := r.a
	t = (t ^ (t >> 15)) * (t | 1)
	t = (t + (t^(t>>7))*(t|61)) ^ t
	return float64(t^(t>>14)) / 4294967296.0
}

func (r *rng32) nInt(lo, hi int) int {
	return lo + int(math.Floor(r.next()*float64(hi-lo+1)))
}

func (r *rng32) nFloat(lo, hi float64) float64 {
	return lo + r.next()*(hi-lo)
}

// ---------------- engine ----------------

const (
	twoPi    = 6.283185307179586 // Math.PI*2
	handR    = 21.06
	solveN   = 840
	maxTicks = 14400 // 60 s of 240 Hz, hard cap for submissions
)

type needle struct {
	shape  int
	params [19]float64
	t, x   float64
	y      float64
}

type Game struct {
	seed          int32
	n, nSeg, hang int
	tx, ty        float64
	xs, ys        []float64
	pxs, pys      []float64
	wA, wB        []float64
	needles       [3]needle
	targetIdx     int
	down          bool
	counter       int
	tick          int
	solved        bool
	solvedTick    int // tick index at which solved first became true (-1 if never)
}

func norm4(x float64) float64 {
	y := x * 10000
	if y == 0 {
		return 0
	}
	q := math.Floor(math.Abs(y) + 0.5)
	if y < 0 {
		return -q / 10000
	}
	return q / 10000
}

func liss(p *[19]float64, t float64) (float64, float64) {
	rx := p[4] + p[6]*fsin(t*p[8])
	ry := p[5] + p[7]*fsin(t*p[8]*1.3)
	phx := p[9] + t*p[11]
	phy := p[10] + t*p[11]*0.7
	dx := (1 - p[12]) + p[12]*(fsin(p[16]+t*p[14])+1)*0.5
	dy := (1 - p[13]) + p[13]*(fsin(p[17]+t*p[15])+1)*0.5
	return p[0] + p[2]*dx*fsin(phx+rx*t), p[1] + p[3]*dy*fsin(phy+ry*t)
}

func advNeedle(nd *needle, tick int) {
	p := &nd.params
	x0, y0 := liss(p, nd.t)
	x1, y1 := liss(p, nd.t+0.0001)
	dx := x1 - x0
	dy := y1 - y0
	arc := hypot(dy, dx)
	d := float64(tick) * (1.0 / 240.0)
	mult := fsin(d*1.6132)*0.2 + 1.15
	speed := p[18] * mult / 240
	if arc > 0.0001 {
		nd.t += 0.0001 * (speed / arc)
	} else {
		nd.t += 0.0001
	}
	nd.x, nd.y = liss(p, nd.t)
}

func inPoly(px, py float64, vx, vy []float64) bool {
	ins := false
	j := len(vx) - 1
	for i := 0; i < len(vx); i++ {
		if (vy[i] > py) != (vy[j] > py) {
			if px < (vx[j]-vx[i])*(py-vy[i])/(vy[j]-vy[i])+vx[i] {
				ins = !ins
			}
		}
		j = i
	}
	return ins
}

// pointInShape polygon vertices live in a local array (max 8): replays run
// concurrently (replayConcurrency), so a shared package-level scratch buffer
// would be a data race between pointInShape (writes) and inPoly (reads).
func pointInShape(px, py, cx, cy, r float64, k int) bool {
	var pvx, pvy [8]float64
	switch k {
	case 0: // circle
		rr := r * 1.15
		dx := px - cx
		dy := py - cy
		return dx*dx+dy*dy <= rr*rr
	case 1: // square
		m := r * 0.9775
		return math.Abs(px-cx) <= m && math.Abs(py-cy) <= m
	case 4: // ellipse
		rx := r * 0.92
		ry := r * 1.61
		dx := px - cx
		dy := py - cy
		return dx*dx/(rx*rx)+dy*dy/(ry*ry) <= 1
	case 6: // diamond
		m := r * 1.035
		m7 := m * 0.7
		m13 := m * 1.3
		vx := pvx[:4]
		vy := pvy[:4]
		vx[0], vy[0] = cx-m7, cy-m
		vx[1], vy[1] = cx+m7, cy-m
		vx[2], vy[2] = cx+m13, cy+m
		vx[3], vy[3] = cx-m13, cy+m
		return inPoly(px, py, vx, vy)
	case 7: // heart
		w := r * 0.85 * 1.265
		h := r * 0.85 * 2 * 1.265
		vx := pvx[:8]
		vy := pvy[:8]
		vx[0], vy[0] = cx, cy+h*0.7
		vx[1], vy[1] = cx-w*1.2, cy+h*0.2
		vx[2], vy[2] = cx-w*1.3, cy-h*0.3
		vx[3], vy[3] = cx-w*0.8, cy-h*0.55
		vx[4], vy[4] = cx, cy-h*0.3
		vx[5], vy[5] = cx+w*0.8, cy-h*0.55
		vx[6], vy[6] = cx+w*1.3, cy-h*0.3
		vx[7], vy[7] = cx+w*1.2, cy+h*0.2
		return inPoly(px, py, vx, vy)
	default: // pentagon(2) hexagon(3) octagon(5)
		rr := r * 1.265
		n := 8
		if k == 2 {
			rr = r * 1.2075
			n = 5
		} else if k == 3 {
			rr = r * 1.15
			n = 6
		}
		vx := pvx[:n]
		vy := pvy[:n]
		for i := 0; i < n; i++ {
			a := float64(i)*2.0*math.Pi/float64(n) - math.Pi/2
			vx[i] = cx + rr*fcos(a)
			vy[i] = cy + rr*fsin(a)
		}
		return inPoly(px, py, vx, vy)
	}
}

func NewGame(seed int32) *Game {
	g := &Game{seed: seed, solvedTick: -1}
	r := &rng32{a: uint32(seed)}

	g.nSeg = r.nInt(12, 15)
	g.n = g.nSeg + 1
	g.hang = 8 * g.nSeg
	ax := r.nFloat(30, 170)
	ay := r.nFloat(20, 40)
	g.tx, g.ty = ax, ay
	wig := r.nFloat(-1, 1)
	amp := r.nFloat(2, 4)

	g.xs = make([]float64, g.n)
	g.ys = make([]float64, g.n)
	g.pxs = make([]float64, g.n)
	g.pys = make([]float64, g.n)
	for i := 0; i < g.n; i++ {
		g.xs[i] = ax
		g.ys[i] = ay + float64(i)*8
		off := 0.0
		if i != 0 {
			off = (wig * amp * float64(i)) / float64(g.nSeg)
		}
		g.pxs[i] = ax - off
		g.pys[i] = ay + float64(i)*8
	}
	g.wA = make([]float64, g.n-1)
	g.wB = make([]float64, g.n-1)
	for i := range g.wA {
		g.wA[i] = 0.5
		g.wB[i] = 0.5
	}
	g.wA[g.n-2] = 1.0 / 9.0
	g.wB[g.n-2] = 8.0 / 9.0

	cx, cy, axA, ayA := 100.0, 156.5, 76.0, 10.925
	var usedS, usedC [10]bool
	g.targetIdx = r.nInt(0, 2)
	for i := 0; i < 3; i++ {
		s := 0
		for {
			s = r.nInt(0, 7)
			if !usedS[s] {
				usedS[s] = true
				break
			}
		}
		for {
			c := r.nInt(0, 9)
			if !usedC[c] {
				usedC[c] = true
				break
			}
		}
		var p [19]float64
		p[0], p[1], p[2], p[3] = cx, cy, axA, ayA
		p[4] = r.nFloat(1.5, 2.5)
		p[5] = r.nFloat(2, 3)
		p[6] = r.nFloat(0.2, 0.4)
		p[7] = r.nFloat(0.2, 0.4)
		p[8] = r.nFloat(0.05, 0.1)
		p[9] = r.nFloat(0, twoPi)
		p[10] = r.nFloat(0, twoPi)
		p[11] = r.nFloat(0.02, 0.05)
		p[12] = r.nFloat(0.15, 0.3)
		p[13] = r.nFloat(0.15, 0.3)
		p[14] = r.nFloat(0.08, 0.15)
		p[15] = r.nFloat(0.06, 0.12)
		p[16] = r.nFloat(0, twoPi)
		p[17] = r.nFloat(0, twoPi)
		p[18] = r.nFloat(26.25, 38.75)
		t0 := r.nFloat(0, 10)
		nd := &g.needles[i]
		nd.shape = s
		nd.params = p
		nd.t = t0
		nd.x, nd.y = liss(&p, t0)
	}
	return g
}

func (g *Game) inTarget() bool {
	nd := &g.needles[g.targetIdx]
	px := g.xs[g.n-1]
	py := g.ys[g.n-1]
	return pointInShape(px, py, nd.x, nd.y, handR, nd.shape)
}

// ballGridPos: rope-end (orange ball) grid position.  xs/ys are norm4-
// quantized inside step(), so this is the exact client-visible value the
// checkpoint proof must reproduce bit-for-bit (engines are bit-exact twins,
// JSON floats round-trip exactly; compare with ==, no epsilon).
func (g *Game) ballGridPos() (float64, float64) {
	return g.xs[g.n-1], g.ys[g.n-1]
}

func (g *Game) step(x, y float64, dn bool) {
	t := g.tick
	if dn {
		g.down = true
		g.tx = norm4(math.Max(10, math.Min(190, x)))
		g.ty = norm4(math.Max(10, math.Min(105, y)))
	} else {
		g.down = false
	}

	for i := range g.needles {
		advNeedle(&g.needles[i], t)
	}

	g.xs[0] = g.tx
	g.ys[0] = g.ty

	mult := 1.0
	if g.down {
		mult = 0.9869
	}
	grav := 700.0 / (240.0 * 240.0)
	n := g.n
	for i := 1; i < n; i++ {
		vx := (g.xs[i] - g.pxs[i]) * mult
		vy := (g.ys[i] - g.pys[i]) * mult
		sp := hypot(vx, vy)
		if sp > 1.2 {
			k := 1.2 / sp
			vx *= k
			vy *= k
		}
		g.pxs[i] = g.xs[i]
		g.pys[i] = g.ys[i]
		g.xs[i] += vx
		g.ys[i] = g.ys[i] + vy + grav
	}

	for it := 0; it < 10; it++ {
		for i := 0; i < n-1; i++ {
			j := i + 1
			dx := g.xs[j] - g.xs[i]
			dy := g.ys[j] - g.ys[i]
			dist := hypot(dy, dx)
			if dist < 1e-4 {
				continue
			}
			diff := (8 - dist) / dist
			cxd := dx * diff
			cyd := dy * diff
			if i != 0 {
				g.xs[i] -= g.wB[i] * cxd
				g.ys[i] -= g.wB[i] * cyd
			}
			g.xs[j] += g.wA[i] * cxd
			g.ys[j] += g.wA[i] * cyd
		}
	}

	for i := 1; i < n; i++ {
		g.xs[i] = math.Max(2, math.Min(198, g.xs[i]))
		g.ys[i] = math.Max(2, math.Min(198, g.ys[i]))
	}

	for i := 0; i < n; i++ {
		g.xs[i] = norm4(g.xs[i])
		g.ys[i] = norm4(g.ys[i])
		g.pxs[i] = norm4(g.pxs[i])
		g.pys[i] = norm4(g.pys[i])
	}

	if g.inTarget() {
		g.counter++
		if g.counter >= solveN && !g.solved {
			g.solved = true
			g.solvedTick = t
		}
	} else if t%3 != 0 && g.counter > 0 {
		g.counter--
	}

	g.tick = t + 1
}
