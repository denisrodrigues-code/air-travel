package client

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bogdanfinn/tls-client/profiles"
	tls "github.com/bogdanfinn/utls"

	"airtravel/internal/config"
)

// Este arquivo fixa as relações MEDIDAS entre os perfis Safari e Opera
// acrescentados ao registro. Todas foram obtidas offline, derivando os specs; o
// hash JA4 continua só verificável na rede (cmd/tlsprobe).
//
// A razão de existirem: a biblioteca pode mudar um perfil numa atualização, e as
// decisões tomadas em client.go e engine.go dependem destas relações. Um
// Safari_IOS_26_0 que virasse cópia do 18.5, ou um Opera cuja derivação
// deixasse de funcionar, passariam sem ninguém notar.

// fingerprintOf resume um spec no que distingue um ClientHello de outro: os
// cifradores e a sequência de extensões, na ordem, **com o conteúdo de cada uma**.
//
// O conteúdo é essencial, e isto foi aprendido errando: a primeira versão
// comparava só os TIPOS das extensões e por isso declarava `Safari_IOS_18_0`
// idêntico ao `Safari_IOS_18_5`. Não são — diferem em `signature_algorithms`, e o
// WAF da TAP separou os dois em 2026-08-06 (um passou, o outro tomou 403). Uma
// comparação que não vê a diferença que o servidor vê não serve para nada.
//
// As extensões de conteúdo aleatório entram apenas pelo tipo: a chave pública do
// key_share e o payload GREASE do ECH mudam a cada derivação por construção.
func fingerprintOfProfile(t *testing.T, p profiles.ClientProfile, label string) string {
	t.Helper()

	spec, err := specFor(p)
	if err != nil {
		t.Fatalf("failed to derive spec de %q: %v", label, err)
	}

	parts := []string{fmt.Sprint(spec.CipherSuites)}
	for _, e := range spec.Extensions {
		name := strings.TrimPrefix(fmt.Sprintf("%T", e), "*tls.")
		switch e.(type) {
		case *tls.KeyShareExtension, *tls.GREASEEncryptedClientHelloExtension, *tls.UtlsGREASEExtension:
			parts = append(parts, name)
			continue
		}
		b := make([]byte, e.Len())
		n, _ := e.Read(b)
		parts = append(parts, fmt.Sprintf("%s:%x", name, b[:n]))
	}
	return strings.Join(parts, "|")
}

func fingerprintOf(t *testing.T, name string) string {
	t.Helper()

	p, ok := profileRegistry[name]
	if !ok {
		t.Fatalf("perfil %q não está no registro", name)
	}
	return fingerprintOfProfile(t, p, name)
}

// TestSpecForResolvesEveryRegisteredProfile é a rede de segurança do registro:
// todo perfil registrado tem de ter spec derivável por algum dos dois caminhos.
//
// Sem specFor este teste falharia em opera_90 e opera_91, cujo
// GetClientHelloSpec() devolve "please implement this method" — eles são
// declarados com tls.EmptyClientHelloSpecFactory e só o utls resolve, por tabela
// interna. É a armadilha que motivou o helper.
func TestSpecForResolvesEveryRegisteredProfile(t *testing.T) {
	for name, p := range profileRegistry {
		spec, err := specFor(p)
		if err != nil {
			t.Errorf("perfil %q sem spec derivável: %v", name, err)
			continue
		}
		if len(spec.CipherSuites) == 0 || len(spec.Extensions) == 0 {
			t.Errorf("perfil %q derivou spec vazio (ciphers=%d exts=%d)",
				name, len(spec.CipherSuites), len(spec.Extensions))
		}
	}
}

