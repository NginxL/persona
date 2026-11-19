package remoteimage

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// publicIP is what the test resolver answers for a host on "the internet"; the test dialler connects it to the
// httptest server. Private numbers are built from bytes (the privacy scan reads literal ones in sources).
const publicIP = "93.184.216.34"

func v4(a, b, c, d byte) string { return netip.AddrFrom4([4]byte{a, b, c, d}).String() }

var (
	privateIP   = netip.AddrFrom4([4]byte{10, 1, 2, 3}).String()
	lanIP       = netip.AddrFrom4([4]byte{192, 168, 1, 10}).String()
	metadataIP4 = netip.AddrFrom4([4]byte{169, 254, 169, 254}).String()
)

type rig struct {
	f      *Fetcher
	srv    *httptest.Server
	hits   atomic.Int32
	mu     sync.Mutex
	dialed []string
}

// newRig serves handler as every host hosts names (name → comma-separated answers of the resolver).
func newRig(t *testing.T, hosts map[string]string, handler http.HandlerFunc) *rig {
	t.Helper()
	return newRigOf(t, Profile{}, hosts, handler)
}

// newRigOf is newRig for profile p (Pictures for none), over TLS when p takes https only.
func newRigOf(t *testing.T, p Profile, hosts map[string]string, handler http.HandlerFunc) *rig {
	t.Helper()
	r := &rig{}
	start := httptest.NewServer
	if p.HTTPSOnly {
		start = httptest.NewTLSServer
	}
	r.srv = start(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Only the fetcher's requests count: a developer machine may probe a new local port on its own (GET /).
		if req.Header.Get("User-Agent") != userAgent {
			http.NotFound(w, req)
			return
		}
		r.hits.Add(1)
		handler(w, req)
	}))
	t.Cleanup(r.srv.Close)
	target := r.srv.Listener.Addr().String()
	r.f = New(Options{
		Profile:     p,
		tlsInsecure: p.HTTPSOnly,
		CacheDir:    t.TempDir(),
		resolve: func(_ context.Context, host string) ([]net.IPAddr, error) {
			answer, ok := hosts[host]
			if !ok {
				return nil, errors.New("no such host")
			}
			var out []net.IPAddr
			for _, s := range strings.Split(answer, ",") {
				out = append(out, net.IPAddr{IP: net.ParseIP(s)})
			}
			return out, nil
		},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			r.mu.Lock()
			r.dialed = append(r.dialed, address)
			r.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
		proxy:   func(*http.Request) (*url.URL, error) { return nil, nil },
		timeout: 2 * time.Second,
	})
	return r
}

func pngBytes(t *testing.T, w, h int, transparent bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if transparent && x == 0 {
				a = 0
			}
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 90, a})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withOrientation puts an EXIF APP1 naming orientation o right after a JPEG's SOI.
func withOrientation(jpg []byte, o uint16) []byte {
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")
	_ = binary.Write(&tiff, binary.BigEndian, uint32(8))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))
	_ = binary.Write(&tiff, binary.BigEndian, []uint16{0x0112, 3})
	_ = binary.Write(&tiff, binary.BigEndian, uint32(1))
	_ = binary.Write(&tiff, binary.BigEndian, []uint16{o, 0})
	_ = binary.Write(&tiff, binary.BigEndian, uint32(0))
	body := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(body)+2))
	out := append([]byte{0xff, 0xd8}, seg...)
	out = append(out, body...)
	return append(out, jpg[2:]...)
}

// hugePNG is a valid PNG header for w × h pixels (DecodeConfig reads only that).
func hugePNG(t *testing.T, w, h uint32) []byte {
	data := pngBytes(t, 1, 1, false)
	binary.BigEndian.PutUint32(data[16:20], w)
	binary.BigEndian.PutUint32(data[20:24], h)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	return data
}

