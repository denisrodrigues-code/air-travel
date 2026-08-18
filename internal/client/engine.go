package client

import "fmt"

// Engine descreve a identidade HTTP de um motor de navegador: User-Agent,
// presença de client hints, cabeçalhos característicos e a ordem canônica.
//
// Existe porque o perfil TLS não basta. O tls-client reproduz o ClientHello,
// mas o User-Agent e os client hints são montados à mão — e uma combinação
// incoerente é detectável ainda que o JA4 esteja perfeito.
//
// Medido contra o WAF de booking.flytap.com em
// /bfm/rest/booking/availability/search, SEM cookie (2026-08-07, os 16 perfis do
// registro, duas passadas com controle limpo nas duas pontas):
//
//	firefox_135 · firefox_147 · firefox_148  · gecko  -> 200
//	os 13 restantes                                   -> 403
//
// O Chromium falha por ter muito mais superfície para divergir: anuncia
// sec-ch-ua, sec-ch-ua-mobile, sec-ch-ua-platform e priority. Gecko não anuncia
// client hint nenhum, então há menos o que errar.
//
// **O veredito muda com o tempo, e este comentário já esteve errado duas vezes.**
// Em 06/08 passavam firefox_135 e os dois Safari, e os Firefox 147/148 não; em
// 07/08 é exatamente o oposto nas duas pontas — a família Gecko passa inteira e
// os três WebKit falham. Daí a conclusão de 06/08 ("é por perfil, não por
// família") não se sustentar como regra: hoje o corte é limpo por família.
//
// O que se sustentou nas três medições: **nenhum Chromium passa sem cookie**, e
// **um cf_clearance coerente o destrava** — com jar de um Chrome 151 real, os
// perfis chrome_* trazem voos. O cookie não é necessário para quem já passa, e é
// suficiente para quem não passava.
//
// Ver CLAUDE.md §4 e MEDICOES-PERFIS.md.
type Engine struct {
	// Name identifica o motor: chromium, gecko ou webkit.
	Name string
	// UserAgent coerente com o perfil TLS.
	UserAgent string
	// ClientHints indica se o motor anuncia os cabeçalhos sec-ch-ua-*.
	ClientHints bool
	// SecFetch indica se o motor envia os cabeçalhos sec-fetch-*.
	SecFetch bool
	// SecCHUA e SecCHUAPlatform só são usados quando ClientHints é verdadeiro.
	SecCHUA         string
	SecCHUAPlatform string
	// Extra são cabeçalhos característicos do motor (ex.: te em Gecko,
	// sec-gpc em Brave).
	Extra map[string]string
	// BlocksTrackers indica que o navegador bloqueia scripts de rastreamento, e
	// portanto NÃO envia os cabeçalhos de RUM que a SPA anexaria.
	//
	// Medido no Brave em 2026-08-06: o capture não traz x-dtreferer, x-dtpc,
	// traceparent nem tracestate em nenhuma rota — ele bloqueia o script do
	// Dynatrace como rastreador. Enviá-los sob identidade Brave seria anunciar
	// telemetria que o navegador real nunca emite.
	BlocksTrackers bool
	// AcceptLang sobrepõe o accept-language da configuração quando o navegador
	// reduz a entropia desse cabeçalho. Vazio usa o valor de config.
	//
	// O Brave manda `pt-BR,pt;q=0.7` onde o Chrome manda
	// `pt-PT,pt;q=0.9,en-US;q=0.8,en;q=0.7`.
	AcceptLang string
	// Order é a ordem canônica dos cabeçalhos. Nomes ausentes na requisição são
	// descartados na projeção — nunca se anuncia um cabeçalho não enviado.
	Order []string
}

// Chromium reproduz o Chrome 151.
var Chromium = Engine{
	Name: "chromium",
	UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
	ClientHints:     true,
	SecFetch:        true,
	SecCHUA:         `"Not=A?Brand";v="99", "Google Chrome";v="151", "Chromium";v="151"`,
	SecCHUAPlatform: `"Windows"`,
	Order: []string{
		"content-length",
		"x-dtreferer",
		"sec-ch-ua-platform",
		"authorization",
		"x-dtpc",
		"sec-ch-ua",
		"sec-ch-ua-mobile",
		"traceparent",
		"user-agent",
		"accept",
		"content-type",
		"tracestate",
		"origin",
		"sec-fetch-site",
		"sec-fetch-mode",
		"sec-fetch-dest",
		"referer",
		"accept-encoding",
		"accept-language",
		"cookie",
		"priority",
	},
}

