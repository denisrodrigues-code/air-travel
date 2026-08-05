package client

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"

	"airtravel/internal/config"
)

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

// TestNewAcceptsEveryRegisteredProfile evita que um perfil entre no registro sem
// que o construtor saiba montá-lo.
func TestNewAcceptsEveryRegisteredProfile(t *testing.T) {
	for name := range profileRegistry {
		c, jar, err := New(Options{ProfileName: name, TimeoutSeconds: 5})
		if err != nil {
			t.Errorf("perfil %q: %v", name, err)
			continue
		}
		if c == nil || jar == nil {
			t.Errorf("perfil %q: cliente ou jar nil", name)
		}
	}
}

func TestNewRejectsUnknownProfile(t *testing.T) {
	if _, _, err := New(Options{ProfileName: "netscape_4"}); err == nil {
		t.Error("perfil desconhecido foi aceito")
	}
	if _, _, err := New(Options{}); err == nil {
		t.Error("perfil vazio foi aceito")
	}
}

func TestNewRejectsMalformedProxy(t *testing.T) {
	_, _, err := New(Options{ProfileName: "firefox_148", ProxyURL: "http://\x7f:bad"})
	if err == nil {
		t.Error("URL de proxy malformada foi aceita")
	}
}

// TestNewDefaultsTimeout: um timeout ausente não pode virar zero, que no
// net/http significa "esperar para sempre".
func TestNewDefaultsTimeout(t *testing.T) {
	if _, _, err := New(Options{ProfileName: "firefox_148", TimeoutSeconds: 0}); err != nil {
		t.Errorf("timeout zero deveria cair no padrão: %v", err)
	}
	if _, _, err := New(Options{ProfileName: "firefox_148", TimeoutSeconds: -1}); err != nil {
		t.Errorf("timeout negativo deveria cair no padrão: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SeedCookies
// ---------------------------------------------------------------------------

// TestSeedCookiesPreservesOrder protege a razão de os cookies serem fatia e não
// map: um cabeçalho Cookie que muda de ordem a cada execução é sinal de bot.
func TestSeedCookiesPreservesOrder(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to build jar: %v", err)
	}

	cookies := []config.Cookie{
		{Name: "_ga", Value: "1"},
		{Name: "cf_clearance", Value: "2"},
		{Name: "__cf_bm", Value: "3"},
		{Name: "httpECX", Value: "4"},
	}

	const base = "https://booking.flytap.com"
	if err := SeedCookies(jar, base, cookies); err != nil {
		t.Fatalf("SeedCookies: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, base+"/booking", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	got := jar.Cookies(req.URL)
	if len(got) != len(cookies) {
		t.Fatalf("jar devolveu %d cookies, esperado %d", len(got), len(cookies))
	}
	for i, want := range cookies {
		if got[i].Name != want.Name {
			t.Errorf("posição %d = %q, esperado %q", i, got[i].Name, want.Name)
		}
		if got[i].Value != want.Value {
			t.Errorf("%s = %q, esperado %q", want.Name, got[i].Value, want.Value)
		}
	}
}

// TestSeedCookiesEmptyIsNoop: rodar sem cookies é o caminho normal dos modos
// calendar e returns.
func TestSeedCookiesEmptyIsNoop(t *testing.T) {
	jar, _ := cookiejar.New(nil)

	if err := SeedCookies(jar, "https://booking.flytap.com", nil); err != nil {
		t.Errorf("lista vazia devolveu erro: %v", err)
	}
	if err := SeedCookies(jar, "url-invalida-mas-lista-vazia", nil); err != nil {
		t.Errorf("lista vazia deveria retornar antes de validar a URL: %v", err)
	}
}

func TestSeedCookiesRejectsBadBaseURL(t *testing.T) {
	jar, _ := cookiejar.New(nil)

	err := SeedCookies(jar, "http://\x7f", []config.Cookie{{Name: "a", Value: "b"}})
	if err == nil {
		t.Error("URL base malformada foi aceita")
	}
}

// ---------------------------------------------------------------------------
// DecompressBody
// ---------------------------------------------------------------------------

func gzipped(t *testing.T, payload string) io.ReadCloser {
	t.Helper()

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatalf("failed to gzip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("failed to close gzip: %v", err)
	}
	return io.NopCloser(&buf)
}

// TestDecompressBodyGzip cobre o caminho normal: o Accept-Encoding é definido à
// mão, então o fhttp não descomprime sozinho.
func TestDecompressBodyGzip(t *testing.T) {
	const payload = `{"status":"200"}`

	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
		Body:   gzipped(t, payload),
	}

	DecompressBody(resp)

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if string(got) != payload {
		t.Errorf("corpo = %q, esperado %q", got, payload)
	}
	if !resp.Uncompressed {
		t.Error("Uncompressed não foi marcado; uma segunda chamada corromperia o corpo")
	}
	if resp.ContentLength != -1 {
		t.Errorf("ContentLength = %d, esperado -1 após descomprimir", resp.ContentLength)
	}
}

// TestDecompressBodyIsIdempotent é a razão da guarda: descomprimir duas vezes
// corromperia o corpo.
func TestDecompressBodyIsIdempotent(t *testing.T) {
	const payload = `{"status":"200"}`

	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
		Body:   gzipped(t, payload),
	}

	DecompressBody(resp)
	DecompressBody(resp) // a segunda deve ser inócua

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if string(got) != payload {
		t.Errorf("corpo = %q após duas chamadas, esperado %q", got, payload)
	}
}

// TestDecompressBodyGuards cobre os casos em que não há nada a fazer.
func TestDecompressBodyGuards(t *testing.T) {
	// Não deve entrar em panic com nil.
	DecompressBody(nil)
	DecompressBody(&http.Response{})

	// Sem Content-Encoding, o corpo passa intacto.
	const plain = `{"status":"200"}`
	resp := &http.Response{
		Header: http.Header{},
		Body:   io.NopCloser(strings.NewReader(plain)),
	}
	DecompressBody(resp)

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if string(got) != plain {
		t.Errorf("corpo sem encoding = %q, esperado %q", got, plain)
	}
	if resp.Uncompressed {
		t.Error("Uncompressed foi marcado sem haver descompressão")
	}
}

// TestDecompressBodyUnknownEncoding: um encoding desconhecido não pode destruir
// o corpo — melhor entregá-lo intacto e deixar o parsing falhar com clareza.
func TestDecompressBodyUnknownEncoding(t *testing.T) {
	const payload = "conteudo-cru"

	resp := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"exotico"}},
		Body:   io.NopCloser(strings.NewReader(payload)),
	}
	DecompressBody(resp)

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if string(got) != payload {
		t.Errorf("corpo = %q, esperado intacto", got)
	}
}