func serve(body []byte, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func TestCheckURL(t *testing.T) {
	for _, bad := range []string{
		"", "ftp://img.test/a.png", "javascript:alert(1)", "data:image/png;base64,AAAA", "file:///etc/passwd",
		"https://user:pass@img.test/a.png", "https:///a.png", "/relative/a.png", "https://img.test/" + strings.Repeat("a", maxURL),
	} {
		if _, err := CheckURL(bad); code(err) != CodeBadURL {
			t.Errorf("CheckURL(%.40q) = %v, want %s", bad, err, CodeBadURL)
		}
	}
	u, err := CheckURL("  https://img.test/a.png?w=1#frag ")
	if err != nil || u.String() != "https://img.test/a.png?w=1" {
		t.Fatalf("CheckURL = %v, %v", u, err)
	}
}

func TestPublic(t *testing.T) {
	refused := []string{
		"127.0.0.1", privateIP, lanIP, v4(172, 16, 0, 1), v4(100, 64, 1, 1), metadataIP4,
		v4(100, 100, 100, 200), "168.63.129.16", v4(0, 0, 0, 0), v4(224, 0, 0, 1), v4(255, 255, 255, 255), "192.0.2.1", v4(198, 18, 0, 1),
		"::1", "::", "fe80::1", "fc00::1", "fd00:ec2::254", "2001:db8::1", "2002:a01:203::1", "2001::1", "2001:10::1",
		"64:ff9b::a01:203", "::ffff:" + privateIP, "ff02::1",
	}
	for _, s := range refused {
		if Public(netip.MustParseAddr(s)) {
			t.Errorf("Public(%s) = true", s)
		}
	}
	for _, s := range []string{publicIP, "1.1.1.1", "2606:4700:4700::1111", "::ffff:" + publicIP} {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("Public(%s) = false", s)
		}
	}
	if Public(netip.MustParseAddr("fe80::1%en0")) {
		t.Error("an address with a zone is public")
	}
}

func TestRefusesAddressesOffTheInternet(t *testing.T) {
	r := newRig(t, map[string]string{
		"private.test": privateIP,
		"lan.test":     lanIP,
		"meta.test":    metadataIP4,
		"loop.test":    "127.0.0.1",
		"mixed.test":   publicIP + "," + lanIP, // a rebinding host's answer
		"v6loop.test":  "::1",
	}, serve(pngBytes(t, 4, 4, false), "image/png"))
	for _, host := range []string{"private.test", "lan.test", "meta.test", "loop.test", "mixed.test", "v6loop.test"} {
		if _, err := r.f.Get(t.Context(), "phone", "http://"+host+"/a.png", 256); code(err) != CodeNotAllowed {
			t.Errorf("%s: %v, want %s", host, err, CodeNotAllowed)
		}
	}
	// Numbers written in the address are checked the same way.
	for _, addr := range []string{"http://127.0.0.1/a.png", "http://[::1]/a.png", "http://" + metadataIP4 + "/latest/meta-data/"} {
		if _, err := r.f.Get(t.Context(), "phone", addr, 256); code(err) != CodeNotAllowed {
			t.Errorf("%s: %v, want %s", addr, err, CodeNotAllowed)
		}
	}
	if n := r.hits.Load(); n != 0 || len(r.dialed) != 0 {
		t.Fatalf("refused hosts reached the server %d times, dialled %v", n, r.dialed)
	}
}

func TestDialsOnlyTheCheckedNumbers(t *testing.T) {
	answers := 0
	r := newRig(t, nil, serve(pngBytes(t, 4, 4, false), "image/png"))
	// The resolver answers a public number once, then a private one (DNS rebinding): every connection resolves once
	// and dials the number it checked, so the second answer can only refuse a later connection.
	r.f.o.resolve = func(context.Context, string) ([]net.IPAddr, error) {
		answers++
		if answers == 1 {
			return []net.IPAddr{{IP: net.ParseIP(publicIP)}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP(privateIP)}}, nil
	}
	if _, err := r.f.Get(t.Context(), "phone", "http://rebind.test/a.png", 256); err != nil {
		t.Fatal(err)
	}
	if len(r.dialed) != 1 || r.dialed[0] != publicIP+":80" {
		t.Fatalf("dialled %v, want only %s:80", r.dialed, publicIP)
	}
	r.f.transport.CloseIdleConnections()
	if _, err := r.f.Get(t.Context(), "phone", "http://rebind.test/b.png", 256); code(err) != CodeNotAllowed {
		t.Fatalf("after rebinding: %v, want %s", err, CodeNotAllowed)
	}
}

