package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"
)

const cookieName = "ws_session"

type Auth struct {
	token   string
	session string
}

func NewAuth(token string) *Auth {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("webcam-stream-session-v1"))
	return &Auth{token: token, session: hex.EncodeToString(mac.Sum(nil))}
}

func (a *Auth) valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.session)) == 1
}

func (a *Auth) Require(page bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.valid(r) {
			next.ServeHTTP(w, r)
			return
		}
		if page {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func (a *Auth) HandleLogin(loginPage []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if a.valid(r) {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			page := loginPage
			if r.URL.Query().Has("err") {
				page = bytes.Replace(page, []byte(`class="err"`), []byte(`class="err show"`), 1)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(page)

		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			given := r.FormValue("token")
			if subtle.ConstantTimeCompare([]byte(given), []byte(a.token)) != 1 {
				time.Sleep(time.Second)
				http.Redirect(w, r, "/login?err=1", http.StatusSeeOther)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    a.session,
				Path:     "/",
				MaxAge:   int((30 * 24 * time.Hour).Seconds()),
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
