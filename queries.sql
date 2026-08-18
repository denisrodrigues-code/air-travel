-- Consultas de demonstração sobre os dados tratados.
-- Uso: ./run.sh queries   (ou `make queries`, se tiver make)
--
-- As agregações de preço quebram por market E por adults, sempre. Misturar
-- mercados soma EUR com BRL; misturar contagens de passageiros soma o total de 1
-- com o de 2 — e as duas coisas produzem um número que não significa nada.

\echo '== 1. O que foi coletado =='
SELECT 'calendar_prices'        AS tabela, count(*) AS linhas FROM calendar_prices
UNION ALL
SELECT 'calendar_return_prices', count(*) FROM calendar_return_prices
UNION ALL
SELECT 'searches',               count(*) FROM searches
UNION ALL
SELECT 'flights',                count(*) FROM flights
UNION ALL
SELECT 'segments',               count(*) FROM segments
UNION ALL
SELECT 'technical_stops',        count(*) FROM technical_stops
UNION ALL
SELECT 'offers',                 count(*) FROM offers
UNION ALL
SELECT 'airports',               count(*) FROM airports
ORDER BY 1;

\echo ''
\echo '== 2. Melhor preço por rota e tipo de viagem (calendário) =='
SELECT origin || ' -> ' || destination      AS rota,
       trip_type                            AS tipo,
       market                               AS mercado,
       adults                               AS adultos,
       count(*)                             AS datas,
       min(best_total_price) || ' ' || min(currency) AS menor,
       round(avg(best_total_price), 2)       AS media
FROM calendar_prices
WHERE NOT no_flights AND NOT sold_out
GROUP BY 1, 2, 3, 4
ORDER BY 1, 2, 3, 4;

\echo ''
\echo '== 3. As 10 datas de partida mais baratas =='
SELECT departure_airport || '-' || arrival_airport AS trecho,
       to_char(departure_date, 'DD/MM/YYYY')       AS partida,
       trip_type                                   AS tipo,
       adults                                      AS adultos,
       best_total_price || ' ' || currency         AS preco
FROM calendar_prices
WHERE NOT no_flights AND NOT sold_out
ORDER BY best_total_price, departure_date
LIMIT 10;

\echo ''
\echo '== 4. Matriz ida x volta: as 10 combinações mais baratas =='
SELECT origin || '-' || coalesce(resolved_dest, destination) AS trecho,
       to_char(departure_date, 'DD/MM')                      AS ida,
       to_char(return_date, 'DD/MM')                         AS volta,
       nights                                                AS noites,
       adults                                                AS adultos,
       total_price || ' ' || currency                         AS total
FROM calendar_return_prices
WHERE NOT no_flights AND NOT sold_out
ORDER BY total_price, nights
LIMIT 10;

\echo ''
\echo '== 5. Menor preço por duração de viagem (1 a 3 semanas) =='
-- Agrupado também por market e adults: sem isso, uma coleta em BRL ao lado de uma
-- em EUR faria o "menor" ser sempre o da moeda de menor valor nominal.
SELECT nights                        AS noites,
       market                        AS mercado,
       adults                        AS adultos,
       min(total_price)              AS menor,
       round(avg(total_price), 2)    AS media,
       count(*)                      AS combinacoes
FROM calendar_return_prices
WHERE NOT no_flights AND NOT sold_out AND nights BETWEEN 7 AND 21
GROUP BY 1, 2, 3
ORDER BY 4
LIMIT 10;

\echo ''
\echo '== 6. Voos detalhados (modo search): origem, destino, voo, horarios, valor =='
SELECT s.carrier || s.flight_number                      AS voo,
       s.departure_airport || '->' || s.arrival_airport   AS trecho,
       to_char(s.departure_time, 'DD/MM HH24:MI')         AS partida,
       to_char(s.arrival_time, 'DD/MM HH24:MI')           AS chegada,
       -- A duracao vem da API: subtrair os horarios daria errado, porque cada
       -- um esta no fuso do seu aeroporto. Ver models.ParseWallClock.
       (f.duration_minutes / 60) || 'h' ||
         lpad((f.duration_minutes % 60)::text, 2, '0')     AS duracao,
       -- number_of_stops conta apenas CONEXÕES. A parada técnica (mesmo número
       -- de voo, sem troca de aeronave) está em technical_stops, e é ela que
       -- explica um voo de 14h15 na mesma rota em que os diretos levam 9h55.
       -- A TAP soma as duas ao anunciar "1 escala" ao usuário.
       f.number_of_stops                                    AS conexoes,
       (SELECT count(*) FROM technical_stops t
         WHERE t.search_key = s.search_key
           AND t.flight_id = s.flight_id)                   AS escalas_tec,
       o.fare_family                                        AS tarifa,
       -- Dois preços, e a distinção não é cosmética: a TAP exibe ao usuário o
       -- da perna de IDA; total_price é a viagem inteira.
       coalesce(o.outbound_price::text, '-')                AS ida,
       o.total_price || ' ' || o.currency                   AS total
FROM segments s
JOIN flights f ON f.search_key = s.search_key AND f.flight_id = s.flight_id
JOIN offers  o ON o.search_key = s.search_key AND o.flight_id = s.flight_id
WHERE s.seq = 0
ORDER BY o.total_price, s.departure_time
LIMIT 10;

\echo ''
\echo '== 7. Aeroportos resolvidos a partir do bloco translate =='
SELECT code, name, city, country FROM airports ORDER BY code;

\echo ''
\echo '== 8. Idade das coletas: a retomada só aproveita o que for recente =='
-- Com -resume (padrão) e -resume-max-age de 24 h, o que estiver mais velho que
-- isto é recoletado na próxima execução.
SELECT 'calendar_prices' AS tabela,
       to_char(max(scraped_at), 'DD/MM HH24:MI') AS coleta_mais_recente,
       to_char(min(scraped_at), 'DD/MM HH24:MI') AS mais_antiga,
       count(DISTINCT calendar_key)              AS series
FROM calendar_prices
UNION ALL
SELECT 'calendar_return_prices',
       to_char(max(scraped_at), 'DD/MM HH24:MI'),
       to_char(min(scraped_at), 'DD/MM HH24:MI'),
       count(DISTINCT returns_key)
FROM calendar_return_prices
UNION ALL
SELECT 'searches',
       to_char(max(scraped_at), 'DD/MM HH24:MI'),
       to_char(min(scraped_at), 'DD/MM HH24:MI'),
       count(DISTINCT search_key)
FROM searches
ORDER BY 1;

\echo ''
\echo '== 9. Rastreabilidade: dados tratados -> resposta bruta no Redis =='
SELECT DISTINCT raw_key FROM calendar_prices
UNION
SELECT DISTINCT raw_key FROM calendar_return_prices
UNION
SELECT DISTINCT raw_key FROM searches
ORDER BY 1;
