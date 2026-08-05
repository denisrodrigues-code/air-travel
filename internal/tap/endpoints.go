// Os endpoints da API BFM e o conjunto de cabeçalhos de cada um.

package tap

import (
	"time"
)

const (
	// pathSessionCreate cria a sessão anônima e emite o primeiro JWT. Foi
	// identificado no bundle main.js da SPA (constante `x + "session/create"`);
	// o /bfm/rest/session/resetValues apenas reinicia uma sessão existente e
	// responde 400 sem um Bearer válido.
	pathSessionCreate   = "/bfm/rest/session/create"
	pathSessionReset    = "/bfm/rest/session/resetValues"
	pathAvailability    = "/bfm/rest/booking/availability/search"
	pathPaxTypes        = "/bfm/rest/search/pax/types"
	pathStopover        = "/bfm/rest/journey/stopover/search"
	pathCalendar        = "/bfm/rest/booking/availability/calendar/"
	pathCalendarReturns = "/bfm/rest/booking/availability/calendarReturns/"

	// tokenRefreshMargin antecipa a renovação do JWT antes do exp real.
	tokenRefreshMargin = 5 * time.Minute
)

// headerProfile descreve o conjunto e a ordem de cabeçalhos de um endpoint.
//
// O Chrome não envia o mesmo conjunto em todas as rotas: os cabeçalhos de RUM
// do Dynatrace (x-dtreferer/x-dtpc) aparecem nas chamadas disparadas a partir
// da página de resultados, mas não em /bfm/rest/session/resetValues. Enviá-los
// onde o navegador não envia altera a impressão digital.
type headerProfile struct {
	// dynatrace indica se x-dtreferer e x-dtpc fazem parte do conjunto.
	dynatrace bool
	// refererPath é o caminho usado em Referer.
	refererPath string
}

// headerProfiles mapeia cada endpoint ao perfil observado no tráfego real.
var headerProfiles = map[string]headerProfile{
	pathSessionCreate: {dynatrace: false, refererPath: "/booking"},
	pathSessionReset:  {dynatrace: false, refererPath: "/booking"},
	pathAvailability:  {dynatrace: true, refererPath: "/booking/flights"},
	pathPaxTypes:      {dynatrace: true, refererPath: "/booking/flights"},
	pathStopover:      {dynatrace: true, refererPath: "/booking/flights"},
	// O calendário é consultado a partir do formulário, não da lista de voos.
	pathCalendar: {dynatrace: true, refererPath: "/booking"},
	// calendarReturns é disparado pelo painel de datas de ida e volta, cujo
	// caminho é /booking/dates. Sem esta entrada a rota caía no padrão e
	// anunciava Referer /booking/flights — uma página que o usuário não teria
	// visitado ainda. O WAF não protege esta rota, mas divergir da captura é
	// justamente o que este mapa existe para evitar.
	pathCalendarReturns: {dynatrace: true, refererPath: "/booking/dates"},
}

// profileFor devolve o perfil do endpoint, com um padrão conservador.
func profileFor(path string) headerProfile {
	if p, ok := headerProfiles[path]; ok {
		return p
	}
	return headerProfile{dynatrace: true, refererPath: "/booking/flights"}
}
