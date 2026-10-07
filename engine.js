(function (root) {
    'use strict';
    var TOINT = 6755399441055744.0;
    var INVPIO2 = 6.36619772367581382433e-01;
    var PIO2_1 = 1.57079632673412561417e+00;
    var PIO2_1T = 6.07710050650619224932e-11;
    var PIO2_2 = 6.07710050630396597660e-11;
    var PIO2_2T = 2.02226624879595063154e-21;
    var S1 = -1.66666666666666324348e-01, S2 = 8.33333333332248946124e-03, S3 = -1.98412698298579493134e-04,
        S4 = 2.75573137070700676789e-06, S5 = -2.50507602534068634195e-08, S6 = 1.58969099521155010221e-10;
    var C1 = 4.16666666666666019037e-02, C2 = -1.38888888888741095749e-03, C3 = 2.48015872894767294178e-05,
        C4 = -2.75573143513906633035e-07, C5 = 2.08757232129817482790e-09, C6 = -1.13596475577881948265e-11;

    function hypot(x, y) {
        var xx = x * x, yy = y * y;
        return Math.sqrt(xx + yy);
    }

    function reduce(x) {
        var t = x * INVPIO2 + TOINT;
        var n = t - TOINT;
        var r = x - n * PIO2_1;
        r = r - n * PIO2_1T;
        r = r - n * PIO2_2;
        r = r - n * PIO2_2T;
        var q = t % 4;
        if (q < 0) q += 4;
        return [r, q];
    }

    function ksin(r) {
        var z = r * r;
        var w = S1 + z * (S2 + z * (S3 + z * (S4 + z * (S5 + z * S6))));
        return r + r * w;
    }

    function kcos(r) {
        var z = r * r;
        var p = C1 + z * (C2 + z * (C3 + z * (C4 + z * (C5 + z * C6))));
        return 1 - z * 0.5 + z * z * p;
    }

    function fsin(x) {
        var k = reduce(x);
        if (k[1] === 1) return kcos(k[0]);
        if (k[1] === 2) return -ksin(k[0]);
        if (k[1] === 3) return -kcos(k[0]);
        return ksin(k[0]);
    }

    function fcos(x) {
        var k = reduce(x);
        if (k[1] === 1) return -ksin(k[0]);
        if (k[1] === 2) return -kcos(k[0]);
        if (k[1] === 3) return ksin(k[0]);
        return kcos(k[0]);
    }

    function mulberry32(a) {
        return function () {
            a |= 0;
            a = a + 0x6D2B79F5 | 0;
            var t = Math.imul(a ^ a >>> 15, 1 | a);
            t = t + Math.imul(t ^ t >>> 7, 61 | t) ^ t;
            return ((t ^ t >>> 14) >>> 0) / 4294967296;
        };
    }

    function nInt(r, lo, hi) {
        return lo + Math.floor(r() * (hi - lo + 1));
    }

    function nFloat(r, lo, hi) {
        return lo + r() * (hi - lo);
    }

    function norm4(x) {
        var y = x * 10000;
        if (y === 0) return 0;
        var q = Math.floor(Math.abs(y) + 0.5);
        return y < 0 ? -q / 10000 : q / 10000;
    }

    var TWO_PI = 6.283185307179586, HAND_R = 21.06, SOLVE_N = 840, MAX_TICKS = 14400;

    function liss(p, t) {
        var rx = p[4] + p[6] * fsin(t * p[8]), ry = p[5] + p[7] * fsin(t * p[8] * 1.3);
        var phx = p[9] + t * p[11], phy = p[10] + t * p[11] * 0.7;
        var dx = (1 - p[12]) + p[12] * (fsin(p[16] + t * p[14]) + 1) * 0.5;
        var dy = (1 - p[13]) + p[13] * (fsin(p[17] + t * p[15]) + 1) * 0.5;
        return [p[0] + p[2] * dx * fsin(phx + rx * t), p[1] + p[3] * dy * fsin(phy + ry * t)];
    }

    function advNeedle(nd, tick) {
        var p = nd.params;
        var a = liss(p, nd.t), b = liss(p, nd.t + 0.0001);
        var arc = hypot(b[1] - a[1], b[0] - a[0]);
        var d = tick * (1 / 240);
        var mult = fsin(d * 1.6132) * 0.2 + 1.15;
        var speed = p[18] * mult / 240;
        nd.t += arc > 0.0001 ? 0.0001 * (speed / arc) : 0.0001;
        var q = liss(p, nd.t);
        nd.x = q[0];
        nd.y = q[1];
    }

    function inPoly(px, py, vx, vy) {
        var ins = false;
        for (var i = 0, j = vx.length - 1; i < vx.length; j = i++) {
            if ((vy[i] > py) !== (vy[j] > py) && px < (vx[j] - vx[i]) * (py - vy[i]) / (vy[j] - vy[i]) + vx[i]) ins = !ins;
        }
        return ins;
    }

    function inShape(px, py, cx, cy, r, k) {
        if (k === 0) {
            var rr = r * 1.15, dx = px - cx, dy = py - cy;
            return dx * dx + dy * dy <= rr * rr;
        }
        if (k === 1) {
            var m = r * 0.9775;
            return Math.abs(px - cx) <= m && Math.abs(py - cy) <= m;
        }
        if (k === 4) {
            var rx = r * 0.92, ry = r * 1.61, dx = px - cx, dy = py - cy;
            return dx * dx / (rx * rx) + dy * dy / (ry * ry) <= 1;
        }
        if (k === 6) {
            var m = r * 1.035, m7 = m * 0.7, m13 = m * 1.3;
            return inPoly(px, py, [cx - m7, cx + m7, cx + m13, cx - m13], [cy - m, cy - m, cy + m, cy + m]);
        }
        if (k === 7) {
            var w = r * 0.85 * 1.265, h = r * 0.85 * 2 * 1.265;
            return inPoly(px, py, [cx, cx - w * 1.2, cx - w * 1.3, cx - w * 0.8, cx, cx + w * 0.8, cx + w * 1.3, cx + w * 1.2], [cy + h * 0.7, cy + h * 0.2, cy - h * 0.3, cy - h * 0.55, cy - h * 0.3, cy - h * 0.55, cy - h * 0.3, cy + h * 0.2]);
        }
        var rr = k === 2 ? r * 1.2075 : (k === 3 ? r * 1.15 : r * 1.265);
        var n = k === 2 ? 5 : (k === 3 ? 6 : 8);
        var vx = [], vy = [];
        for (var i = 0; i < n; i++) {
            var a = i * 2 * Math.PI / n - Math.PI / 2;
            vx.push(cx + rr * fcos(a));
            vy.push(cy + rr * fsin(a));
        }
        return inPoly(px, py, vx, vy);
    }

    function Game(ps) {
        this.seed = ps;
        var r = mulberry32(ps);
        var nSeg = nInt(r, 12, 15);
        this.n = nSeg + 1;
        this.hang = 8 * nSeg;
        var ax = nFloat(r, 30, 170), ay = nFloat(r, 20, 40);
        this.tx = ax;
        this.ty = ay;
        var wig = nFloat(r, -1, 1), amp = nFloat(r, 2, 4);
        this.xs = [];
        this.ys = [];
        this.pxs = [];
        this.pys = [];
        for (var i = 0; i < this.n; i++) {
            this.xs.push(ax);
            this.ys.push(ay + i * 8);
            var off = i === 0 ? 0 : (wig * amp * i) / nSeg;
            this.pxs.push(ax - off);
            this.pys.push(ay + i * 8);
        }
        this.wA = new Array(this.n - 1).fill(0.5);
        this.wB = new Array(this.n - 1).fill(0.5);
        this.wA[this.n - 2] = 1 / 9;
        this.wB[this.n - 2] = 8 / 9;
        var cx = 100, cy = 156.5, axA = 76, ayA = 10.925;
        var usedS = {}, usedC = {};
        this.needles = [];
        this.targetIdx = nInt(r, 0, 2);
        for (var i = 0; i < 3; i++) {
            var s;
            do {
                s = nInt(r, 0, 7);
            } while (usedS[s]);
            usedS[s] = 1;
            var c;
            do {
                c = nInt(r, 0, 9);
            } while (usedC[c]);
            usedC[c] = 1;
            var p = [cx, cy, axA, ayA, nFloat(r, 1.5, 2.5), nFloat(r, 2, 3), nFloat(r, .2, .4), nFloat(r, .2, .4), nFloat(r, .05, .1), nFloat(r, 0, TWO_PI), nFloat(r, 0, TWO_PI), nFloat(r, .02, .05), nFloat(r, .15, .3), nFloat(r, .15, .3), nFloat(r, .08, .15), nFloat(r, .06, .12), nFloat(r, 0, TWO_PI), nFloat(r, 0, TWO_PI), nFloat(r, 26.25, 38.75)];
            var t0 = nFloat(r, 0, 10);
            var q = liss(p, t0);
            this.needles.push({shape: s, params: p, t: t0, x: q[0], y: q[1], target: i === this.targetIdx});
        }
        this.down = false;
        this.counter = 0;
        this.tick = 0;
        this.solved = false;
        this.solvedTick = -1;
    }

    Game.prototype.inTarget = function () {
        var nd = this.needles[this.targetIdx];
        return inShape(this.xs[this.n - 1], this.ys[this.n - 1], nd.x, nd.y, HAND_R, nd.shape);
    };

    Game.prototype.step = function (x, y, dn) {
        var t = this.tick;
        if (dn) {
            this.down = true;
            this.tx = norm4(Math.max(10, Math.min(190, x)));
            this.ty = norm4(Math.max(10, Math.min(105, y)));
        } else this.down = false;

        for (var k = 0; k < 3; k++) advNeedle(this.needles[k], t);

        this.xs[0] = this.tx;
        this.ys[0] = this.ty;

        var mult = this.down ? 0.9869 : 1.0, g = 700 / (240 * 240);
        for (var i = 1; i < this.n; i++) {
            var vx = (this.xs[i] - this.pxs[i]) * mult, vy = (this.ys[i] - this.pys[i]) * mult;
            var sp = hypot(vx, vy);
            if (sp > 1.2) {
                var kk = 1.2 / sp;
                vx *= kk;
                vy *= kk;
            }
            this.pxs[i] = this.xs[i];
            this.pys[i] = this.ys[i];
            this.xs[i] += vx;
            this.ys[i] = this.ys[i] + vy + g;
        }

        for (var it = 0; it < 10; it++) for (var i = 0; i < this.n - 1; i++) {
            var j = i + 1;
            var dx = this.xs[j] - this.xs[i], dy = this.ys[j] - this.ys[i];
            var dist = hypot(dy, dx);
            if (dist < 1e-4) continue;
            var diff = (8 - dist) / dist, cxd = dx * diff, cyd = dy * diff;
            if (i !== 0) {
                this.xs[i] -= this.wB[i] * cxd;
                this.ys[i] -= this.wB[i] * cyd;
            }
            this.xs[j] += this.wA[i] * cxd;
            this.ys[j] += this.wA[i] * cyd;
        }

        for (var i = 1; i < this.n; i++) {
            this.xs[i] = Math.max(2, Math.min(198, this.xs[i]));
            this.ys[i] = Math.max(2, Math.min(198, this.ys[i]));
        }

        for (var i = 0; i < this.n; i++) {
            this.xs[i] = norm4(this.xs[i]);
            this.ys[i] = norm4(this.ys[i]);
            this.pxs[i] = norm4(this.pxs[i]);
            this.pys[i] = norm4(this.pys[i]);
        }

        if (this.inTarget()) {
            if (++this.counter >= SOLVE_N) {
                if (!this.solved) {
                    this.solved = true;
                    this.solvedTick = t;
                }
            }
        } else if (t % 3 !== 0 && this.counter > 0) this.counter--;

        this.tick = t + 1;
    };

    root.TN = {
        Game: Game, MAX_TICKS: MAX_TICKS, SOLVE_N: SOLVE_N, HAND_R: HAND_R
    };
})(typeof module !== 'undefined' && module.exports ? module.exports : window);
