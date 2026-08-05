// Package collect é o caso de uso de coleta: obter dados do provedor e
// persistir nos dois destinos, com uma política única.
//
// Existe porque essa política estava implementada duas vezes — no orquestrador
// do CLI e nos handlers HTTP — e as duas cópias divergiram no tratamento de
// falha. Ver CLAUDE.md §9 e o comentário de Service.persist.
//
// O pacote não conhece HTTP nem SQL: declara portas, e quem monta a aplicação
// injeta as implementações.
package collect

import (
	"context"
	"time"

	"airtravel/internal/models"
)

// Provider é a fonte dos dados. Implementado pelo adapter da TAP.
//
// Devolve a resposta decodificada e o corpo bruto: o bruto é persistido como
// está, para que uma mudança de parsing não exija nova requisição.
type Provider interface {
	Search(ctx context.Context, p models.SearchParams) (*models.SearchResponse, []byte, error)
	Calendar(ctx context.Context, p models.SearchParams) (*models.CalendarResponse, []byte, error)
	CalendarReturns(ctx context.Context, p models.SearchParams) (*models.CalendarReturnsResponse, []byte, error)
}

// TreatedStore persiste os dados tratados e responde pela retomada.
type TreatedStore interface {
	SaveSearch(ctx context.Context, key models.SearchKey, resp *models.SearchResponse, rawKey string, scrapedAt time.Time) error
	SaveCalendar(ctx context.Context, key models.CalendarKey, resp *models.CalendarResponse, rawKey string, scrapedAt time.Time) error
	SaveReturns(ctx context.Context, key models.ReturnsKey, resp *models.CalendarReturnsResponse, rawKey string, scrapedAt time.Time) error

	// As três checagens de retomada recebem notBefore: uma coleta só conta como
	// feita se for MAIS NOVA que esse instante.
	//
	// Sem o corte, a retomada é permanente — a chave do calendário não inclui
	// data, então uma rota coletada uma vez ficaria marcada como pronta para
	// sempre e o modo padrão nunca atualizaria preço nenhum. Preço de passagem
	// muda todo dia; "já existe" não é a mesma coisa que "está atual".
	//
	// Zero desliga o corte e restaura a checagem por existência pura.
	Exists(ctx context.Context, searchKey string, notBefore time.Time) (bool, error)
	CalendarExists(ctx context.Context, calendarKey string, notBefore time.Time) (bool, error)
	ReturnsExists(ctx context.Context, returnsKey string, notBefore time.Time) (bool, error)
}

// RawStore persiste as respostas brutas.
type RawStore interface {
	SaveRaw(ctx context.Context, key string, scrapedAt time.Time, payload []byte) (string, error)
}
