#!/bin/bash
#
# Exercita scripts/migrar.sh sem tocar em máquina nenhuma.
#
# Uma migração só roda de verdade no dia em que ela precisa dar certo — não dá para
# ensaiar num servidor de produção. Então ssh, scp, psql e o próprio binário são
# substituídos por dublês que registram o que foi pedido, e o teste confere a sequência.
#
# O que este teste cobre:
#   - a chave de criptografia sai daqui e chega lá, e vai ANTES do instalador;
#   - a restauração roda sem --forcar (a recusa por chave errada é a rede de proteção);
#   - o servidor de origem não é parado nem apagado;
#   - contagem divergente entre origem e destino faz a migração falhar;
#   - medida vazia não é confundida com sucesso.
#
# Uso:  bash scripts/migrar_test.sh
#
set -uo pipefail

RAIZ=$(cd "$(dirname "$0")/.." && pwd)
FALHAS=0

ok()    { printf '  \033[32mok\033[0m   %s\n' "$1"; }
falha() { printf '  \033[31mFALHA\033[0m %s\n' "$1"; FALHAS=$((FALHAS + 1)); }

# montar_cenario cria um mundo falso: um /etc/vodmanager.env, um binário e dublês de
# ssh/scp/psql que gravam tudo o que recebem em REGISTRO.
montar_cenario() {
    CENARIO=$(mktemp -d)
    export REGISTRO="$CENARIO/registro.txt"
    : > "$REGISTRO"

    mkdir -p "$CENARIO/etc" "$CENARIO/bin" "$CENARIO/backups"
    cat > "$CENARIO/etc/vodmanager.env" <<AMB
VODM_DATABASE_URL=postgres://vodmanager:senha@localhost:5432/vodmanager?sslmode=disable
VODM_ENCRYPTION_KEY=CHAVE-SECRETA-DA-ORIGEM
VODM_HTTP_ADDR=:8080
AMB

    # Binário: só o subcomando backup é exercitado aqui.
    cat > "$CENARIO/bin/vodmanager" <<'BIN'
#!/bin/bash
echo "BINARIO $*" >> "$REGISTRO"
if [ "${1:-}" = "backup" ]; then
    for ((i=1; i<=$#; i++)); do
        if [ "${!i}" = "--arquivo" ]; then
            j=$((i+1)); echo "conteudo-do-backup" > "${!j}"
        fi
    done
fi
exit 0
BIN

    cat > "$CENARIO/bin/ssh" <<'SSHFAKE'
#!/bin/bash
argumentos=("$@")
alvo=""
comando=""
for ((i=0; i<${#argumentos[@]}; i++)); do
    case "${argumentos[$i]}" in
        -p|-o) i=$((i+1)) ;;
        -t|-O) ;;
        *) if [ -z "$alvo" ]; then alvo="${argumentos[$i]}"
           else comando="${argumentos[*]:$i}"; break; fi ;;
    esac
done

# O `ssh -O exit` da limpeza não é um comando remoto.
[ -z "$comando" ] && { echo "SSH-CONEXAO $alvo" >> "$REGISTRO"; exit 0; }

echo "SSH $comando" >> "$REGISTRO"

case "$comando" in
    *apt-get*echo\ apt*)         echo apt ;;
    *test\ -f*vodmanager.env*)   echo "${DESTINO_JA_TEM:-nao}" ;;
    *"umask 077"*)               cat >> "$REGISTRO.chave" ;;
    *healthz*)                   echo "${SAUDE_REMOTA:-ok}" ;;
    *psql*)
        entrada=$(cat)
        echo "SQL-REMOTO $entrada" >> "$REGISTRO"
        case "$entrada" in
            *count*) echo "${CONTAGEM_REMOTA-7}" ;;
            *max*)   echo "${MAIOR_ID_REMOTO-99}" ;;
        esac ;;
esac
exit 0
SSHFAKE

    cat > "$CENARIO/bin/scp" <<'SCPFAKE'
#!/bin/bash
echo "SCP $*" >> "$REGISTRO"
exit 0
SCPFAKE

    cat > "$CENARIO/bin/psql" <<'PSQLFAKE'
#!/bin/bash
consulta="${*: -1}"
echo "SQL-LOCAL $consulta" >> "$REGISTRO"
case "$consulta" in
    *count*)             echo "${CONTAGEM_LOCAL-7}" ;;
    *max*)               echo "${MAIOR_ID_LOCAL-99}" ;;
    *public_base_url*)   echo "${BASE_PUBLICA_LOCAL-}" ;;
