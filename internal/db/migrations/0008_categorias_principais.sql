-- Categorias principais: as pastas que o administrador escolheu manter.
--
-- # O problema
--
-- Até aqui, cada categoria que uma fonte declarava virava uma categoria canônica nova,
-- criada automaticamente com o nome que a fonte deu. "Filmes | Lancamentos" da fonte A e
-- "LANÇAMENTOS" da fonte B viravam DUAS pastas, e sobrava para o administrador mesclar as
-- duas depois — a cada fonte nova, de novo.
--
-- Não existia lista de pendências, porque nada ficava pendente: tudo já nascia vinculado a
-- si mesmo. E as sugestões de mesclagem tinham que adivinhar, porque a decisão do
-- administrador nunca havia sido registrada em lugar nenhum.
--
-- # A inversão
--
-- Agora o administrador marca quais categorias são PRINCIPAIS. A sincronização só usa
-- essas. Uma categoria de fonte que não casa com nenhuma principal fica pendente,
-- esperando uma decisão que é tomada UMA vez e vale para sempre.
--
-- O padrão é false para as existentes, por escolha do administrador: ele prefere
-- selecionar as que valem em vez de herdar cinquenta pastas criadas automaticamente.
ALTER TABLE categories
    ADD COLUMN principal boolean NOT NULL DEFAULT false;

-- A busca da sincronização é sempre por nome normalizado + tipo, entre as principais.
CREATE INDEX categories_principais_idx ON categories (normalized_name, content_type)
    WHERE principal;

COMMENT ON COLUMN categories.principal IS
    'Categoria escolhida pelo administrador como destino final. A sincronização só vincula a estas; as demais existem apenas como histórico.';

-- Categoria de fonte sem vínculo é uma PENDÊNCIA, não um erro. Este índice é o que faz a
-- tela de pendências ser instantânea mesmo com muitas fontes.
CREATE INDEX source_categories_pendentes_idx ON source_categories (content_type, normalized_name)
    WHERE category_id IS NULL;

-- Limpeza das linhas com tipo 'unknown'.
--
-- Elas são resíduo do comportamento antigo: quando a fonte não dizia se a categoria era de
-- filme ou de série (o caso do M3U, que só tem group-title), a categoria era registrada com
-- tipo 'unknown' e nunca vinculada a nada. Quem de fato classificava era o caminho por
-- item, com o tipo verdadeiro.
--
-- O código novo não cria mais linhas assim. Deixá-las no banco encheria a tela de
-- pendências com dezenas de itens impossíveis de resolver — o seletor de destino só
-- oferece categorias de filme ou de série, e nenhuma casa com 'unknown'.
--
-- É seguro apagar: elas não carregam vínculo nenhum, e as categorias de verdade continuam
-- intactas.
DELETE FROM source_categories WHERE content_type = 'unknown' AND category_id IS NULL;
