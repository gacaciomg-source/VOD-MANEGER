-- Tentar de novo, com espera crescente.
--
-- # O que estava errado
--
-- Uma cópia que falhava virava erro DEFINITIVO. A linha ficava vermelha no painel e ninguém
-- a trazia de volta — nem quando a causa era claramente passageira.
--
-- E a causa quase sempre é passageira. "403 Forbidden" numa fonte de IPTV raramente significa
-- "você não tem direito a este filme": significa "você pediu demais nesta hora". Uma hora
-- depois a mesma URL entrega o vídeo inteiro. O sistema desistia de um conteúdo que teria
-- conseguido baixar sozinho.
--
-- # Por que espera crescente, e não repetição imediata
--
-- Se a fonte recusou por excesso de acesso, insistir na hora é fazer exatamente o que a
-- levou a recusar. A espera dobra a cada tentativa: dá tempo de o limite virar, e desiste
-- rápido quando a causa é permanente.
--
-- # Por que um limite de tentativas
--
-- Sem ele, um link morto seria retentado para sempre — uma consulta e uma conexão a cada
-- ciclo, por meses, para nunca funcionar. O limite transforma "falha permanente" numa
-- conclusão que o sistema alcança sozinho, em vez de num estado que ele nunca revisita.

ALTER TABLE arquivos_guardados
    ADD COLUMN tentativas integer NOT NULL DEFAULT 0,
    ADD COLUMN tentar_apos timestamptz;

COMMENT ON COLUMN arquivos_guardados.tentativas IS
    'quantas vezes a cópia já falhou; ao chegar no limite, o erro passa a ser definitivo';
COMMENT ON COLUMN arquivos_guardados.tentar_apos IS
    'instante a partir do qual a fila volta a enxergar esta linha; nulo é "agora"';

-- O índice da fila precisa conhecer a espera, ou ela seria filtrada depois de lida — e uma
-- fila cheia de linhas em espera faria o baixador varrer tudo para não achar nada.
DROP INDEX IF EXISTS arquivos_guardados_fila_idx;
CREATE INDEX arquivos_guardados_fila_idx
    ON arquivos_guardados (adiantado DESC, tentar_apos, criado_em)
    WHERE estado IN ('pendente', 'baixando');