// TestLegacyProfilesNeedTheFallback prova que o fallback do specFor não é
// defensivo por precaução — ele é a ÚNICA via para o Opera.
//
// Se uma atualização da biblioteca passar a dar SpecFactory ao Opera, este teste
// falha e avisa que o helper deixou de ser necessário para ele. É informação, não
// defeito: prefere-se saber a manter um fallback por superstição.
func TestLegacyProfilesNeedTheFallback(t *testing.T) {
	for _, name := range []string{"opera_91", "opera_90"} {
		if _, err := profileRegistry[name].GetClientHelloSpec(); err == nil {
			t.Errorf("perfil %q agora deriva spec direto — o fallback de specFor "+
				"pode ter deixado de ser necessário para ele", name)
		}
	}

	// E o caminho direto continua valendo para os perfis modernos, senão o
	// fallback estaria mascarando um erro real.
	if _, err := profileRegistry["chrome_151"].GetClientHelloSpec(); err != nil {
		t.Errorf("chrome_151 deveria derivar spec direto: %v", err)
	}
}

// TestSafariProfilesMeasuredRelations fixa a relação real entre os perfis Safari,
// e ela é contraintuitiva: **o nome `Safari_IOS_18_0` não descreve o
// ClientHello dele.**
//
// Medido em 2026-08-06 comparando conteúdo de extensão: o `Safari_IOS_18_0` é
// idêntico ao `Safari_16_0` e ao `Safari_15_6_1` — o fingerprint do Safari de
// 2022 — e NÃO ao `Safari_IOS_18_5`, do qual difere em `signature_algorithms`
// (o 18_0 anuncia `0203`, ECDSA-SHA1, que o 18_5 não anuncia).
//
// A consequência é concreta e foi medida na rede no mesmo dia: `safari_ios_18_0`
// tomou 403 na rota `search` enquanto `safari_ios_18_5` e `safari_ios_26_0`
// trouxeram 34 voos, com o mesmo jar. O motor `WebKitIOS18` anuncia Safari 18.0
// sobre um ClientHello de Safari 16 — a incoerência que o engine.go existe para
// impedir, introduzida por confiar no nome do perfil da biblioteca.
//
// Ver MEDICOES-PERFIS.md.
func TestSafariProfilesMeasuredRelations(t *testing.T) {
	ios185 := fingerprintOf(t, "safari_ios_18_5")
	ios180 := fingerprintOf(t, "safari_ios_18_0")
	ios260 := fingerprintOf(t, "safari_ios_26_0")

	if ios180 == ios185 {
		t.Error("safari_ios_18_0 virou idêntico ao 18_5 — a documentação afirma que " +
			"eles diferem em signature_algorithms, e é essa diferença que o WAF viu")
	}
	if ios260 == ios185 {
		t.Error("safari_ios_26_0 virou cópia do 18_5")
	}

	// O 18_0 é, de fato, o Safari de 2022. Se isto deixar de valer, a razão de
	// desconfiar dele muda e o registro precisa ser reavaliado.
	old := fingerprintOfProfile(t, profiles.Safari_16_0, "Safari_16_0")
	if ios180 != old {
		t.Errorf("safari_ios_18_0 deixou de ser idêntico ao Safari_16_0 — o motivo " +
			"documentado para tratá-lo como incoerente era exatamente esse")
	}
	if ios185 == old || ios260 == old {
		t.Error("safari_ios_18_5 ou o 26_0 virou igual ao Safari de 2022")
	}

	// O Safari 26 não envia padding — diferença estrutural, além do conteúdo.
	spec, err := specFor(profileRegistry["safari_ios_26_0"])
	if err != nil {
		t.Fatalf("failed to derive spec: %v", err)
	}
	for _, e := range spec.Extensions {
		if _, ok := e.(*tls.UtlsPaddingExtension); ok {
			t.Error("safari_ios_26_0 passou a enviar padding")
		}
	}
}

// TestGreaseBrandComesFirst fixa a ordem das marcas no sec-ch-ua, medida nos dois
// navegadores reais em 2026-08-06.
//
// A primeira versão de chromiumBrand punha a marca GREASE no fim, por palpite. O
// motor `Chromium`, escrito à mão a partir do capture do Chrome, já a punha no
// começo — então a divergência ficava só nos motores de marca, exatamente onde não
// havia medição.
func TestGreaseBrandComesFirst(t *testing.T) {
	for _, profile := range []string{"chrome_151", "opera_91", "opera_90"} {
		e, err := EngineFor(profile)
		if err != nil {
			t.Fatalf("perfil %q sem motor: %v", profile, err)
		}
		if !strings.HasPrefix(e.SecCHUA, `"Not=A?Brand";v="99"`) {
			t.Errorf("sec-ch-ua de %q não começa pela marca GREASE: %s", profile, e.SecCHUA)
		}
	}
}

