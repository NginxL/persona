package remoteimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The avatars route's profile (#10923, docs/protocol/protocol.md "Avatars"): https on the open internet only, on every
// redirect too; no cookies or credentials; PNG, JPEG, WebP and GIF read from the bytes; 4 MiB, 16 megapixels, 10 s;
// and an extension's own bytes held to the same rules.

func avatarRig(t *testing.T, hosts map[string]string, handler http.HandlerFunc) *rig {
	t.Helper()
	return newRigOf(t, Avatars, hosts, handler)
}

func TestAvatarsRefuseAddressesOffTheInternet(t *testing.T) {
	r := avatarRig(t, map[string]string{
		"private.test": privateIP,
		"lan.test":     lanIP,
		"meta.test":    metadataIP4,
		"loop.test":    "127.0.0.1",
		"mixed.test":   publicIP + "," + lanIP, // a rebinding host's answer
		"v6loop.test":  "::1",
	}, serve(pngBytes(t, 4, 4, false), "image/png"))
	for _, host := range []string{"private.test", "lan.test", "meta.test", "loop.test", "mixed.test", "v6loop.test"} {
		if _, err := r.f.GetIn(t.Context(), "phone", "telegram", "https://"+host+"/a.png", 128); code(err) != CodeNotAllowed {
			t.Errorf("%s: %v, want %s", host, err, CodeNotAllowed)
		}
	}
	for _, addr := range []string{"https://127.0.0.1/a.png", "https://[::1]/a.png", "https://" + metadataIP4 + "/latest/meta-data/"} {
		if _, err := r.f.GetIn(t.Context(), "phone", "telegram", addr, 128); code(err) != CodeNotAllowed {
			t.Errorf("%s: %v, want %s", addr, err, CodeNotAllowed)
		}
	}
	if n := r.hits.Load(); n != 0 || len(r.dialed) != 0 {
		t.Fatalf("refused hosts reached the server %d times, dialled %v", n, r.dialed)
	}
}

func TestAvatarsTakeHTTPSOnly(t *testing.T) {
	r := avatarRig(t, map[string]string{"img.test": publicIP}, serve(pngBytes(t, 4, 4, false), "image/png"))
	for _, addr := range []string{"http://img.test/a.png", "https://user:secret@img.test/a.png", "ftp://img.test/a.png", "file:///etc/passwd"} {
		if _, err := r.f.GetIn(t.Context(), "phone", "telegram", addr, 128); code(err) != CodeBadURL {
			t.Errorf("%s: %v, want %s", addr, err, CodeBadURL)
		}
	}
	if n := r.hits.Load(); n != 0 {
		t.Fatalf("refused addresses reached the server %d times", n)
	}
	img, err := r.f.GetIn(t.Context(), "phone", "telegram", "https://img.test/a.png", 128)
	if err != nil || img.Type != "image/jpeg" {
		t.Fatalf("an https picture: %v, %v", img, err)
	}
}

func TestAvatarsRedirectsAreCheckedAtEveryHop(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	r := avatarRig(t, map[string]string{"img.test": publicIP, "cdn.test": publicIP, "internal.test": privateIP}, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen = append(seen, req.Header.Get("Cookie")+"|"+req.Header.Get("Referer")+"|"+req.Header.Get("Authorization"))
		mu.Unlock()
		switch req.URL.Path {
		case "/hop":
			http.SetCookie(w, &http.Cookie{Name: "track", Value: "1"})
			http.Redirect(w, req, "https://cdn.test/final.png", http.StatusFound)
		case "/to-internal":
			http.Redirect(w, req, "https://internal.test/secret", http.StatusFound)
		case "/to-loopback":
			http.Redirect(w, req, "https://127.0.0.1:9/x", http.StatusFound)
		case "/to-metadata":
			http.Redirect(w, req, "https://"+metadataIP4+"/latest/meta-data/", http.StatusFound)
		case "/to-http":
			http.Redirect(w, req, "http://cdn.test/final.png", http.StatusFound)
		case "/loop":
			http.Redirect(w, req, "/loop", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes(t, 4, 4, false))
		}
	})
	if _, err := r.f.GetIn(t.Context(), "phone", "telegram", "https://img.test/hop", 128); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "||" || seen[1] != "||" {
		t.Fatalf("the requests carried %q, want no cookie, Referer or credentials", seen)
	}
	for path, want := range map[string]string{"/to-internal": CodeNotAllowed, "/to-loopback": CodeNotAllowed, "/to-metadata": CodeNotAllowed,
		"/to-http": CodeBadURL, "/loop": CodeFetchFailed} {
		if _, err := r.f.GetIn(t.Context(), "phone", "telegram", "https://img.test"+path, 128); code(err) != want {
			t.Errorf("%s: %v, want %s", path, err, want)
		}
	}
	for _, d := range r.dialed {
		if d != publicIP+":443" {
			t.Errorf("dialled %s: only the checked public number may be", d)
		}
	}
}

