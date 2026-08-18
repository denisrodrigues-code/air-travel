-- Schema dos dados tratados da TAP (booking.flytap.com, API BFM).
-- Aplicado automaticamente por storage.NewPostgres na inicialização.

CREATE TABLE IF NOT EXISTS searches (
    search_key   TEXT PRIMARY KEY,
    origin       TEXT        NOT NULL,
    destination  TEXT        NOT NULL,
    depart_date  DATE        NOT NULL,
    return_date  DATE,
    cabin_class  TEXT        NOT NULL,
    market       TEXT        NOT NULL,
    adults       INT         NOT NULL,
    currency     TEXT,
    office_id    TEXT,
    total_offers INT         NOT NULL DEFAULT 0,
    raw_key      TEXT,
    scraped_at   TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS flights (
    search_key       TEXT NOT NULL REFERENCES searches (search_key) ON DELETE CASCADE,
    flight_id        INT  NOT NULL,
    duration_minutes INT  NOT NULL,
    number_of_stops  INT  NOT NULL,
    route            TEXT NOT NULL,
    -- Sem fuso de propósito: a API devolve hora local do aeroporto com um
    -- sufixo Z falso. Ver models.ParseWallClock.
    departure_time   TIMESTAMP,
    arrival_time     TIMESTAMP,
    PRIMARY KEY (search_key, flight_id)
);

CREATE TABLE IF NOT EXISTS segments (
    search_key         TEXT        NOT NULL,
    flight_id          INT         NOT NULL,
    seq                INT         NOT NULL,
    carrier            TEXT        NOT NULL,
    operating_carrier  TEXT,
    flight_number      TEXT        NOT NULL,
    departure_airport  TEXT        NOT NULL,
    arrival_airport    TEXT        NOT NULL,
    -- Hora local do aeroporto, sem fuso. Ver models.ParseWallClock.
    departure_time     TIMESTAMP   NOT NULL,
    arrival_time       TIMESTAMP   NOT NULL,
    duration_minutes   INT         NOT NULL,
    stop_time_minutes  INT         NOT NULL,
    equipment          TEXT,
    departure_terminal TEXT,
    arrival_terminal   TEXT,
    codeshare          BOOLEAN     NOT NULL DEFAULT FALSE,
    PRIMARY KEY (search_key, flight_id, seq),
    FOREIGN KEY (search_key, flight_id)
        REFERENCES flights (search_key, flight_id) ON DELETE CASCADE
);

-- Paradas técnicas de um segmento: o avião pousa e decola com o mesmo número de
-- voo. Tabela própria porque são 0..n por segmento e a TAP as conta como escala
-- na interface, embora segments.number_of_stops (que conta conexões) seja 0.
CREATE TABLE IF NOT EXISTS technical_stops (
    search_key       TEXT      NOT NULL,
    flight_id        INT       NOT NULL,
    seq              INT       NOT NULL, -- segmento dentro do voo
    stop_seq         INT       NOT NULL, -- parada dentro do segmento
    location         TEXT      NOT NULL,
    -- Hora local do aeroporto da escala, sem fuso. Mesma regra dos segmentos.
    arrival_time     TIMESTAMP,
    departure_time   TIMESTAMP,
    duration_minutes INT       NOT NULL,
    PRIMARY KEY (search_key, flight_id, seq, stop_seq),
    FOREIGN KEY (search_key, flight_id, seq)
        REFERENCES segments (search_key, flight_id, seq) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS offers (
    search_key                TEXT           NOT NULL,
    offer_id                  INT            NOT NULL,
    flight_id                 INT            NOT NULL,
    currency                  TEXT           NOT NULL,
    cabin                     TEXT,
    fare_family               TEXT,
    commercial_fare_family    TEXT,
    fare_family_rank          INT,
    total_price               NUMERIC(12, 2) NOT NULL,
    -- Preço só da perna de ida, que é o que a TAP mostra nos cartões de voo.
    -- total_price é a viagem inteira. NULL quando a resposta não traz a perna.
    outbound_price            NUMERIC(12, 2),
    base_price                NUMERIC(12, 2) NOT NULL,
    tax                       NUMERIC(12, 2) NOT NULL,
    super_saver               BOOLEAN        NOT NULL DEFAULT FALSE,
    discounted_with_promocode BOOLEAN        NOT NULL DEFAULT FALSE,
    fare_basis                TEXT[],
    rbd                       TEXT[],
    PRIMARY KEY (search_key, offer_id, flight_id),
    FOREIGN KEY (search_key, flight_id)
        REFERENCES flights (search_key, flight_id) ON DELETE CASCADE
);

-- Calendário de melhores preços (POST booking/availability/calendar/).
-- Uma consulta devolve um ano de preços diários, daí a granularidade por data.
CREATE TABLE IF NOT EXISTS calendar_prices (
    calendar_key      TEXT           NOT NULL,
    origin            TEXT           NOT NULL,
    destination       TEXT           NOT NULL,
    departure_airport TEXT           NOT NULL,
    arrival_airport   TEXT           NOT NULL,
    departure_date    DATE           NOT NULL,
    cabin_class       TEXT           NOT NULL,
    trip_type         TEXT           NOT NULL,
    market            TEXT           NOT NULL,
    -- adults compõe a calendar_key e portanto a série: o preço de 1 e de 2
    -- adultos são valores diferentes para a mesma data. Sem a coluna, as duas
    -- séries coexistem no banco sem forma de distinguí-las, e uma agregação
    -- mistura as duas — o mesmo erro que misturar EUR e BRL por ignorar market.
    adults            INT            NOT NULL DEFAULT 1,
    currency          TEXT           NOT NULL,
    best_total_price  NUMERIC(12, 2) NOT NULL,
    best_total_miles  INT            NOT NULL DEFAULT 0,
    monthly_minimum   BOOLEAN        NOT NULL DEFAULT FALSE,
    monthly_maximum   BOOLEAN        NOT NULL DEFAULT FALSE,
    star_alliance     BOOLEAN        NOT NULL DEFAULT FALSE,
    sold_out          BOOLEAN        NOT NULL DEFAULT FALSE,
    no_flights        BOOLEAN        NOT NULL DEFAULT FALSE,
    -- insertion_date é quando a TAP calculou o preço; scraped_at é a coleta.
    insertion_date    TIMESTAMP,
    raw_key           TEXT,
    scraped_at        TIMESTAMPTZ    NOT NULL,
    PRIMARY KEY (calendar_key, departure_date)
);

CREATE INDEX IF NOT EXISTS idx_calendar_route_date
    ON calendar_prices (origin, destination, departure_date);
CREATE INDEX IF NOT EXISTS idx_calendar_cheapest
    ON calendar_prices (origin, destination, best_total_price)
    WHERE no_flights = FALSE AND sold_out = FALSE;

-- Matriz ida x volta (POST booking/availability/calendarReturns/).
-- Dada uma data de ida, a API devolve o preço TOTAL para cada data de retorno.
CREATE TABLE IF NOT EXISTS calendar_return_prices (
    returns_key      TEXT           NOT NULL,
    origin           TEXT           NOT NULL,
    destination      TEXT           NOT NULL,
    resolved_dest    TEXT,
    departure_date   DATE           NOT NULL,
    return_date      DATE           NOT NULL,
    nights           INT            NOT NULL,
    cabin_class      TEXT           NOT NULL,
    market           TEXT           NOT NULL,
    -- Ver a nota de calendar_prices.adults.
    adults           INT            NOT NULL DEFAULT 1,
    currency         TEXT           NOT NULL,
    total_price      NUMERIC(12, 2) NOT NULL,
    miles            INT            NOT NULL DEFAULT 0,
    monthly_minimum  BOOLEAN        NOT NULL DEFAULT FALSE,
    monthly_maximum  BOOLEAN        NOT NULL DEFAULT FALSE,
    sold_out         BOOLEAN        NOT NULL DEFAULT FALSE,
    no_flights       BOOLEAN        NOT NULL DEFAULT FALSE,
    direct_flight    BOOLEAN        NOT NULL DEFAULT FALSE,
    raw_key          TEXT,
    scraped_at       TIMESTAMPTZ    NOT NULL,
    PRIMARY KEY (returns_key, return_date)
);

CREATE INDEX IF NOT EXISTS idx_returns_route
    ON calendar_return_prices (origin, destination, departure_date, return_date);
CREATE INDEX IF NOT EXISTS idx_returns_cheapest
    ON calendar_return_prices (origin, destination, total_price)
    WHERE no_flights = FALSE AND sold_out = FALSE;
CREATE INDEX IF NOT EXISTS idx_returns_nights
    ON calendar_return_prices (origin, destination, nights, total_price);

-- ---------------------------------------------------------------------------
-- Migrações para bancos criados por versões anteriores. Idempotentes.
-- ---------------------------------------------------------------------------

-- adults passou a ser coluna depois de se notar que duas coletas da mesma rota
-- com contagens diferentes de passageiros produziam duas séries de preços
-- indistinguíveis. O default 1 é o valor histórico: era o padrão do CLI.
ALTER TABLE calendar_prices        ADD COLUMN IF NOT EXISTS adults INT NOT NULL DEFAULT 1;
ALTER TABLE calendar_return_prices ADD COLUMN IF NOT EXISTS adults INT NOT NULL DEFAULT 1;

-- outbound_price passou a ser coluna depois de se comparar a coleta com a tela
-- da TAP: o preço exibido ao usuário é o da perna de ida, e só o total estava
-- sendo gravado. Nullable sem default porque as linhas antigas não têm o valor —
-- um default numérico inventaria um preço que nunca foi coletado.
ALTER TABLE offers ADD COLUMN IF NOT EXISTS outbound_price NUMERIC(12, 2);

-- Correção de tipo para bancos criados antes de se descobrir que os horários da
-- API são hora local, não UTC.
--
-- Condicionada de propósito: um ALTER COLUMN TYPE incondicional roda a cada
-- inicialização e toma ACCESS EXCLUSIVE na tabela, mesmo quando não há nada
-- para converter.
DO $migrate$
DECLARE
    col RECORD;
BEGIN
    FOR col IN
        SELECT table_name, column_name
          FROM information_schema.columns
         WHERE table_schema = current_schema()
           AND table_name IN ('flights', 'segments')
           AND column_name IN ('departure_time', 'arrival_time')
           AND data_type <> 'timestamp without time zone'
    LOOP
        EXECUTE format('ALTER TABLE %I ALTER COLUMN %I TYPE TIMESTAMP',
                       col.table_name, col.column_name);
    END LOOP;
END
$migrate$;

-- Dicionários vindos do bloco "translate" da resposta.
CREATE TABLE IF NOT EXISTS airports (
    code         TEXT PRIMARY KEY,
    name         TEXT,
    city         TEXT,
    country      TEXT,
    country_code TEXT
);

CREATE TABLE IF NOT EXISTS airlines (
    code TEXT PRIMARY KEY,
    name TEXT
);

CREATE INDEX IF NOT EXISTS idx_searches_route
    ON searches (origin, destination, depart_date);
CREATE INDEX IF NOT EXISTS idx_offers_price
    ON offers (search_key, total_price);
CREATE INDEX IF NOT EXISTS idx_segments_flight_number
    ON segments (carrier, flight_number);
