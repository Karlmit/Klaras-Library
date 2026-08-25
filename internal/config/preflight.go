package config

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// PathCheck is the result of testing one configured directory.
//
// Missing and unwritable are kept apart deliberately: a read-only library is a
// legitimate way to run, while a library that is not there at all is not.
type PathCheck struct {
	Name     string
	Path     string
	Required bool
	Exists   bool
	Writable bool
	// Err is set when the directory is missing or is not a directory.
	Err error
	// WriteErr is set when it exists but cannot be written to.
	WriteErr error
}

// Preflight verifies the configured directories before anything relies on them.
//
// This exists because the failure it catches is silent and total. Docker
// creates a missing bind-mount host directory as root, so on Unraid -- where
// the container runs as 99:100 -- an appdata path that did not already exist
// is unwritable. Cover generation then fails for every book and quietly serves
// a placeholder, which looks like "the covers are broken" with nothing in the
// logs to say why. KEPUB conversion fails the same way, which degrades Kobo
// sync.
func (c *Config) Preflight() []PathCheck {
	checks := []PathCheck{
		{Name: "library", Path: c.LibraryRoot, Required: true},
		{Name: "cache", Path: c.CacheDir, Required: true},
	}
	if c.IngestDir != "" {
		checks = append(checks, PathCheck{Name: "ingest", Path: c.IngestDir})
	}

	for i := range checks {
		ck := &checks[i]
		if ck.Path == "" {
			continue
		}
		st, err := os.Stat(ck.Path)
		if err != nil {
			ck.Err = err
			continue
		}
		if !st.IsDir() {
			ck.Err = fmt.Errorf("not a directory")
			continue
		}
		// Actually write, rather than inferring from the mode bits: the mode
		// says nothing useful about a share mounted with squashed ownership.
		probe := filepath.Join(ck.Path, ".klaras-write-probe")
		ck.Exists = true
		f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			ck.WriteErr = err
			continue
		}
		f.Close()
		os.Remove(probe)
		ck.Writable = true
	}
	return checks
}

// CheckExternalURL verifies the address handed to Kobo devices.
//
// This deserves its own check because the consequence outlives the mistake.
// /v1/initialization returns URLs built from KLARAS_EXTERNAL_URL and the device
// WRITES THEM INTO ITS OWN CONFIG. Point it at a host that does not resolve and
// the reader keeps the bad address after the server is fixed, so a typo here
// quietly breaks a device until someone edits its configuration by hand.
func (c *Config) CheckExternalURL(log *slog.Logger) bool {
	if c.ExternalURL == "" {
		log.Warn("KLARAS_EXTERNAL_URL is not set: Kobo sync cannot work, because the " +
			"device needs absolute URLs it can resolve itself")
		return false
	}
	u, err := url.Parse(c.ExternalURL)
	if err != nil || u.Host == "" {
		log.Error("KLARAS_EXTERNAL_URL is not a usable URL", "value", c.ExternalURL)
		return false
	}

	host := u.Hostname()
	if ip := net.ParseIP(host); ip == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
			log.Error("KLARAS_EXTERNAL_URL does not resolve", "host", host, "err", err)
			for _, line := range []string{
				"Kobo devices are handed this address and SAVE IT to their own config,",
				"so a wrong hostname keeps breaking the device after the server is fixed.",
				"Set it to the public address the DEVICE can reach, then restart.",
				"If a device has already stored the wrong one, it corrects itself on the",
				"next successful sync -- or edit api_endpoint and image_host in",
				".kobo/Kobo/Kobo eReader.conf on the device.",
			} {
				log.Error(line)
			}
			return false
		}
	}
	log.Info("external URL looks reachable", "url", c.ExternalURL)
	return true
}

// LogPreflight reports the checks, loudly when something is wrong.
//
// Returns true if everything the server needs is usable.
func (c *Config) LogPreflight(log *slog.Logger) bool {
	ok := true
	for _, ck := range c.Preflight() {
		switch {
		case ck.Err != nil:
			ok = false
			log.Error("directory is missing or unusable",
				"what", ck.Name, "path", ck.Path, "err", ck.Err)

		case !ck.Writable && ck.Name == "library":
			// Browsing and Kobo sync work fine against a read-only library;
			// only editing, ingest and reorganize do not. Not a failure.
			log.Warn("library is read-only: editing, ingest and reorganize will not work",
				"path", ck.Path)

		case !ck.Writable:
			ok = false
			log.Error("directory is not writable",
				"what", ck.Name, "path", ck.Path, "err", ck.WriteErr)
		}
	}
	if !ok {
		// One line per line, because slog escapes embedded newlines into
		// literal \n and the guidance becomes unreadable exactly when someone
		// most needs to read it.
		for _, line := range []string{
			"PERMISSIONS PROBLEM: covers and KEPUB conversion will not work.",
			"The container runs as the uid from the compose 'user:' setting (99:100 on Unraid).",
			"Docker creates a missing bind-mount directory owned by root, which that uid cannot write.",
			"Fix it on the HOST, not inside the container. In your compose file, find",
			"the host directory mapped to " + c.CacheDir + " -- on Unraid that is usually",
			"/mnt/user/appdata/klaras-library/cache -- then run:",
			"    mkdir -p  <that host directory>",
			"    chown -R 99:100  <that host directory>",
			"and restart the container.",
			"Change ONLY that directory. Do not chown its parent appdata folder: that",
			"also holds the Postgres data directory, which must stay owned by uid 70,",
			"and Postgres will not start without it.",
		} {
			log.Error(line)
		}
	}
	return ok
}
