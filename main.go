package main

// waf — human-trajectory captcha front door.
//
// Flow:
//   GET  /*            cookie==md5(password+ip+dateUTC)?  -> upstream / congrats
//                      else -> 401 + challenge page (game runs client-side)
//   POST /challenge    (bucket) issue one-time ticket seed, TTL 10 min, bound to ip
//   POST /solve        {seed, ticks, cps} (bucket)
//                      param errors -> err1 + 30 min:  unknown/expired/mismatched
//                      seed, bad body, replay longer than 14400 ticks, wrong
//                      count of checkpoint positions, replay not ending
//                      exactly on the solve tick (early or with tail)
//                      machine trajectory (p < THR) -> 403, no penalty
//                      else -> Set-Cookie (valid until UTC midnight), bucket -4 min

import (
	"bytes"
	"crypto/md5"
	crand "crypto/rand"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// ================= config (config.json; auto-generated on first start) =================

const (
	cookieName = "waf"

	bucketDrop   = 20 * time.Minute // w = max(w, now-drop): 20-min idle empties
	bucketAdd    = 1 * time.Minute  // per /challenge or /solve
	bucketRefund = 4 * time.Minute  // refunded on successful /solve
	bucketBan    = 30 * time.Minute // param-error penalty added at the "#" step

	ticketTTL = 10 * time.Minute
	gcEvery   = 5 * time.Minute

	maxBodyBytes = 8 << 20 // 8 MiB (a 14400-tick payload is ~0.5 MiB)

	replayConcurrency = 8 // global backpressure for replays
)

// Set from config.json in main(), before any request is served.
var (
	listenAddr = ":8081"   // config "listen"
	upstream   = ""        // config "upstream": "" -> plain "congratulations" page (test mode); e.g. "http://127.0.0.1:9000"
	password   = "hunter2" // config "sign_key": md5 salt for the day cookie

	// 38 / 45
	thrHuman = 0.45 // config "threshold": p(human) gate, EER ~13.8% vs ALL frozen probes (g0 human test set)
)

//go:embed page.html
var pageHTML []byte

//go:embed engine.js
var engineJS []byte

// ================= state =================

type ticket struct {
	ip     string
	expire time.Time
}

var (
	mu        sync.Mutex
	buckets   = map[string]time.Time{} // ip -> water level (absolute time)
	tickets   = map[int32]*ticket{}    // seed -> {ip, expire}
	replaySem = make(chan struct{}, replayConcurrency)
)

// nowFn is injectable for tests.
var nowFn = time.Now

// ================= leaky bucket =================

// bucketTake: w = max(w, now-drop); w += add (+penalty at the "#"); ok iff w <= now.
func bucketTake(ip string, penalty time.Duration) bool {
	mu.Lock()
	defer mu.Unlock()
	now := nowFn()
	w := buckets[ip]
	if floor := now.Add(-bucketDrop); w.Before(floor) {
		w = floor
	}
	w = w.Add(bucketAdd + penalty)
	buckets[ip] = w
	return !w.After(now)
}

func refundBucket(ip string) {
	mu.Lock()
	defer mu.Unlock()
	buckets[ip] = buckets[ip].Add(-bucketRefund)
}

func bucketPenalize(ip string) {
	mu.Lock()
	defer mu.Unlock()
	now := nowFn()
	w := buckets[ip]
	if w.Before(now) {
		w = now
	}
	buckets[ip] = w.Add(bucketBan)
}

// ================= auth =================

func dayToken(ip string, now time.Time) string {
	h := md5.Sum([]byte(password + ip + now.UTC().Format("20060102")))
	return hex.EncodeToString(h[:])
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func hasValidCookie(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	return c.Value == dayToken(clientIP(r), time.Now())
}

// ================= handlers =================

func jsend(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func handleChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsend(w, http.StatusMethodNotAllowed, map[string]any{"err": "POST only"})
		return
	}
	ip := clientIP(r)
	if !bucketTake(ip, 0) {
		jsend(w, http.StatusTooManyRequests, map[string]any{"err": "rate"})
		return
	}
	jsend(w, 200, map[string]any{"seed": issueTicket(ip)})
}

func issueTicket(ip string) int32 {
	mu.Lock()
	defer mu.Unlock()
	for {
		var b [4]byte
		if _, err := crand.Read(b[:]); err != nil {
			log.Fatal("crypto/rand: ", err)
		}
		s := int32(binary.LittleEndian.Uint32(b[:]))
		if _, dup := tickets[s]; dup {
			continue
		}
		tickets[s] = &ticket{ip: ip, expire: time.Now().Add(ticketTTL)}
		return s
	}
}

type solveReq struct {
	Seed  int32       `json:"seed"`
	Ticks [][]float64 `json:"ticks"` // [x, y, down] at 240 Hz
	// Cps holds the rope-end ("ball") grid position observed after stepping
	// each checkpoint tick k = 2^(n+2) (n a positive integer: 8, 16, 32, ...),
	// in ascending tick order.  [x, y] per checkpoint.
	Cps [][]float64 `json:"cps"`
}

func rejectErr1(w http.ResponseWriter, ip string) {
	bucketPenalize(ip)
	jsend(w, http.StatusBadRequest, map[string]any{"err": "err1"})
}

func handleSolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsend(w, http.StatusMethodNotAllowed, map[string]any{"err": "POST only"})
		return
	}
	ip := clientIP(r)
	if !bucketTake(ip, 0) {
		jsend(w, http.StatusTooManyRequests, map[string]any{"err": "rate"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		rejectErr1(w, ip)
		return
	}
	var req solveReq
	if err := json.Unmarshal(body, &req); err != nil || len(req.Ticks) == 0 {
		rejectErr1(w, ip)
		return
	}

	// ticket: must exist, unexpired, same ip. Burn it regardless (single use).
	mu.Lock()
	t, ok := tickets[req.Seed]
	if ok {
		delete(tickets, req.Seed)
	}
	mu.Unlock()
	if !ok || t.ip != ip || time.Now().After(t.expire) {
		rejectErr1(w, ip)
		return
	}
	if len(req.Ticks) > maxTicks {
		rejectErr1(w, ip)
		return
	}
	if len(req.Cps) != cpCount(len(req.Ticks)) {
		rejectErr1(w, ip)
		return
	}

	replaySem <- struct{}{}
	replayOK, why := replayCheck(req.Seed, req.Ticks, req.Cps)
	<-replaySem
	if !replayOK {
		log.Printf("ip=%s seed=%d rejected replay: %s", ip, req.Seed, why)
		rejectErr1(w, ip)
		return
	}

	p := scoreTicks(req.Ticks)
	if p < thrHuman {
		log.Printf("ip=%s seed=%d machine (p=%.4f)", ip, req.Seed, p)
		jsend(w, http.StatusForbidden, map[string]any{"err": "err2"})
		return
	}

	refundBucket(ip)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: dayToken(ip, time.Now()),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	log.Printf("ip=%s seed=%d human (p=%.4f) -> cookie", ip, req.Seed, p)
	jsend(w, 200, map[string]any{"ok": 1})
}

// ---------------- checkpoint proofs (anti-amplification) ----------------
//
// DoS vector closed here: without checkpoints a client could fetch seeds
// from many IPs and submit 60 s of garbage, forcing the server to burn the
// full replay CPU at zero client cost.  Now every tick whose index k is a
// power of two >= 8 (k = 2^(n+2) for some positive integer n) must also be
// reported with the ball's grid position at that tick.  The ball position
// depends on the entire input history (verlet chain), so producing it
// requires running the very same simulation; a wrong checkpoint aborts the
// replay immediately (garbage dies after 9 steps at tick 8).  A client
// whose checkpoints pass up to tick k has itself simulated >= k ticks, so
// it can save at most a constant factor (<= 2) of the server's work.
//
// Positions are compared with strict equality: fasttn.go and engine.js are
// bit-exact twins (only exactly-defined IEEE 754 ops: correctly rounded
// + - * /, exact sqrt/floor, trunc-mod; no FMA on amd64), norm4 quantizes
// to the 1e-4 grid with a single correctly rounded division, and shortest
// round-trip JSON floats parse back to the identical f64.  There is no
// 754-permitted divergence anywhere in this chain, so any epsilon would
// only hand the attacker a free brute-force margin.

// isCpTick: k == 2^(n+2) for some positive integer n (8, 16, 32, ...).
func isCpTick(k int) bool { return k >= 8 && k&(k-1) == 0 }

// cpCount: number of checkpoint ticks in [0, numTicks).
func cpCount(numTicks int) int {
	c := 0
	for k := 8; k < numTicks; k *= 2 {
		c++
	}
	return c
}

// replayCheck: replay must end EXACTLY on the solve tick — solved at the
// final tick, never earlier (tail), never not at all — and every reported
// checkpoint position must match bit-for-bit.  Precondition:
// len(cps) == cpCount(len(ticks)), checked by the caller.
func replayCheck(seed int32, ticks, cps [][]float64) (bool, string) {
	g := NewGame(seed)
	last := len(ticks) - 1
	ci := 0
	for k, tk := range ticks {
		if len(tk) != 3 {
			return false, "row"
		}
		g.step(tk[0], tk[1], tk[2] != 0)
		if g.solved && k < last {
			return false, "tail"
		}
		if isCpTick(k) {
			if ci >= len(cps) {
				return false, "cplen"
			}
			if len(cps[ci]) != 2 {
				return false, "cprow"
			}
			bx, by := g.ballGridPos()
			if cps[ci][0] != bx || cps[ci][1] != by {
				return false, fmt.Sprintf("cp@%d", k)
			}
			ci++
		}
	}
	if !g.solved {
		return false, "unsat"
	}
	return true, ""
}

var proxy *httputil.ReverseProxy

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if hasValidCookie(r) {
		if proxy != nil {
			proxy.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "congratulations")
		return
	}
	html := bytes.Replace(pageHTML, []byte("ENGINE_JS"), engineJS, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write(html)
}

func gcLoop() {
	for range time.Tick(gcEvery) {
		mu.Lock()
		now := time.Now()
		for s, t := range tickets {
			if now.After(t.expire) {
				delete(tickets, s)
			}
		}
		floor := now.Add(-bucketDrop)
		for ip, w := range buckets {
			if w.Before(floor) {
				delete(buckets, ip)
			}
		}
		mu.Unlock()
	}
}

func main() {
	cfg := loadConfig()
	listenAddr = cfg.Listen
	upstream = cfg.Upstream
	password = cfg.SignKey
	thrHuman = cfg.Threshold

	mustLoadDisc()
	if upstream != "" {
		u, err := url.Parse(upstream)
		if err != nil {
			log.Fatal("bad upstream: ", err)
		}
		proxy = httputil.NewSingleHostReverseProxy(u)
	}
	go gcLoop()
	http.HandleFunc("/challenge", handleChallenge)
	http.HandleFunc("/solve", handleSolve)
	http.HandleFunc("/", handleRoot)
	log.Printf("waf listening on %s (upstream=%q, thr=%.6f)", listenAddr, upstream, thrHuman)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
