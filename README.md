# hs_waf

A post-AI CAPTCHA. Users prove they are human by playing a tiny physics game: dragging the head of a soft rope and swinging its ball-shaped end into a moving target. The server verifies two things — that the computation really happened (a deterministic, constant-cost replay of the physics with spot-checked intermediate states), and that the pointer trajectory *looks human* (a small 1-D CNN discriminator that lives entirely server-side).

No obfuscation, no visual puzzles, no secrets in the client. The challenge engine is public and bit-exact between browser and server; what is hidden is the statistics.

## Why

Code obfuscation stopped being a defense when reverse-engineering dropped from "person-months" to "GPU-hours", and visual puzzles are free training data for VLM vendors. A post-AI CAPTCHA should instead rest on:

- **Fairness** — the server's verification cost is at most a constant factor above the client's (checkpoints at power-of-two ticks bound the gap by 2), so no request can burn defender resources for free.
- **Verifiability** — every submission must carry proof that the full closed-loop simulation actually ran; replayed garbage dies after 9 ticks.
- **Time dependence** — the rope is chaotic dynamics: every sample depends on the whole input history, so offline/open-loop trajectories and pointwise adversarial perturbations both fail.
- **A hidden discriminator** — the puzzle is not a VLM task; "seeing" the target is trivial, *controlling* the rope is the challenge. The scorer, its threshold and its preprocessing never reach the client.

## Quick start

```sh
go build -o waf .
./waf
```

On first start a `config.json` is written with defaults and the server listens on `:8081`. Open it, fail the challenge once, play the game, get proxied to the upstream.

## Configuration

All runtime configuration lives in `config.json` next to the binary.

- **Missing file** → generated automatically with defaults (mode `0600`), logged, and used for the current run.
- **Missing keys** → fall back to defaults (a partial file is fine).
- **Invalid values** → the server refuses to start with a clear message.
- Changes take effect on restart only.

```json
{
  "threshold": 0.45,
  "upstream": "https://example.com",
  "listen": ":8081",
  "sign_key": "hunter2"
}
```

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `listen` | string | `":8081"` | Address to listen on (`host:port` or `:port`). |
| `upstream` | string | `"https://example.com"` | Backend to proxy to once a client is verified. Set to `""` for **test mode**: verified visitors get a plain `congratulations` page and nothing is proxied. |
| `sign_key` | string | `"hunter2"` | Secret salt for the day cookie: `md5(sign_key + client_ip + UTC_date)`, valid until UTC midnight. **Change this** — with the default (or any short guessable key) anyone can forge cookies. Use a long random string. |
| `threshold` | float | `0.45` | p(human) gate. Lower = stricter (more bots blocked, more humans annoyed); higher = looser. Must lie in (0, 1). Tuned to the EER ≈ 13.8 % point against the frozen probe set. |

Example — local test without any backend:

```json
{
  "upstream": "",
  "listen": "127.0.0.1:8081",
  "sign_key": "8f4b2c1d9e7a6503b8f2",
  "threshold": 0.45
}
```

Rate limiting and other knobs (leaky-bucket water levels, ticket TTL, replay concurrency, max body size, tick cap) are compile-time constants in [`main.go`](main.go) — deliberately not runtime-configurable.

## HTTP surface

| Route | Method | Behaviour |
|-------|--------|-----------|
| `/` | GET | Valid cookie → proxy to `upstream` (or `congratulations` in test mode). Otherwise `401` + the challenge page. |
| `/challenge` | POST | Issues a one-time ticket seed (TTL 10 min, bound to the caller's IP). |
| `/solve` | POST | `{seed, ticks, cps}`: ticket burn, deterministic replay with checkpoint proofs at ticks 2^n (n ≥ 3), then the discriminator. Pass → `Set-Cookie` (`waf`); machine → `403`; malformed → `400` + 30 min penalty. |

## Repository layout

| File | Role |
|------|------|
| `main.go` | HTTP handlers, tickets, leaky bucket, cookie auth, reverse proxy. |
| `fasttn.go` | The game engine (Go) — hand-rolled fdlibm kernels for bit-exact replay. |
| `engine.js` | The same engine, line-by-line, for the browser. |
| `page.html` | Challenge page served on `401`. |
| `disc.go` | Server-side 1-D InceptionTime-style discriminator (~100k params). |
| `weights.json` | Frozen discriminator weights — never sent to the client. |

## Performance

Against the frozen test set: discriminator EER ≈ 13.8 %, human first-try pass rate ≈ 91.2 % (a failed human just replays — the game takes seconds), machine probe bypass ≈ 6.6 % over a mixed probe pool, 0.7 % for the single strongest probe.

## Full version

This repository ships the FOSS discriminator (~100k parameters). A substantially stronger model (~500k parameters) with clearly better numbers, plus the full training pipeline, is available — email [1@868322.xyz](mailto:1@868322.xyz) to inquire.

## Acknowledgements

Human and machine pointer-trajectory data used to train the bundled discriminator is from [CaptchaSolve30k](https://huggingface.co/datasets/Capycap-AI/CaptchaSolve30k) by Capycap-AI, released under the MIT License. The 1-D discriminator architecture follows the InceptionTime line of work.

## License

MIT.