// TestOperaProfilesAreOneClientHello fixa a advertência do registro: as duas
// versões de Opera são o MESMO ClientHello, e é o do Chromium de 2022.
//
// A consequência prática está no engine.go — o User-Agent tem de anunciar
// Chromium 104/105, não 151 — e no pool, onde contar opera_90 e opera_91 como
// duas identidades TLS seria falso.
func TestOperaProfilesAreOneClientHello(t *testing.T) {
	o91 := fingerprintOf(t, "opera_91")
	o90 := fingerprintOf(t, "opera_90")

	if o91 != o90 {
		t.Errorf("opera_91 e opera_90 deixaram de ser idênticos — os motores "+
			"associados assumem eras de Chromium diferentes\n91: %s\n90: %s", o91, o90)
	}

	if got := fingerprintOfProfile(t, profiles.Chrome_103, "Chrome_103"); got != o91 {
		t.Errorf("opera_91 deixou de coincidir com o Chrome_103 — a era de Chromium "+
			"que o User-Agent anuncia foi derivada dessa igualdade\nopera_91:   %s\nchrome_103: %s", o91, got)
	}

	// ALPS no codepoint ANTIGO (0x4469), como o Chrome_131 e diferente de todos
	// os Chromium modernos do registro. É o eixo da hipótese em aberto do §10 do
	// CLAUDE.md — registrado aqui porque, se alguém for medi-la, o Opera é um
	// perfil com 0x4469 que já está montado.
	spec, err := specFor(profileRegistry["opera_91"])
	if err != nil {
		t.Fatalf("failed to derive spec: %v", err)
	}
	var hasOldALPS bool
	for _, e := range spec.Extensions {
		if _, ok := e.(*tls.ApplicationSettingsExtension); ok {
			hasOldALPS = true
		}
	}
	if !hasOldALPS {
		t.Error("opera_91 deixou de trazer ALPS no codepoint antigo (0x4469)")
	}
}

// TestAddedProfilesHaveCoherentEngines complementa TestEngineCoherence, que só
// exige que exista motor: aqui se exige que o motor DIGA a verdade sobre o
// perfil.
//
// É a propriedade que o engine.go inteiro existe para sustentar (§4): um
// ClientHello de Chromium 105 com User-Agent de Chrome 151 é a incoerência que
// fez o WAF recusar. Um `Chrome/151` no motor do Opera passaria em todos os
// outros testes.
func TestAddedProfilesHaveCoherentEngines(t *testing.T) {
	cases := []struct {
		profile  string
		wantUA   []string
		absentUA []string
		engine   string
	}{
		{"safari_ios_26_0", []string{"iPhone", "Version/26.0"}, []string{"Macintosh"}, "webkit"},
		{"safari_ios_18_0", []string{"iPhone", "Version/18.0"}, []string{"Macintosh"}, "webkit"},
		{"opera_91", []string{"Chrome/105.0.0.0", "OPR/91"}, []string{"Chrome/151"}, "chromium"},
		{"opera_90", []string{"Chrome/104.0.0.0", "OPR/90"}, []string{"Chrome/151"}, "chromium"},
	}

	for _, c := range cases {
		e, err := EngineFor(c.profile)
		if err != nil {
			t.Errorf("perfil %q sem motor: %v", c.profile, err)
			continue
		}
		if e.Name != c.engine {
			t.Errorf("motor de %q: Name = %q, esperado %q", c.profile, e.Name, c.engine)
		}
		for _, want := range c.wantUA {
			if !strings.Contains(e.UserAgent, want) {
				t.Errorf("User-Agent de %q não contém %q: %s", c.profile, want, e.UserAgent)
			}
		}
		for _, absent := range c.absentUA {
			if strings.Contains(e.UserAgent, absent) {
				t.Errorf("User-Agent de %q contém %q, incoerente com o perfil: %s",
					c.profile, absent, e.UserAgent)
			}
		}
	}
}