func TestAvatarsLimits(t *testing.T) {
	big := make([]byte, Avatars.MaxSource+10)
	copy(big, pngBytes(t, 1, 1, false))
	slow := make(chan struct{})
	t.Cleanup(func() { close(slow) })
	r := avatarRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/declared":
			w.Header().Set("Content-Length", fmt.Sprint(Avatars.MaxSource+1))
			w.WriteHeader(http.StatusOK)
		case "/streamed":
			w.Header().Set("Content-Type", "image/png")
			w.(http.Flusher).Flush() // no Content-Length: the limit is counted while reading
			_, _ = w.Write(big)
		case "/pixels":
			_, _ = w.Write(hugePNG(t, 5000, 4000)) // 20 megapixels: under /remote-image's limit, over the avatars'
		case "/slow":
			select {
			case <-slow:
			case <-req.Context().Done():
			}
		}
	})
	r.f.o.timeout = 300 * time.Millisecond
	for path, want := range map[string]string{"/declared": CodeTooLarge, "/streamed": CodeTooLarge, "/pixels": CodeTooLarge, "/slow": CodeTimeout} {
		if _, err := r.f.GetIn(t.Context(), "phone", "telegram", "https://img.test"+path, 128); code(err) != want {
			t.Errorf("%s: %v, want %s", path, err, want)
		}
	}
}