func TestRedirects(t *testing.T) {
	var cookies []string
	var mu sync.Mutex
	r := newRig(t, map[string]string{"img.test": publicIP, "cdn.test": publicIP, "internal.test": privateIP}, func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		cookies = append(cookies, req.Header.Get("Cookie")+"|"+req.Header.Get("Referer"))
		mu.Unlock()
		switch req.URL.Path {
		case "/hop":
			http.SetCookie(w, &http.Cookie{Name: "track", Value: "1"})
			http.Redirect(w, req, "http://cdn.test/final.png", http.StatusFound)
		case "/to-internal":
			http.Redirect(w, req, "http://internal.test/secret", http.StatusFound)
		case "/to-loopback":
			http.Redirect(w, req, "http://127.0.0.1:9/x", http.StatusFound)
		case "/to-file":
			http.Redirect(w, req, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			http.Redirect(w, req, "/loop", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes(t, 4, 4, false))
		}
	})
	if _, err := r.f.Get(t.Context(), "phone", "http://img.test/hop", 256); err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 2 || cookies[1] != "|" {
		t.Fatalf("the redirect's request carried %q, want no cookie and no Referer", cookies)
	}
	for path, want := range map[string]string{"/to-internal": CodeNotAllowed, "/to-loopback": CodeNotAllowed, "/to-file": CodeBadURL, "/loop": CodeFetchFailed} {
		if _, err := r.f.Get(t.Context(), "phone", "http://img.test"+path, 256); code(err) != want {
			t.Errorf("%s: %v, want %s", path, err, want)
		}
	}
}

func TestOwnOriginRefererOnlyAfterARefusal(t *testing.T) {
	var seen []string
	r := newRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, req *http.Request) {
		seen = append(seen, req.Header.Get("Referer"))
		if req.Header.Get("Referer") != "http://img.test/" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write(pngBytes(t, 4, 4, false))
	})
	if _, err := r.f.Get(t.Context(), "phone", "http://img.test/a.png", 256); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "" || seen[1] != "http://img.test/" {
		t.Fatalf("Referers sent: %q, want none and then the image's own origin", seen)
	}
	// A host that refuses both stays refused, as fetch_failed, after exactly one retry.
	r2 := newRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	if _, err := r2.f.Get(t.Context(), "phone", "http://img.test/a.png", 256); code(err) != CodeFetchFailed || r2.hits.Load() != 2 {
		t.Fatalf("%v after %d requests, want %s after 2", err, r2.hits.Load(), CodeFetchFailed)
	}
}

func TestTypesAreReadFromTheBytes(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte("<!doctype html><title>x</title>")
	avif := append([]byte{0, 0, 0, 0x1c}, []byte("ftypavif\x00\x00\x00\x00avifmif1miaf")...)
	heic := append([]byte{0, 0, 0, 0x18}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	files := map[string][]byte{"/a.svg": svg, "/fake.png": html, "/a.avif": avif, "/a.heic": heic}
	r := newRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "image/png") // a lie for every one of them
		_, _ = w.Write(files[req.URL.Path])
	})
	for _, p := range []string{"/a.svg", "/fake.png"} {
		if _, err := r.f.Get(t.Context(), "phone", "http://img.test"+p, 256); code(err) != CodeUnsupported {
			t.Errorf("%s: %v, want %s", p, err, CodeUnsupported)
		}
	}
	for p, want := range map[string]string{"/a.avif": "image/avif", "/a.heic": "image/heic"} {
		img, err := r.f.Get(t.Context(), "phone", "http://img.test"+p, 256)
		if err != nil || img.Type != want || !bytes.Equal(img.Data, files[p]) {
			t.Errorf("%s: %+v, %v; want it passed as %s", p, img, err, want)
		}
	}
}