esac
exit 0
PSQLFAKE

    chmod +x "$CENARIO/bin/"*
    export PATH="$CENARIO/bin:$PATH"
    export VODM_SEM_ROOT=1
    export VODM_AMBIENTE="$CENARIO/etc/vodmanager.env"
    export VODM_BINARIO="$CENARIO/bin/vodmanager"
    export VODM_PASTA_BACKUP="$CENARIO/backups"
}

desmontar_cenario() {
    rm -rf "$CENARIO"
    unset DESTINO_JA_TEM SAUDE_REMOTA CONTAGEM_REMOTA MAIOR_ID_REMOTO
    unset CONTAGEM_LOCAL MAIOR_ID_LOCAL BASE_PUBLICA_LOCAL
}

rodar() {
    bash "$RAIZ/scripts/migrar.sh" --destino root@198.51.100.7 </dev/null > "$CENARIO/saida.txt" 2>&1
    echo $?
}

# rodar_com passa opções adicionais. Existe para os cenários em que o modo de trabalho é a
# própria coisa sendo testada.
rodar_com() {
    bash "$RAIZ/scripts/migrar.sh" --destino root@198.51.100.7 "$@" </dev/null > "$CENARIO/saida.txt" 2>&1
    echo $?
}

# ---------------------------------------------------------------------------
echo
echo "migrar.sh — caminho feliz"
montar_cenario
codigo=$(rodar)

[ "$codigo" = "0" ] && ok "termina com sucesso" || {
    falha "esperava código 0, obtive $codigo"
    sed 's/^/       /' "$CENARIO/saida.txt"
}

grep -q 'CHAVE-SECRETA-DA-ORIGEM' "$REGISTRO.chave" 2>/dev/null \
    && ok "a chave de criptografia chega ao destino" \
    || falha "a chave NÃO foi enviada ao destino"

# A chave tem de chegar antes de o instalador rodar: o instalador só preserva a chave
# que já encontrar no lugar. Invertido, o destino nasce com chave nova e não lê nada.
linha_chave=$(grep -n 'umask 077' "$REGISTRO" | head -1 | cut -d: -f1)
linha_instalador=$(grep -n 'instalar.sh' "$REGISTRO" | head -1 | cut -d: -f1)
if [ -n "$linha_chave" ] && [ -n "$linha_instalador" ] && [ "$linha_chave" -lt "$linha_instalador" ]; then
    ok "a chave vai antes do instalador"
else
    falha "ordem errada: chave na linha ${linha_chave:-?}, instalador na ${linha_instalador:-?}"
fi

grep -q 'restaurar --arquivo' "$REGISTRO" \
    && ok "a restauração é disparada no destino" \
    || falha "a restauração não foi disparada"

grep -q 'restaurar.*--forcar' "$REGISTRO" \
    && falha "a restauração usou --forcar; isso desliga a checagem de chave" \
    || ok "a restauração NÃO usa --forcar"

grep -q 'systemctl stop vodmanager' "$REGISTRO" \
    && grep -q 'systemctl start vodmanager' "$REGISTRO" \
    && ok "o serviço do destino para e volta durante a carga" \
    || falha "o destino não foi parado/reiniciado em volta da restauração"

systemctl_local=$(grep -c 'BINARIO.*systemctl' "$REGISTRO" || true)
[ "$systemctl_local" = "0" ] && ok "nada foi desligado na origem" \
    || falha "a origem foi mexida"

[ -f "$VODM_AMBIENTE" ] && ok "o env da origem continua intacto" \
    || falha "o env da origem sumiu"

ls "$VODM_PASTA_BACKUP"/migracao-*.tar.gz >/dev/null 2>&1 \
    && ok "o backup da origem fica guardado aqui" \
    || falha "o backup não foi guardado na origem"
desmontar_cenario

# ---------------------------------------------------------------------------
# O nginx e o certificado são arquivos da máquina, não do banco: não viajam no backup.
# Uma instalação que atende por domínio e migra sem saber disso cai no instante em que o
# DNS apontar para o destino — endereço certo, máquina sem nginx.
echo
echo "migrar.sh — a instalação atende por domínio"
montar_cenario
export BASE_PUBLICA_LOCAL="https://vod.exemplo.com"
codigo=$(rodar)
[ "$codigo" = "0" ] && ok "termina com sucesso" || falha "esperava código 0, obtive $codigo"

