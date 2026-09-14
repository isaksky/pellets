package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSettingsHTTPPersistenceAndSecurity(t *testing.T) {
	f := newHandlerFixture(t, 1)
	send := func(body, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", testOrigin+"/settings", strings.NewReader(body))
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w
	}
	body := `{"_csrf":"` + testCSRF + `","key":"theme","value":"icy"}`
	if w := send(body, "bad"); w.Code != 403 {
		t.Fatalf("CSRF: %d %s", w.Code, w.Body.String())
	}
	if w := send(body, testCSRF); w.Code != 200 || !strings.Contains(w.Body.String(), `"theme":"icy"`) {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := send(strings.Replace(body, `"icy"`, `"invalid"`, 1), testCSRF); w.Code < 400 {
		t.Fatal("invalid theme accepted")
	}
	if w := send(body+`{}`, testCSRF); w.Code < 400 {
		t.Fatal("trailing JSON accepted")
	}
	r := httptest.NewRequest("GET", testOrigin+"/settings", nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"theme":"icy"`) {
		t.Fatalf("read: %s", w.Body.String())
	}
	r = httptest.NewRequest("GET", testOrigin+"/projects/"+f.projects[0].Code+"/tasks", nil)
	w = httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `data-settings="{&#34;theme&#34;:&#34;icy&#34;}"`) {
		t.Fatalf("initial HTML missing saved theme: %d", w.Code)
	}
}
