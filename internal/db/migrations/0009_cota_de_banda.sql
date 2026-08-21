-- Cota de banda por credencial.
--
-- O modelo de venda é volume, não simultaneidade: "este cliente tem 4 GB", "aquele tem
-- 2 TB". Ao atingir a cota, o acesso é bloqueado até o administrador aumentar o limite ou
-- o ciclo renovar.
--
-- # Duas formas de cobrar, uma coluna a mais
--
-- `ciclo` distingue os dois modelos que fazem sentido aqui:
--
--   'nenhum' — balde único. Os 4 GB acabam e acabou, até alguém liberar mais. É o pré-pago.
--   'mensal' — renova todo dia 1º. É o plano recorrente.
--
-- A escolha é por credencial, porque um mesmo administrador costuma vender os dois.
ALTER TABLE stream_credentials
    -- NULL = sem cota. É o padrão, e mantém quem já existe funcionando como antes.
    ADD COLUMN bytes_limit  bigint,
    ADD COLUMN ciclo        text NOT NULL DEFAULT 'nenhum'
                            CHECK (ciclo IN ('nenhum', 'mensal')),
    -- bytes_ciclo é o consumo DO CICLO ATUAL, e é ele que a cota compara.
    --
    -- Separado de bytes_served de propósito: aquele é o total histórico, que o
    -- administrador usa para entender o cliente ao longo do tempo. Zerar o histórico a
    -- cada mês apagaria justamente a informação que diz se vale renovar o contrato.
    ADD COLUMN bytes_ciclo  bigint NOT NULL DEFAULT 0,
    ADD COLUMN ciclo_inicio timestamptz NOT NULL DEFAULT now();

ALTER TABLE stream_credentials
    ADD CONSTRAINT stream_credentials_cota_positiva
        CHECK (bytes_limit IS NULL OR bytes_limit > 0);

COMMENT ON COLUMN stream_credentials.bytes_limit IS
    'Cota de banda em bytes. NULL = sem limite. Comparada contra bytes_ciclo.';
COMMENT ON COLUMN stream_credentials.bytes_ciclo IS
    'Consumo do ciclo atual. Zerado na renovação; bytes_served guarda o total histórico.';