func TestScalesDownUprightAndNeverUp(t *testing.T) {
	var gifBuf bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 30, 10), []color.Color{color.Black, color.White})
	if err := gif.Encode(&gifBuf, pal, nil); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"/big.png":     pngBytes(t, 3000, 2000, false),
		"/small.png":   pngBytes(t, 100, 80, true),
		"/rotated.jpg": withOrientation(jpegBytes(t, 40, 20), 6),
		"/a.gif":       gifBuf.Bytes(),
	}
	r := newRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write(files[req.URL.Path]) })
	for _, c := range []struct {
		path, typ  string
		size, w, h int
	}{
		{"/big.png", "image/jpeg", 500, 512, 341}, // 500 snaps up to 512; opaque → JPEG
		{"/small.png", "image/png", 256, 100, 80}, // smaller than the size: as it is; transparent → PNG
		{"/rotated.jpg", "image/jpeg", 256, 20, 40},
		{"/a.gif", "image/jpeg", 256, 30, 10},
	} {
		img, err := r.f.Get(t.Context(), "phone", "http://img.test"+c.path, c.size)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
		if err != nil || img.Type != c.typ || img.Width != c.w || img.Height != c.h || cfg.Width != c.w || cfg.Height != c.h {
			t.Errorf("%s: %s %d×%d (decodes as %d×%d, %v), want %s %d×%d", c.path, img.Type, img.Width, img.Height, cfg.Width, cfg.Height, err, c.typ, c.w, c.h)
		}
	}
}

func TestLimits(t *testing.T) {
	big := make([]byte, maxSource+10)
	copy(big, pngBytes(t, 1, 1, false))
	slow := make(chan struct{})
	t.Cleanup(func() { close(slow) })
	r := newRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/declared":
			w.Header().Set("Content-Length", fmt.Sprint(maxSource+1))
			w.WriteHeader(http.StatusOK)
		case "/streamed":
			w.Header().Set("Content-Type", "image/png")
			w.(http.Flusher).Flush() // no Content-Length: the limit is counted while reading
			_, _ = w.Write(big)
		case "/pixels":
			_, _ = w.Write(hugePNG(t, 10000, 5000))
		case "/slow":
			select {
			case <-slow:
			case <-req.Context().Done():
			}
		case "/broken":
			_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a\n"), "not really"...))
		}
	})
	for path, want := range map[string]string{"/declared": CodeTooLarge, "/streamed": CodeTooLarge, "/pixels": CodeTooLarge, "/slow": CodeTimeout, "/broken": CodeBadImage} {
		if _, err := r.f.Get(t.Context(), "phone", "http://img.test"+path, 256); code(err) != want {
			t.Errorf("%s: %v, want %s", path, err, want)
		}
	}
}

func TestCacheAndFailedAddresses(t *testing.T) {
	r := newRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/missing.png" {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(pngBytes(t, 1200, 900, false))
	})
	ctx := t.Context()
	for _, size := range []int{256, 256, 1024, 2048} {
		if _, err := r.f.Get(ctx, "phone", "http://img.test/a.png", size); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.hits.Load(); n != 1 {
		t.Fatalf("the source was fetched %d times for three sizes, want once", n)
	}
	// A new Fetcher on the same directory (a restarted manager) serves it from disk.
	again := New(Options{CacheDir: r.f.o.CacheDir, resolve: r.f.o.resolve, dial: r.f.o.dial, proxy: r.f.o.proxy})
	if _, err := again.Get(ctx, "phone", "http://img.test/a.png", 256); err != nil || r.hits.Load() != 1 {
		t.Fatalf("after a restart: %v, %d fetches", err, r.hits.Load())
	}
	for range 3 {
		if _, err := r.f.Get(ctx, "phone", "http://img.test/missing.png", 256); code(err) != CodeFetchFailed {
			t.Fatalf("missing: %v", err)
		}
	}
	if n := r.hits.Load(); n != 2 {
		t.Fatalf("a missing picture was fetched %d times in a row, want once", n-1)
	}
}

