package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"webcam-stream/stream/transcoder"
)

type Server struct {
	http *http.Server
	log  *slog.Logger
}

type Options struct {
	Listen    string
	HLSDir    string
	AuthToken string
	Static    fs.FS
	Status    func() transcoder.Status
	Log       *slog.Logger
}

func New(o Options) (*Server, error) {
	loginPage, err := fs.ReadFile(o.Static, "login.html")
	if err != nil {
		return nil, err
	}
	indexPage, err := fs.ReadFile(o.Static, "index.html")
	if err != nil {
		return nil, err
	}

	auth := NewAuth(o.AuthToken)
	mux := http.NewServeMux()

	mux.HandleFunc("/login", auth.HandleLogin(loginPage))
	mux.HandleFunc("POST /logout", auth.HandleLogout)

	mux.Handle("GET /{$}", auth.Require(true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(indexPage)
	})))

	mux.Handle("GET /static/", auth.Require(false, http.StripPrefix("/static/", http.FileServerFS(o.Static))))
	mux.Handle("GET /hls/", auth.Require(false, http.StripPrefix("/hls/", HLSHandler(o.HLSDir))))

	mux.Handle("GET /healthz", auth.Require(false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := o.Status()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if !st.Ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(st)
	})))

	h := securityHeaders(logRequests(o.Log, mux))

	return &Server{
		log: o.Log,
		http: &http.Server{
			Addr:              o.Listen,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		},
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("HTTP server jalan", "addr", "http://"+s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.http.Shutdown(shutdownCtx)
}

func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' https://cdn.jsdelivr.net; " +
		"style-src 'self' 'unsafe-inline'; " +
		"media-src 'self' blob:; worker-src 'self' blob:; " +
		"img-src 'self' data:; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if strings.HasPrefix(r.URL.Path, "/hls/") || r.URL.Path == "/healthz" {
			level = slog.LevelDebug
		}
		log.Log(r.Context(), level, "request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"dur", time.Since(start).Round(time.Millisecond))
	})
}