grep -q 'vod.exemplo.com' "$CENARIO/saida.txt" \
    && ok "detecta o domínio configurado" || falha "não detectou o domínio"
grep -q 'NÃO vieram no backup' "$CENARIO/saida.txt" \
    && ok "avisa que nginx e certificado não migram" || falha "não avisou sobre o nginx"
grep -q 'dominio.sh vod.exemplo.com' "$CENARIO/saida.txt" \
    && ok "dá o comando exato para emitir o certificado" || falha "não deu o comando"
grep -q 'aponte o DNS' "$CENARIO/saida.txt" \
    && ok "diz que o DNS vem antes do certificado" || falha "não explicou a ordem"
desmontar_cenario

echo
echo "migrar.sh — a base pública é um IP, não um domínio"
montar_cenario
export BASE_PUBLICA_LOCAL="http://179.198.97.196:8080"
codigo=$(rodar)
[ "$codigo" = "0" ] && ok "termina com sucesso" || falha "esperava código 0, obtive $codigo"
grep -q 'NÃO vieram no backup' "$CENARIO/saida.txt" \
    && falha "tratou um IP como se fosse domínio" \
    || ok "um IP não é confundido com domínio"
desmontar_cenario

# ---------------------------------------------------------------------------
echo
echo "migrar.sh — o destino tem menos conteúdo que a origem"
montar_cenario
export CONTAGEM_LOCAL=1000
export CONTAGEM_REMOTA=998
codigo=$(rodar)
[ "$codigo" != "0" ] && ok "falha quando as contagens não batem" \
    || falha "aceitou uma migração com contagens diferentes"
grep -q 'NÃO batem' "$CENARIO/saida.txt" \
    && ok "diz que os números não batem" || falha "não avisou sobre a divergência"
grep -q 'continua no ar e intacto' "$CENARIO/saida.txt" \
    && ok "manda não desligar o servidor atual" || falha "não avisou para preservar a origem"
desmontar_cenario

# ---------------------------------------------------------------------------
echo
echo "migrar.sh — os ids mudaram"
montar_cenario
export MAIOR_ID_LOCAL=254025
export MAIOR_ID_REMOTO=254024
codigo=$(rodar)
[ "$codigo" != "0" ] && ok "falha quando o maior id não bate" \
    || falha "aceitou uma migração que não preservou os ids"
desmontar_cenario

# ---------------------------------------------------------------------------
echo
echo "migrar.sh — o banco do destino não responde"
montar_cenario
export CONTAGEM_REMOTA=""
export MAIOR_ID_REMOTO=""
codigo=$(rodar)
[ "$codigo" != "0" ] && ok "medida vazia não é confundida com sucesso" \
    || falha "medida vazia passou como migração conferida"
grep -q 'NÃO confirmei' "$CENARIO/saida.txt" \
    && ok "avisa que não conferiu" || falha "não deixou claro que a conferência não rodou"
desmontar_cenario

# ---------------------------------------------------------------------------
echo
echo "migrar.sh — o serviço do destino não sobe"
montar_cenario
export SAUDE_REMOTA=falhou
codigo=$(rodar)
[ "$codigo" != "0" ] && ok "falha quando o destino não responde" \
    || falha "declarou sucesso com o destino fora do ar"
desmontar_cenario

# ---------------------------------------------------------------------------
echo
echo "migrar.sh — recusas de entrada"
montar_cenario
saida=$(bash "$RAIZ/scripts/migrar.sh" 2>&1 || true)
grep -q 'informe o destino' <<< "$saida" \
    && ok "exige --destino" || falha "aceitou rodar sem destino"

saida=$(VODM_AMBIENTE="$CENARIO/nao-existe.env" \
        bash "$RAIZ/scripts/migrar.sh" --destino root@198.51.100.7 2>&1 || true)
grep -q 'que TEM os dados' <<< "$saida" \
    && ok "recusa rodar onde não há instalação" || falha "não checou a instalação de origem"

# Um destino que já tem dados precisa de confirmação digitada; entrada vazia cancela.
export DESTINO_JA_TEM=sim
saida=$(bash "$RAIZ/scripts/migrar.sh" --destino root@198.51.100.7 </dev/null 2>&1 || true)
grep -q 'Cancelado' <<< "$saida" \
    && ok "cancela quando o destino já tem dados e ninguém confirma" \
    || falha "sobrescreveu um destino com dados sem confirmação"