// Gecko reproduz o Firefox 148. É a família que atravessa o WAF.
var Gecko = Engine{
	Name:      "gecko",
	UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0",
	SecFetch:  true,
	Extra:     map[string]string{"te": "trailers"},
	Order: []string{
		"content-length",
		"user-agent",
		"accept",
		"accept-language",
		"accept-encoding",
		"referer",
		"x-dtreferer",
		"x-dtpc",
		"traceparent",
		"tracestate",
		"content-type",
		"authorization",
		"origin",
		"sec-fetch-dest",
		"sec-fetch-mode",
		"sec-fetch-site",
		"cookie",
		"te",
	},
}

// WebKit reproduz o Safari, que não envia client hints nem sec-fetch-*.
var WebKit = Engine{
	Name: "webkit",
	UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 " +
		"(KHTML, like Gecko) Version/18.0 Safari/605.1.15",
	Order: []string{
		"content-length",
		"accept",
		"content-type",
		"authorization",
		"x-dtreferer",
		"x-dtpc",
		"traceparent",
		"tracestate",
		"origin",
		"user-agent",
		"referer",
		"accept-language",
		"accept-encoding",
		"cookie",
	},
}

// webKitIOS monta a identidade de um Safari de iPhone.
//
// Existe como construtor porque o único eixo que varia entre versões do Safari
// móvel é o par (versão do Safari, versão do iOS) — o resto do WebKit é o mesmo,
// e uma cópia por versão convidaria a divergir num campo só.
//
// Note que os perfis iOS pedem UA de iPhone: o WebKit acima anuncia macOS, o que
// é coerente com um perfil de Safari desktop e não com um de iOS.
func webKitIOS(safariVersion, osVersion string) Engine {
	return Engine{
		Name: "webkit",
		UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS " + osVersion + " like Mac OS X) " +
			"AppleWebKit/605.1.15 (KHTML, like Gecko) Version/" + safariVersion +
			" Mobile/15E148 Safari/604.1",
		Order: WebKit.Order,
	}
}

// chromiumBrand monta a identidade de um Chromium de marca (Brave, Opera), que
// difere do Chrome em duas coisas: o sufixo do User-Agent e a lista de marcas do
// sec-ch-ua.
//
// A versão do Chromium é parâmetro separado da versão da marca porque as duas
// divergem — o Opera 91 é Chromium 105 — e é o número do Chromium que tem de
// bater com o ClientHello. Anunciar Chrome/151 sobre um ClientHello de Chromium
// 105 é exatamente a incoerência que este arquivo existe para impedir.
//
// uaSuffix vazio produz a identidade do Chrome puro no User-Agent. Hoje nenhum
// motor registrado usa essa forma — ela existia para o Brave, cuja defesa
// antifingerprint é justamente parecer Chrome: o UA dele não tem token "Brave", só
// o sec-ch-ua o revela (capture de 2026-08-06). O caso continua suportado porque é
// uma variação real de navegador, não um detalhe do Brave.
//
// **A marca GREASE vem PRIMEIRO.** Medido nos dois navegadores reais:
//
//	Chrome  "Not=A?Brand";v="99", "Google Chrome";v="151", "Chromium";v="151"
//	Brave   "Not=A?Brand";v="99", "Brave";v="151", "Chromium";v="151"
//
// A primeira versão desta função a punha no fim, por palpite. Como o `Chromium`
// logo acima é escrito à mão e já batia com o capture, a divergência ficava só nos
// motores de marca — que era justamente onde não havia medição.
func chromiumBrand(brand, brandVersion, chromiumVersion, uaSuffix string) Engine {
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/" + chromiumVersion + ".0.0.0 Safari/537.36"
	if uaSuffix != "" {
		ua += " " + uaSuffix
	}

	hints := `"Not=A?Brand";v="99"`
	if brand != "" {
		hints += `, "` + brand + `";v="` + brandVersion + `"`
	}
	hints += `, "Chromium";v="` + chromiumVersion + `"`

	return Engine{
		Name:            "chromium",
		UserAgent:       ua,
		ClientHints:     true,
		SecFetch:        true,
		SecCHUA:         hints,
		SecCHUAPlatform: `"Windows"`,
		Order:           Chromium.Order,
	}
}

