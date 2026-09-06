package console

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sync"
	"time"
)

const SessionCookieName = "console_session"

const SessionTTL = 8 * time.Hour

type Store struct {
	mu       sync.Mutex
	sessions map[string]time.Time
}

func NewStore() *Store {
	return &Store{sessions: make(map[string]time.Time)}
}

func (s *Store) Create() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {

		panic("console: generate session id: " + err.Error())
	}
	id := hex.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for k, exp := range s.sessions {
		if exp.Before(now) {
			delete(s.sessions, k)
		}
	}
	s.sessions[id] = now.Add(SessionTTL)
	return id
}

func (s *Store) Validate(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[id]
	if !ok || exp.Before(time.Now()) {
		delete(s.sessions, id)
		return false
	}
	return true
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

//go:embed all:dist
var distFS embed.FS

func Static() http.Handler {
	sub, serr := fs.Sub(distFS, "dist")
	if serr != nil {
		panic("console: embedded dist subtree: " + serr.Error())
	}
	serve := func(w http.ResponseWriter, r *http.Request, name string) {
		f, err := sub.Open(name)
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"console_not_built","message":"admin console assets are not built; run: cd web/admin && npm run build"}}`))
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, st.Name(), st.ModTime(), rs)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"console_not_built","message":"admin console assets are not built; run: cd web/admin && npm run build"}}`))
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		if clean != "/" {
			if _, err := fs.Stat(sub, trimLeading(clean)); err == nil {
				serve(w, r, trimLeading(clean))
				return
			}
		}

		serve(w, r, "index.html")
	})
}

func trimLeading(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
