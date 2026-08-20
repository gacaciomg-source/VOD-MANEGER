-- Índice para a busca por título do painel.
--
-- A busca compara o termo contra dois campos: normalized_title e title. Só o primeiro
-- tinha índice trigram — e uma condição OR onde um lado não é indexável obriga o Postgres
-- a varrer a tabela inteira, duas vezes (uma para a contagem, outra para a página).
--
-- Medido no acervo real, com 16 mil filmes e 8 mil séries: 3,3s para "bob" e 7,5s para
-- "a". Era isso que fazia a tela piscar e parecer travada a cada tecla digitada.
--
-- Com os dois lados indexados, o planejador combina os dois índices (BitmapOr) em vez de
-- varrer tudo.
CREATE INDEX contents_title_trgm ON contents USING gin (title gin_trgm_ops);

-- A busca sempre restringe por tipo e descarta os removidos. Este índice parcial evita
-- que a verificação recaia sobre a tabela depois do casamento por trigrama.
CREATE INDEX contents_tipo_ativo_idx ON contents (type, id)
    WHERE status <> 'deleted';