// WebKitIOS26 e WebKitIOS18 acompanham os perfis Safari_IOS_26_0 e _18_0.
var (
	WebKitIOS26 = webKitIOS("26.0", "26_0")
	WebKitIOS18 = webKitIOS("18.0", "18_0")
)

// Não há motor de Brave aqui: o perfil `brave_151` foi removido do registro (ver
// client.go). O que a medição dele deixou, e continua valendo, é um resultado
// NEGATIVO útil: o `chrome_151` passou usando o jar do Brave, anunciando
// "Google Chrome" no sec-ch-ua e enviando os quatro cabeçalhos de telemetria que
// o Brave real não envia. Logo o WAF **não** confere marca nem presença de RUM
// contra o clearance — foi isso que encolheu o espaço de hipóteses do §4.
//
// Os campos BlocksTrackers e AcceptLang do Engine nasceram desse capture e hoje
// não têm nenhum motor que os defina. Ficam porque descrevem uma variação real de
// navegador e o transporte já os respeita; o próximo motor de marca vai precisar
// deles.

// OperaChromium105 e OperaChromium104 acompanham Opera_91 e Opera_90.
//
// ATENÇÃO, valores NÃO medidos: o mapeamento Opera 91 → Chromium 105 e Opera 90
// → Chromium 104 vem da correspondência de versões do projeto Opera, e os
// números de build (`OPR/91.0.4516.20`) não foram conferidos contra captura.
// Antes de confiar nestes dois motores numa rota que discrimina, capture o
// tráfego do Opera real e confira — é a lição do §4 do CLAUDE.md, onde uma
// suposição de identidade não medida custou horas.
//
// Sabe-se, isso sim, que o ClientHello destes perfis é o do Chromium de 2022:
// medido igual ao de Chrome_103 e Chrome_105, com ALPS no codepoint antigo
// 0x4469 (ver TestOperaProfilesAreOneClientHello).
var (
	OperaChromium105 = chromiumBrand("Opera", "91", "105", "OPR/91.0.4516.20")
	OperaChromium104 = chromiumBrand("Opera", "90", "104", "OPR/90.0.4480.54")
)

// engineByProfile amarra cada perfil TLS ao seu motor. A coerência entre os dois
// é o ponto: um ClientHello do Firefox com User-Agent do Chrome é justamente a
// incoerência que o WAF detecta.
var engineByProfile = map[string]Engine{
	"chrome_131":      Chromium,
	"chrome_133":      Chromium,
	"chrome_133_psk":  Chromium,
	"chrome_144":      Chromium,
	"chrome_144_psk":  Chromium,
	"chrome_146":      Chromium,
	"chrome_146_psk":  Chromium,
	"chrome_151":      Chromium,
	"firefox_148":     Gecko,
	"firefox_147":     Gecko,
	"firefox_135":     Gecko,
	"safari_ios_18_5": WebKit,

	"safari_ios_26_0": WebKitIOS26,
	"safari_ios_18_0": WebKitIOS18,

	"opera_91": OperaChromium105,
	"opera_90": OperaChromium104,
}

// EngineFor devolve o motor coerente com o perfil TLS informado.
func EngineFor(profileName string) (Engine, error) {
	e, ok := engineByProfile[profileName]
	if !ok {
		return Engine{}, fmt.Errorf("perfil %q sem motor associado", profileName)
	}
	return e, nil
}

// ProjectOrder filtra a ordem canônica, mantendo apenas os cabeçalhos presentes.
//
// present informa se um cabeçalho foi definido. content-length e cookie são
// sempre mantidos: o transporte os define, mas a posição relativa deles compõe a
// impressão digital.
func (e Engine) ProjectOrder(present func(name string) bool) []string {
	order := make([]string, 0, len(e.Order))
	for _, name := range e.Order {
		switch name {
		case "content-length", "cookie":
			order = append(order, name)
		default:
			if present(name) {
				order = append(order, name)
			}
		}
	}
	return order
}