// TestBrandedChromiumHintsMatchTheUserAgent fixa que a versão do Chromium
// anunciada no sec-ch-ua é a MESMA do User-Agent.
//
// Divergir aqui é fácil e silencioso: são duas strings montadas em pontos
// diferentes, e um WAF que compare as duas pega a diferença de graça.
func TestBrandedChromiumHintsMatchTheUserAgent(t *testing.T) {
	for _, profile := range []string{"opera_91", "opera_90"} {
		e, err := EngineFor(profile)
		if err != nil {
			t.Fatalf("perfil %q sem motor: %v", profile, err)
		}
		if !e.ClientHints {
			t.Errorf("motor de %q é Chromium e deveria anunciar client hints", profile)
			continue
		}

		// Extrai o número do Chromium do User-Agent e exige-o no sec-ch-ua.
		const marker = "Chrome/"
		i := strings.Index(e.UserAgent, marker)
		if i < 0 {
			t.Errorf("User-Agent de %q sem versão de Chromium: %s", profile, e.UserAgent)
			continue
		}
		version := e.UserAgent[i+len(marker):]
		version = version[:strings.Index(version, ".")]

		if want := `"Chromium";v="` + version + `"`; !strings.Contains(e.SecCHUA, want) {
			t.Errorf("sec-ch-ua de %q não declara %s, divergindo do User-Agent\nsec-ch-ua: %s\nUA: %s",
				profile, want, e.SecCHUA, e.UserAgent)
		}
	}
}

// TestProfileNamesCoversRegistry é o que impede a lista das ferramentas de medição
// de dessincronizar do registro outra vez.
//
// O cmd/wafprobe trazia seis nomes fixos enquanto o registro tinha dezoito, então
// `./run.sh wafprobe` media um terço dos perfis e calava sobre o resto — e nada
// reclamava. Agora as duas sondas derivam desta função.
func TestProfileNamesCoversRegistry(t *testing.T) {
	names := ProfileNames()

	if len(names) != len(profileRegistry) {
		t.Errorf("ProfileNames() devolveu %d nomes para %d perfis registrados",
			len(names), len(profileRegistry))
	}
	for _, name := range names {
		if _, ok := profileRegistry[name]; !ok {
			t.Errorf("ProfileNames() devolveu %q, que não está no registro", name)
		}
	}

	// Ordenada, para a saída das sondas ser comparável entre execuções.
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("ProfileNames() não está ordenada: %q antes de %q", names[i-1], names[i])
		}
	}
}

// blockedOnSearch são os perfis MEDIDOS tomando 403 em
// booking/availability/search sem cookie, em 2026-08-07, duas passadas cada, com
// controle limpo nas duas pontas.
//
// A lista é o complemento de PassingProfiles dentro do registro, e existe separada
// porque afirma uma coisa diferente: não "estes eu escolhi para a rotação", mas
// "estes foram medidos falhando, então incluí-los é gastar uma requisição e um 403
// para redescobrir o conhecido".
//
// **Os três WebKit entraram aqui em 07/08**, vindos da lista de aprovados de
// 06/08. Na mesma medição os dois Firefox saíram daqui. Se esta lista e a
// PassingProfiles parecerem trocadas em relação ao que o CLAUDE.md diz numa
// tabela antiga, é porque o veredito do WAF mudou — refaça a medição antes de
// "corrigir" qualquer uma das duas.
var blockedOnSearch = []string{
	"chrome_131", "chrome_133", "chrome_133_psk",
	"chrome_144", "chrome_144_psk", "chrome_146", "chrome_146_psk", "chrome_151",
	"opera_90", "opera_91",
	"safari_ios_18_0", "safari_ios_18_5", "safari_ios_26_0",
}

