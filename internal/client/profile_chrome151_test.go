package client

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/bogdanfinn/fhttp/http2"
	tls_client "github.com/bogdanfinn/tls-client/profiles"
	tls "github.com/bogdanfinn/utls"
)

// trustAnchors é a extensão que poluía o JA4 do perfil chrome_146
// (TLSEXT_TYPE_trust_anchors, 0xca34) e que o Chrome real não anuncia nesta
// rota. Ver CLAUDE.md §4: a culpa foi atribuída por engano a
// WithRandomTLSExtensionOrder antes de se descobrir que era inerente ao perfil.
const trustAnchors uint16 = 0xca34

// wireBytes serializa uma extensão. Os dois primeiros bytes são o identificador
// da extensão, o que permite comparar identidade e conteúdo sem depender de
// nenhum acessor que o utls não expõe.
func wireBytes(t *testing.T, e tls.TLSExtension) []byte {
	t.Helper()

	b := make([]byte, e.Len())
	n, err := e.Read(b)
	if err != nil && err.Error() != "EOF" {
		t.Fatalf("failed to read extension %T: %v", e, err)
	}
	return b[:n]
}

// isRandom identifica as extensões cujo conteúdo muda a cada derivação — o
// payload GREASE do ECH é aleatório por construção, então só o seu tipo pode ser
// comparado.
func isRandom(e tls.TLSExtension) bool {
	_, ok := e.(*tls.GREASEEncryptedClientHelloExtension)
	return ok
}

func sigAlgs(t *testing.T, spec tls.ClientHelloSpec) []tls.SignatureScheme {
	t.Helper()

	for _, e := range spec.Extensions {
		if ext, ok := e.(*tls.SignatureAlgorithmsExtension); ok {
			return ext.SupportedSignatureAlgorithms
		}
	}
	t.Fatal("spec sem extensão signature_algorithms")
	return nil
}

func specs(t *testing.T) (base, mine tls.ClientHelloSpec) {
	t.Helper()

	base, err := tls_client.Chrome_144.GetClientHelloSpec()
	if err != nil {
		t.Fatalf("failed to derive Chrome_144 spec: %v", err)
	}
	mine, err = chrome151Spec()
	if err != nil {
		t.Fatalf("failed to derive chrome151 spec: %v", err)
	}
	return base, mine
}

// TestChrome151DiffersFromChrome144OnlyInSignatureAlgorithms encoda diretamente
// a descoberta documentada em CLAUDE.md §4: o perfil custom É o Chrome_144 mais
// três algoritmos de assinatura pós-quânticos, e nada mais.
//
// O hash JA4 só a rede confirma. Mas o CONTEÚDO do ClientHello é verificável
// aqui, byte a byte: se alguém "atualizar" o perfil e mudar cifradores, ordem de
// extensões ou acrescentar trust_anchors, este teste aponta exatamente o quê.
func TestChrome151DiffersFromChrome144OnlyInSignatureAlgorithms(t *testing.T) {
	base, mine := specs(t)

	if got, want := fmt.Sprint(mine.CipherSuites), fmt.Sprint(base.CipherSuites); got != want {
		t.Errorf("cifradores divergem do Chrome_144 — o 2º componente do JA4 deixa de bater\ntemos: %s\nbase:  %s", got, want)
	}
	if got, want := fmt.Sprint(mine.CompressionMethods), fmt.Sprint(base.CompressionMethods); got != want {
		t.Errorf("métodos de compressão = %s, esperado %s", got, want)
	}
	if len(mine.Extensions) != len(base.Extensions) {
		t.Fatalf("temos %d extensões, o Chrome_144 tem %d — a contagem entra no JA4 (t13d1516h2)",
			len(mine.Extensions), len(base.Extensions))
	}

	var diverged []int
	for i := range base.Extensions {
		if got, want := fmt.Sprintf("%T", mine.Extensions[i]), fmt.Sprintf("%T", base.Extensions[i]); got != want {
			t.Errorf("extensão %d = %s, esperado %s — a ORDEM das extensões define o JA3", i, got, want)
			continue
		}
		if isRandom(base.Extensions[i]) {
			continue
		}
		if string(wireBytes(t, mine.Extensions[i])) != string(wireBytes(t, base.Extensions[i])) {
			diverged = append(diverged, i)
		}
	}

	if len(diverged) != 1 {
		t.Fatalf("extensões divergentes: %v — esperada exatamente uma (signature_algorithms)", diverged)
	}
	if _, ok := mine.Extensions[diverged[0]].(*tls.SignatureAlgorithmsExtension); !ok {
		t.Errorf("a divergência está em %T (índice %d), e deveria estar só em signature_algorithms",
			mine.Extensions[diverged[0]], diverged[0])
	}
}

