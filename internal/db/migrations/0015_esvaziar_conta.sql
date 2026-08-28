-- Esvaziar uma conta de nuvem, movendo o acervo dela para outra.
--
-- # Por que uma marca, e não um comando
--
-- Mover o conteúdo de uma conta é baixar e subir cada arquivo de novo — dezenas ou centenas
-- de gigabytes, horas ou dias de trabalho. Não cabe numa requisição do painel: ela expiraria
-- muito antes, e uma migração interrompida no meio deixaria metade do acervo apontando para
-- um lugar e metade para outro.
--
-- A marca transforma isso num ESTADO, e não num evento. O sistema vê a conta marcada e vai
-- movendo um arquivo por vez, no ritmo dele. Se o serviço reiniciar, ele retoma de onde
-- parou, porque a marca continua lá. E o painel pode mostrar o progresso, porque a resposta
-- é sempre "quantos ainda faltam".
--
-- # Por que não apagar a conta direto
--
-- Remover uma conta com acervo dentro perde o acervo. Esvaziar primeiro, e remover depois com
-- a conta vazia, separa uma decisão reversível de uma definitiva.

ALTER TABLE nuvens
    ADD COLUMN esvaziando boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN nuvens.esvaziando IS
    'true quando o acervo desta conta está sendo movido para outra, um arquivo por vez';

-- Uma conta esvaziando não pode receber cópias novas: seria encher de um lado enquanto se
-- esvazia do outro. O banco recusa a combinação em vez de confiar em quem escreve.
ALTER TABLE nuvens
    ADD CONSTRAINT nuvens_esvaziando_nao_recebe
    CHECK (NOT esvaziando OR somente_leitura);
