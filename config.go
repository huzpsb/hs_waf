package main

// config.json — runtime configuration.  On first start the file is written
// with defaults; afterwards it is read at every start.  A partial file
// keeps defaults for absent keys.

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
)

const configPath = "config.json"

type config struct {
	Threshold float64 `json:"threshold"` // p(human) gate
	Upstream  string  `json:"upstream"`  // "" -> plain "congratulations" page (test mode)
	Listen    string  `json:"listen"`    // e.g. ":8081"
	SignKey   string  `json:"sign_key"`  // md5 salt for the day cookie
}

func defaultConfig() config {
	return config{
		Threshold: 0.45,
		Upstream:  "https://example.com",
		Listen:    ":8081",
		SignKey:   "hunter2",
	}
}

// loadConfig: read config.json; auto-generate it from defaults if missing.
func loadConfig() config {
	cfg := defaultConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Fatalf("config: read %s: %v", configPath, err)
		}
		writeConfig(cfg)
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("config: parse %s: %v", configPath, err)
	}
	cfg.validate()
	return cfg
}

func writeConfig(cfg config) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Fatalf("config: marshal: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		log.Fatalf("config: write %s: %v", configPath, err)
	}
	log.Printf("config: %s not found, wrote defaults (edit and restart to change)", configPath)
}

func (c *config) validate() {
	if c.Listen == "" {
		log.Fatal(`config: "listen" must not be empty (e.g. ":8081")`)
	}
	if c.Threshold <= 0 || c.Threshold >= 1 {
		log.Fatalf(`config: "threshold" must be in (0, 1), got %v`, c.Threshold)
	}
	if c.Upstream != "" {
		u, err := url.Parse(c.Upstream)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			log.Fatalf(`config: bad "upstream" %q (want http(s)://host[:port], or "" for test mode)`, c.Upstream)
		}
	}
	if c.SignKey == "" {
		log.Fatal(`config: "sign_key" must not be empty (cookie salt)`)
	}
}
