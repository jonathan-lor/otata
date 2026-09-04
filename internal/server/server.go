// Package server exposes the install surface over loopback.
//
// A transport is what makes this reachable from a phone. the server never binds a public interface.
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

// Extensions no system MIME table knows, and which the phones are strict
// about. iOS's install daemon rejects a manifest served as octet-stream, and
// Android's browser offers to install an APK only under its own type; as
// octet-stream it is just a download.
var mimeOverrides = map[string]string{
	".ipa":   "application/octet-stream",
	".plist": "application/xml",
	".apk":   "application/vnd.android.package-archive",
}

type Server struct {
	// root confines every file operation to the served directory at the syscall
	// level, which is stronger than sanitizing strings. percent-decoding turns %2F into
	// a separator and %2e%2e into "..", and a symlink escapes a clean-looking path.
	root *os.Root

	// prefix is what every request still carries when it arrives: the base URL's
	// path under a proxy that forwards it unchanged. Tailscale strips its mount
	// path, so there it is empty. The server strips exactly this and nothing else.
	prefix string

	// identity names the store this server serves, so a client can tell "an otata
	// server is on the port" from "the otata server for MY root is". Without it a
	// publish under one OTATA_ROOT writes into its own tree while the server on
	// the port serves another, and reports a URL that 404s.
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

// Listen binds loopback only. Passing ":port" would accept on every interface
// and expose payloads to the local network instead of only the transport.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Identity on every response, including errors. Port occupancy is not process
	// identity: without it, "is our server up?" cannot be told from "something
	// else holds the port", and stopping ours could signal an unrelated process.
	// The version rides along so status and doctor can see a long-lived server
	// still running the build an upgrade has since replaced on disk.
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
		// A directory without its trailing slash gets one first. The pages link
		// icons and the index relatively ("icon.png", "../"), and "/myapp" would
		// resolve those a level too high. The Location is relative for the same
		// reason the links are: behind a stripping proxy the server cannot know
		// the phone's path.
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
		// Directories serve their index; there is no listing ever.
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
	// A payload URL is stable across builds, so caching must be defeated
	// deliberately rather than relied upon to notice a change.
	w.Header().Set("Cache-Control", "no-store")

	// ServeContent may answer 206, 304 or 416, so the status has to be observed
	// rather than assumed; the log is the only visibility a background server has.
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	// *os.File is already an io.ReadSeeker. ServeContent handles ranges, 206,
	// 416 and conditional requests, which a large payload over a relayed link
	// genuinely needs.
	http.ServeContent(rec, r, path.Base(name), info.ModTime(), f)
	s.log(r, rec.status)
}

// resolve maps a request path to a name inside the root. Go has already
// percent-decoded URL.Path, so this runs after the decoding that makes %2F
// dangerous. path.Clean plus os.Root is what makes it inert.
//
// A configured prefix is required because a proxy that forwards the path
// unchanged never produces a bare request, so one that arrives is not otata's.
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