desmontar_cenario

# ---------------------------------------------------------------------------
# Modo somente dados.
#
# Reinstalar Postgres, Go e compilar leva vários minutos, e não faz sentido quando a máquina
# de destino já tem o sistema rodando — aí o que se quer é só trazer o catálogo e as
# decisões tomadas desde a última vez.
#
# O ponto delicado é o arquivo de configuração do destino. No modo completo ele pode ser
# reescrito, porque o instalador o regenera logo depois. Aqui não há instalador: sobrescrever
# levaria junto a URL do banco, e o destino não subiria mais.
echo
echo "migrar.sh — destino que já tem o sistema"
montar_cenario
export DESTINO_JA_TEM=sim
codigo=$(rodar_com --sim)

[ "$codigo" = "0" ] && ok "termina com sucesso" || {
    falha "esperava código 0, obtive $codigo"
    sed 's/^/       /' "$CENARIO/saida.txt"
}

grep -q 'instalar.sh' "$REGISTRO" \
    && falha "reinstalou o sistema num destino que já o tinha" \
    || ok "não reinstala nada no destino"

grep -q 'git clone\|git fetch' "$REGISTRO" \
    && falha "clonou o código num destino que já o tem" \
    || ok "não leva o código de novo"

# `cat > /etc/vodmanager.env` trunca o arquivo. Sem o instalador para regenerá-lo, isso
# apagaria a URL do banco do destino e ele nunca mais subiria.
grep -q 'cat > /etc/vodmanager.env' "$REGISTRO" \
    && falha "sobrescreveu o /etc/vodmanager.env do destino" \
    || ok "preserva a configuração do destino, trocando só a linha da chave"

grep -q 'VODM_ENCRYPTION_KEY=' "$REGISTRO" \
    && ok "a chave é conferida no destino" \
    || falha "a chave não foi tratada"

grep -q 'restaurar --arquivo' "$REGISTRO" \
    && ok "os dados são restaurados" || falha "os dados não foram restaurados"

grep -q 'schema_migrations' "$REGISTRO" \
    && ok "confere a compatibilidade do schema antes de enviar" \
    || falha "não conferiu o schema do destino"
desmontar_cenario

# --completo desfaz a escolha automática: quem quer refazer a instalação, refaz.
montar_cenario
export DESTINO_JA_TEM=sim
codigo=$(rodar_com --sim --completo)
grep -q 'instalar.sh' "$REGISTRO" \
    && ok "--completo reinstala mesmo num destino que já tem o sistema" \
    || falha "--completo não reinstalou"
desmontar_cenario

# Um destino MAIS ANTIGO recusaria o backup na restauração, por coluna inexistente. Melhor
# dizer isso antes de subir o arquivo do que depois.
montar_cenario
export DESTINO_JA_TEM=sim MAIOR_ID_LOCAL=11 MAIOR_ID_REMOTO=5
codigo=$(rodar_com --sim)
[ "$codigo" != "0" ] \
    && ok "falha quando o destino está numa versão mais antiga" \
    || falha "aceitou restaurar num destino mais antigo"
grep -q 'MAIS ANTIGA' "$CENARIO/saida.txt" \
    && ok "diz que o destino precisa ser atualizado antes" \
    || falha "não explicou o motivo da recusa"
grep -q 'SCP' "$REGISTRO" \
    && falha "enviou o backup antes de conferir a compatibilidade" \
    || ok "não gasta a subida do arquivo para nada"
desmontar_cenario

# --somente-dados num destino vazio não tem o que fazer.
montar_cenario
saida=$(bash "$RAIZ/scripts/migrar.sh" --destino root@198.51.100.7 --somente-dados </dev/null 2>&1 || true)
grep -q 'exige que o destino já tenha' <<< "$saida" \
    && ok "--somente-dados recusa um destino sem o sistema" \
    || falha "aceitou --somente-dados num destino vazio"
desmontar_cenario

# ---------------------------------------------------------------------------
echo
if [ "$FALHAS" -eq 0 ]; then
    printf '\033[32mTodos os testes passaram.\033[0m\n\n'
    exit 0
fi
printf '\033[31m%d teste(s) falharam.\033[0m\n\n' "$FALHAS"
exit 1