// TestChrome151SignatureAlgorithmsArePostQuantumFirst fixa os três ML-DSA e a
// posição deles: o JA4 lista os algoritmos na ordem enviada, então 0904,0905,0906
// precisam vir ANTES dos oito clássicos.
func TestChrome151SignatureAlgorithmsArePostQuantumFirst(t *testing.T) {
	base, mine := specs(t)

	classic := sigAlgs(t, base)
	got := sigAlgs(t, mine)

	want := append([]tls.SignatureScheme{mlDSA44, mlDSA65, mlDSA87}, classic...)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("signature_algorithms:\ntemos:    %#v\nesperado: %#v", got, want)
	}
	if mlDSA44 != 0x0904 || mlDSA65 != 0x0905 || mlDSA87 != 0x0906 {
		t.Errorf("os identificadores ML-DSA foram alterados: %#x %#x %#x", mlDSA44, mlDSA65, mlDSA87)
	}
}

// TestChrome151HasNoTrustAnchors é a regressão do trust_anchors: ver CLAUDE.md §4.
func TestChrome151HasNoTrustAnchors(t *testing.T) {
	_, mine := specs(t)

	for i, e := range mine.Extensions {
		if isRandom(e) {
			continue // payload aleatório daria falso positivo
		}
		b := wireBytes(t, e)
		if len(b) < 2 {
			continue // a SNI sem servidor definido não serializa
		}
		if id := binary.BigEndian.Uint16(b); id == trustAnchors {
			t.Errorf("extensão %d é trust_anchors (%#x) — ela altera o JA4 e o Chrome real não a envia nesta rota", i, id)
		}
	}
}

// TestChrome151HTTP2FingerprintMatchesChrome144 protege os números medidos no
// tráfego real: SETTINGS, sua ordem, o WINDOW_UPDATE inicial e a ordem dos
// pseudo-cabeçalhos. São o fingerprint HTTP/2, independente do TLS.
func TestChrome151HTTP2FingerprintMatchesChrome144(t *testing.T) {
	if got, want := chrome151.GetSettings(), tls_client.Chrome_144.GetSettings(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("SETTINGS = %v, esperado %v", got, want)
	}
	if got, want := chrome151.GetSettingsOrder(), tls_client.Chrome_144.GetSettingsOrder(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ordem dos SETTINGS = %v, esperado %v", got, want)
	}
	if got := chrome151.GetConnectionFlow(); got != 15663105 {
		t.Errorf("connectionFlow = %d, esperado 15663105 (o WINDOW_UPDATE inicial do Chrome)", got)
	}

	wantPseudo := "[:method :authority :scheme :path]"
	if got := fmt.Sprint(chrome151.GetPseudoHeaderOrder()); got != wantPseudo {
		t.Errorf("ordem dos pseudo-cabeçalhos = %s, esperado %s", got, wantPseudo)
	}

	// O valor explícito importa: o Chrome envia ENABLE_PUSH = 0, e um SETTINGS
	// ausente não é a mesma coisa que um SETTINGS com zero.
	if v, ok := chrome151.GetSettings()[http2.SettingEnablePush]; !ok || v != 0 {
		t.Errorf("SETTINGS_ENABLE_PUSH = %d (presente: %v), esperado 0 explícito", v, ok)
	}
}

// TestChrome151IsWhatTheRegistryServes: o perfil só vale se for o que o
// construtor entrega.
func TestChrome151IsWhatTheRegistryServes(t *testing.T) {
	got, ok := profileRegistry["chrome_151"]
	if !ok {
		t.Fatal("chrome_151 não está no registro")
	}
	if got.GetClientHelloId().Version != "151" || got.GetClientHelloId().Client != "Chrome" {
		t.Errorf("ClientHelloID = %+v", got.GetClientHelloId())
	}
	if got.GetClientHelloId().RandomExtensionOrder {
		t.Error("RandomExtensionOrder ligado: embaralhar a ordem destrói o JA3 que o perfil existe para reproduzir")
	}
}
