package client

import "fmt"

// Engine descreve a identidade HTTP de um motor de navegador: User-Agent,
// presença de client hints, cabeçalhos característicos e a ordem canônica.
//
// Existe porque o perfil TLS não basta. O tls-client reproduz o ClientHello,
// mas o User-Agent e os client hints são montados à mão — e uma combinação
// incoerente é detectável ainda que o JA4 esteja perfeito.
//
// Foi o que se mediu contra o WAF de booking.flytap.com em
// /bfm/rest/booking/availability/search:
//
//	firefox_148 / _147 / _135 · gecko    -> 200
//	safari_ios_18_5           · webkit   -> 200
//	chrome_151 / _146         · chromium -> 403
//
// O Chromium falha por ter muito mais superfície para divergir: anuncia
// sec-ch-ua, sec-ch-ua-mobile, sec-ch-ua-platform e priority. Gecko não anuncia
// client hint nenhum, então há menos o que errar. Ver CLAUDE.md §4.
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
	// Extra são cabeçalhos característicos do motor (ex.: te em Gecko).
	Extra map[string]string
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

// engineByProfile amarra cada perfil TLS ao seu motor. A coerência entre os dois
// é o ponto: um ClientHello do Firefox com User-Agent do Chrome é justamente a
// incoerência que o WAF detecta.
var engineByProfile = map[string]Engine{
	"chrome_131":      Gecko,
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
