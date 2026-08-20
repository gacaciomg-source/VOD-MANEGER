-- Índices nas colunas que referenciam source_variants.
--
-- O PostgreSQL cria índice automaticamente para chaves PRIMÁRIAS e UNIQUE, mas NÃO para
-- chaves ESTRANGEIRAS. Sem eles, cada linha removida de source_variants obriga o banco a
-- varrer `contents` e `episodes` inteiras, uma vez por coluna que aponta para ela — são
-- seis colunas.
--
-- Efeito observado: excluir uma fonte com 1.633 variantes e 1.607 conteúdos travava por
-- minutos, porque a operação virava ~1.633 × 6 varreduras completas de tabela. Com os
-- índices, cada verificação é uma busca direta.
--
-- Vale para qualquer operação que remova variantes, não só a exclusão de fonte.

CREATE INDEX contents_primary_variant_idx   ON contents (primary_variant_id)   WHERE primary_variant_id   IS NOT NULL;
CREATE INDEX contents_secondary_variant_idx ON contents (secondary_variant_id) WHERE secondary_variant_id IS NOT NULL;
CREATE INDEX contents_tertiary_variant_idx  ON contents (tertiary_variant_id)  WHERE tertiary_variant_id  IS NOT NULL;

CREATE INDEX episodes_primary_variant_idx   ON episodes (primary_variant_id)   WHERE primary_variant_id   IS NOT NULL;
CREATE INDEX episodes_secondary_variant_idx ON episodes (secondary_variant_id) WHERE episodes.secondary_variant_id IS NOT NULL;
CREATE INDEX episodes_tertiary_variant_idx  ON episodes (tertiary_variant_id)  WHERE episodes.tertiary_variant_id  IS NOT NULL;

-- A exclusão de uma fonte também percorre estas tabelas por source_id.
CREATE INDEX IF NOT EXISTS unresolved_items_source_idx ON unresolved_items (source_id);