// TestPassingProfilesMatchTheMeasurement guarda `PassingProfiles` contra os dois
// erros que de fato aconteceram ao editá-la à mão em 2026-08-06.
//
// O primeiro derrubou a aplicação: `"Firefox_135"` com maiúscula não é chave do
// registro — é o nome da variável Go na biblioteca — e `NewPool` falha ao montar.
// Como `Rotate` é ligado por padrão, o `scraper` e a `api` não sobem:
//
//	failed to build fingerprint pool: perfil "Firefox_135" sem motor associado
//
// O segundo era silencioso e pior: `safari_ios_18_0` entrou na lista no lugar do
// `18_5`. Os nomes diferem em um caractere, e o `18_0` é justamente o que carrega o
// ClientHello do Safari 16 de 2022 e foi medido bloqueado. A rotação gastaria uma
// requisição e um 403 nele a cada volta, sem nada indicando o porquê.
//
// Em 07/08 o mesmo erro se repetiu numa reedição da lista — o `safari_ios_18_0`
// entrou de novo, agora acompanhado dos dois Firefox que HAVIAM voltado a passar.
// O teste separou as duas coisas: aceitou os Firefox depois da remedição e barrou
// o Safari, que continuava bloqueado. É a razão de a checagem comparar contra uma
// lista de medições e não contra uma contagem.
func TestPassingProfilesMatchTheMeasurement(t *testing.T) {
	blocked := make(map[string]bool, len(blockedOnSearch))
	for _, p := range blockedOnSearch {
		blocked[p] = true
	}

	for _, p := range PassingProfiles {
		// O erro que impede a aplicação de subir.
		if _, ok := profileRegistry[p]; !ok {
			t.Errorf("PassingProfiles contém %q, que NÃO está no registro — o pool "+
				"falha ao montar e a aplicação não sobe (as chaves são minúsculas)", p)
		}
		if _, err := EngineFor(p); err != nil {
			t.Errorf("PassingProfiles contém %q sem motor associado: %v", p, err)
		}
		// O erro silencioso.
		if blocked[p] {
			t.Errorf("PassingProfiles contém %q, medido BLOQUEADO na rota search — "+
				"a rotação gastaria uma requisição e um 403 nele a cada volta", p)
		}
	}

	// A lista de bloqueados também precisa nomear perfis reais, senão ela para de
	// proteger sem avisar.
	for _, p := range blockedOnSearch {
		if _, ok := profileRegistry[p]; !ok {
			t.Errorf("blockedOnSearch nomeia %q, que não está no registro", p)
		}
	}

	// Juntas, as duas listas têm de cobrir o registro inteiro: um perfil novo que
	// não esteja em nenhuma delas é um perfil nunca medido nesta rota.
	if got, want := len(PassingProfiles)+len(blockedOnSearch), len(profileRegistry); got != want {
		t.Errorf("as duas listas cobrem %d perfis, o registro tem %d — há perfil "+
			"sem medição na rota search; rode ./run.sh wafprobe -control \"\"", got, want)
	}
}

// TestDefaultProfileIsRegisteredAndPassing amarra o padrão da aplicação à medição.
//
// Vive aqui, e não em internal/config, porque este pacote importa config **e**
// conhece o registro — o inverso seria ciclo de importação. Foi a lacuna que
// permitiu `DefaultTLSProfile = "firefox_148"` continuar apontando para um perfil
// bloqueado depois de 06/08, com o teste de config verde porque fixava o literal.
func TestDefaultProfileIsRegisteredAndPassing(t *testing.T) {
	def := config.DefaultTLSProfile

	if _, ok := profileRegistry[def]; !ok {
		t.Fatalf("DefaultTLSProfile = %q não está no registro", def)
	}
	if !slices.Contains(PassingProfiles, def) {
		t.Errorf("DefaultTLSProfile = %q não consta de PassingProfiles — o padrão da "+
			"aplicação é um perfil que não foi medido atravessando o WAF", def)
	}
}