// heicBytes is the start of a HEIC file (its ftyp box): /remote-image passes those through, avatars do not.
func heicBytes() []byte {
	return append([]byte{0, 0, 0, 24}, append([]byte("ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 64)...)...)
}

func TestAvatarsTypesAreReadFromTheBytes(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><rect width="4" height="4"/></svg>`)
	html := []byte("<!doctype html><script>alert(1)</script>")
	r := avatarRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "image/png") // whatever it is
		switch req.URL.Path {
		case "/svg":
			_, _ = w.Write(svg)
		case "/html":
			_, _ = w.Write(html)
		case "/heic":
			_, _ = w.Write(heicBytes())
		case "/png-as-html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(pngBytes(t, 600, 400, false))
		}
	})
	for path, want := range map[string]string{"/svg": CodeUnsupported, "/html": CodeUnsupported, "/heic": CodeUnsupported} {
		if _, err := r.f.GetIn(t.Context(), "phone", "telegram", "https://img.test"+path, 128); code(err) != want {
			t.Errorf("%s: %v, want %s", path, err, want)
		}
	}
	img, err := r.f.GetIn(t.Context(), "phone", "telegram", "https://img.test/png-as-html", 100)
	if err != nil {
		t.Fatal(err)
	}
	// Drawn again at the size asked, rounded up to the avatars' sizes (128), never served as it came.
	if img.Type != "image/jpeg" || img.Width != 128 || img.Height != 85 {
		t.Fatalf("drawn as %s %d×%d, want image/jpeg 128×85", img.Type, img.Width, img.Height)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data)); err != nil || cfg.Width != 128 {
		t.Fatalf("the bytes are not the picture drawn: %v %v", cfg, err)
	}
}

// An extension's own bytes (a media ref's) go through the same checks as a fetched address's: the hub never serves
// what an extension hands it as it is.
func TestAvatarsMakeHoldsAnExtensionsBytesToTheSameRules(t *testing.T) {
	f := New(Options{Profile: Avatars, CacheDir: t.TempDir()})
	give := func(data []byte, err error) func(context.Context) ([]byte, error) {
		return func(context.Context) ([]byte, error) { return data, err }
	}
	cases := map[string]struct {
		get  func(context.Context) ([]byte, error)
		want string
	}{
		"svg":     {give([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), nil), CodeUnsupported},
		"html":    {give([]byte("<html><script>alert(1)</script>"), nil), CodeUnsupported},
		"heic":    {give(heicBytes(), nil), CodeUnsupported},
		"big":     {give(append(pngBytes(t, 1, 1, false), make([]byte, Avatars.MaxSource)...), nil), CodeTooLarge},
		"pixels":  {give(hugePNG(t, 5000, 4000), nil), CodeTooLarge},
		"broken":  {give(append([]byte("\x89PNG\r\n\x1a\n"), "not really"...), nil), CodeBadImage},
		"failed":  {give(nil, errors.New("not signed in")), CodeFetchFailed},
		"refused": {give(nil, fail(CodeNotAllowed, http.StatusForbidden, "no")), CodeNotAllowed},
	}
	for name, c := range cases {
		if _, err := f.Make(t.Context(), "phone", "telegram", "a."+name, 128, c.get); code(err) != c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	var gets atomic.Int32
	good := func(context.Context) ([]byte, error) { gets.Add(1); return pngBytes(t, 300, 300, false), nil }
	img, err := f.Make(t.Context(), "phone", "telegram", "a.u1.501", 128, good)
	if err != nil || img.Width != 128 {
		t.Fatalf("a picture: %v, %v", img, err)
	}
	if _, err := f.Make(t.Context(), "phone", "telegram", "a.u1.501", 128, good); err != nil || gets.Load() != 1 {
		t.Fatalf("asked again: %v, got its bytes %d times, want once", err, gets.Load())
	}
	// A failed ref is answered from memory a while: a screen of rows does not ask the extension again and again.
	var failing atomic.Int32
	bad := func(context.Context) ([]byte, error) { failing.Add(1); return nil, errors.New("gone") }
	for range 3 {
		_, _ = f.Make(t.Context(), "phone", "telegram", "a.gone", 128, bad)
	}
	if failing.Load() != 1 {
		t.Fatalf("a failing ref was asked %d times, want once", failing.Load())
	}
	if _, err := f.Make(t.Context(), "phone", "../x", "a.u1.501", 128, good); code(err) != CodeBadURL {
		t.Fatalf("a directory outside the cache: %v", err)
	}
}

// A screen of rows asks for tens of new pictures at once: the per-device limit counts only what is fetched, so a
// screen loads whole and asking again (or for another size of a kept source) costs nothing.
func TestAvatarsLimitCountsOnlyFetches(t *testing.T) {
	const screen = 50
	if Avatars.PerCaller < screen*2 {
		t.Fatalf("Avatars.PerCaller %d does not let two screens of new pictures through a minute", Avatars.PerCaller)
	}
	f := New(Options{Profile: Avatars, CacheDir: t.TempDir()})
	f.limits = newLimiter(screen, time.Minute, time.Now) // exactly one screen's worth
	var gets atomic.Int32
	get := func(context.Context) ([]byte, error) { gets.Add(1); return pngBytes(t, 200, 200, false), nil }
	ask := func(size int) []error {
		errs := make([]error, screen)
		var wg sync.WaitGroup
		for i := range screen {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = f.Make(t.Context(), "phone", "telegram", fmt.Sprintf("a.u%d.1", i), size, get)
			}()
		}
		wg.Wait()
		return errs
	}
	for i, err := range ask(128) {
		if err != nil {
			t.Fatalf("row %d of the first screen: %v", i, err)
		}
	}
	for i, err := range append(ask(128), ask(64)...) {
		if err != nil {
			t.Fatalf("ask %d again (cached, or another size of a kept source): %v", i, err)
		}
	}
	if n := gets.Load(); n != screen {
		t.Fatalf("got %d sources, want %d", n, screen)
	}
	if _, err := f.Make(t.Context(), "phone", "telegram", "a.new.1", 128, get); code(err) != CodeRateLimited {
		t.Fatalf("one more new picture: %v, want %s", err, CodeRateLimited)
	}
}

func TestAvatarsKeepEachSourceApart(t *testing.T) {
	dir := t.TempDir()
	f := New(Options{Profile: Avatars, CacheDir: dir})
	get := func(context.Context) ([]byte, error) { return pngBytes(t, 50, 50, false), nil }
	for _, sub := range []string{"telegram", "github"} {
		if _, err := f.Make(t.Context(), "phone", sub, "a.u1.501", 128, get); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []string{"telegram", "github"} {
		if files, _ := os.ReadDir(filepath.Join(dir, sub)); len(files) != 2 { // its source and the size drawn
			t.Fatalf("%s keeps %d files, want 2", sub, len(files))
		}
	}
	if n := len(f.cache.list()); n != 4 {
		t.Fatalf("the cache counts %d files, want 4 (one cap over every source)", n)
	}
	f.Drop("telegram")
	if _, err := os.Stat(filepath.Join(dir, "telegram")); !os.IsNotExist(err) {
		t.Fatalf("a dropped source's directory stays: %v", err)
	}
	if n := len(f.cache.list()); n != 2 {
		t.Fatalf("after the drop the cache counts %d files, want github's 2", n)
	}
	for _, sub := range []string{"..", ".hidden", "a/b", `a\b`, "x y"} {
		if validSub(sub) {
			t.Errorf("%q names a directory of the cache", sub)
		}
	}
}

func TestAvatarsSnap(t *testing.T) {
	for in, want := range map[int]int{0: 128, -1: 128, 1: 64, 64: 64, 65: 128, 120: 128, 300: 512, 5000: 512} {
		if got := Avatars.Snap(in); got != want {
			t.Errorf("Snap(%d) = %d, want %d", in, got, want)
		}
	}
	if Snap(0) != DefaultSize || Snap(300) != 512 {
		t.Fatal("/remote-image's sizes changed")
	}
}