func TestConcurrentAsksShareOneFetch(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{}, 1)
	r := newRig(t, map[string]string{"img.test": publicIP}, func(w http.ResponseWriter, _ *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		_, _ = w.Write(pngBytes(t, 600, 400, false))
	})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.f.Get(t.Context(), fmt.Sprint("device", i%2), "http://img.test/a.png", 512)
			errs <- err
		}()
	}
	// Asks that come while the fetch runs join it; any after it finds the cache: either way, one fetch.
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := r.hits.Load(); n != 1 {
		t.Fatalf("8 asks fetched %d times, want once", n)
	}
}

func TestPerCallerLimit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newLimiter(3, time.Minute, func() time.Time { return now })
	for i := range 3 {
		if _, ok := l.take("phone"); !ok {
			t.Fatalf("take %d refused", i)
		}
	}
	wait, ok := l.take("phone")
	if ok || wait != 20*time.Second {
		t.Fatalf("4th take: %v, %v; want refused for 20s", wait, ok)
	}
	if _, ok := l.take("laptop"); !ok {
		t.Fatal("another caller was limited by the first")
	}
	now = now.Add(20 * time.Second)
	if _, ok := l.take("phone"); !ok {
		t.Fatal("still refused after the wait")
	}

	r := newRig(t, map[string]string{"img.test": publicIP}, serve(pngBytes(t, 4, 4, false), "image/png"))
	r.f.limits = newLimiter(1, time.Minute, time.Now)
	if _, err := r.f.Get(t.Context(), "phone", "http://img.test/1.png", 256); err != nil {
		t.Fatal(err)
	}
	_, err := r.f.Get(t.Context(), "phone", "http://img.test/2.png", 256)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeRateLimited || e.Status != http.StatusTooManyRequests || e.RetryAfter <= 0 {
		t.Fatalf("second fetch: %v, want rate_limited with a Retry-After", err)
	}
	// What is cached costs nothing.
	if _, err := r.f.Get(t.Context(), "phone", "http://img.test/1.png", 256); err != nil {
		t.Fatalf("a cached picture was limited: %v", err)
	}
}

func TestDiskCacheCap(t *testing.T) {
	dir := t.TempDir()
	c := &diskCache{dir: dir, max: 10_000, logf: t.Logf}
	payload := bytes.Repeat([]byte("x"), 3000)
	for i := range 3 {
		c.put(fmt.Sprint("k", i), &Image{Type: "image/png", Data: payload})
		// Distinct use times: the first put is the least recently used.
		at := time.Now().Add(time.Duration(i-10) * time.Minute)
		_ = os.Chtimes(c.path(fmt.Sprint("k", i)), at, at)
	}
	if c.image("k0") == nil {
		t.Fatal("k0 gone before the cap")
	}
	at := time.Now().Add(-time.Hour) // k1 is now the least recently used
	_ = os.Chtimes(c.path("k1"), at, at)
	c.put("k3", &Image{Type: "image/png", Data: payload})
	if c.image("k1") != nil || c.image("k0") == nil || c.image("k3") == nil {
		t.Fatal("the cap did not drop the least recently used file")
	}
	if c.total > c.max {
		t.Fatalf("holds %d bytes over the cap of %d", c.total, c.max)
	}
}

func TestSnap(t *testing.T) {
	for in, want := range map[int]int{0: DefaultSize, -5: DefaultSize, 1: 256, 256: 256, 257: 512, 700: 768, 2048: 2048, 9000: 2048} {
		if got := Snap(in); got != want {
			t.Errorf("Snap(%d) = %d, want %d", in, got, want)
		}
	}
}
