// Package server serves the install pages and payloads on loopback, for a
// transport to expose.
package server

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jonathan-lor/otata/internal/version"
)

// mimeOverrides are types the phones insist on: iOS rejects a manifest
// served as octet-stream, and Android only offers to install an APK served
// as its own type.
var mimeOverrides = map[string]string{
	".ipa":   "application/octet-stream",
	".plist": "application/xml",
	".apk":   "application/vnd.android.package-archive",
}

type Server struct {
	// root confines every file access to the served directory, symlinks included.
	root *os.Root

	// prefix is the path a forwarding proxy leaves on every request, which the
	// server strips.
	prefix string

	// identity is the RootDigest of the store served, so clients can tell roots apart.
	identity string

	logger *log.Logger
}

func New(publicDir, incomingPrefix, identity string, logger *log.Logger) (*Server, error) {
	root, err := os.OpenRoot(publicDir)
	if err != nil {
		return nil, fmt.Errorf("could not open %s: %w", publicDir, err)
	}
	if logger == nil {
		logger = log.New(os.Stdout, "", 0)
	}
	return &Server{root: root, prefix: strings.Trim(incomingPrefix, "/"), identity: identity, logger: logger}, nil
}

func (s *Server) Close() error { return s.root.Close() }

// Listen binds loopback only, never every interface.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Identify the server on every response, errors included, so the CLI can
	// tell it from another process on the port and spot an outdated one.
	w.Header().Set("X-Otata", "1")
	w.Header().Set("X-Otata-Pid", strconv.Itoa(os.Getpid()))
	w.Header().Set("X-Otata-Root", s.identity)
	w.Header().Set("X-Otata-Version", version.String())

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.fail(w, r, http.StatusMethodNotAllowed)
		return
	}

	name, ok := s.resolve(r.URL.Path)
	if !ok {
		s.fail(w, r, http.StatusNotFound)
		return
	}

	f, err := s.root.Open(name)
	if err != nil {
		// Includes escape attempts, which os.Root rejects outright.
		s.fail(w, r, http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		s.fail(w, r, http.StatusNotFound)
		return
	}
	if info.IsDir() {
		// Redirect a directory to its trailing-slash form. The Location is
		// relative, since behind a stripping proxy the server can't know the
		// phone's path.
		if !strings.HasSuffix(r.URL.Path, "/") {
			target := path.Base(r.URL.Path) + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			w.Header().Set("Location", target)
			s.log(r, http.StatusMovedPermanently)
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		// Directories serve their index, never a listing.
		name = path.Join(name, "index.html")
		f.Close()
		if f, err = s.root.Open(name); err != nil {
			s.fail(w, r, http.StatusNotFound)
			return
		}
		defer f.Close()
		if info, err = f.Stat(); err != nil || info.IsDir() {
			s.fail(w, r, http.StatusNotFound)
			return
		}
	}

	if ct, override := mimeOverrides[strings.ToLower(path.Ext(name))]; override {
		w.Header().Set("Content-Type", ct)
	}
	// Payload URLs stay the same across builds, so never cache.
	w.Header().Set("Cache-Control", "no-store")

	// Record the status ServeContent actually sends, for the log.
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	// ServeContent handles ranges and conditional requests, which large
	// payloads need.
	http.ServeContent(rec, r, path.Base(name), info.ModTime(), f)
	s.log(r, rec.status)
}

// resolve maps an already-decoded request path to a name inside the root.
// With a prefix set, a request without it isn't for otata.
func (s *Server) resolve(urlPath string) (string, bool) {
	clean := path.Clean("/" + urlPath)
	trimmed := strings.TrimPrefix(clean, "/")

	if s.prefix != "" {
		if trimmed == s.prefix {
			trimmed = ""
		} else if after, found := strings.CutPrefix(trimmed, s.prefix+"/"); found {
			trimmed = after
		} else {
			return "", false
		}
	}
	if trimmed == "" {
		trimmed = "."
	}
	if strings.Contains(trimmed, "\x00") {
		return "", false
	}
	return trimmed, true
}

// statusRecorder remembers the status code actually written.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int) {
	s.log(r, code)
	http.Error(w, http.StatusText(code), code)
}

func (s *Server) log(r *http.Request, code int) {
	s.logger.Printf("%s %q %d", time.Now().Format("2006-01-02 15:04:05"), r.Method+" "+r.URL.RequestURI(), code)
}
