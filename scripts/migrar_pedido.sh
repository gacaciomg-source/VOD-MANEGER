#!/bin/bash
#
# Executa a migração pedida pelo painel.
#
# É a ponte entre a tela de Migração e o scripts/migrar.sh, que é quem faz o trabalho. O
# painel não pode executar nada: ele só escreve um pedido na única pasta onde tem permissão
# de escrever, e o systemd — que é root — chama este script.
#
# # O formato do pedido
#
# Uma linha por campo, em /opt/vodmanager/runtime/solicitar-migracao:
#
#     destino=root@203.0.113.10
#     porta_ssh=22
#     porta_app=8080
#     senha=<senha do SSH do destino>
#
# A senha é o motivo de este arquivo existir separado. Ela:
#
#   - é apagada do disco ANTES de a migração começar, não depois. Se o script morrer no
#     meio, ela já não está mais lá;
#   - nunca vira argumento de comando. Argumento aparece no `ps` para QUALQUER usuário da
#     máquina enquanto o comando roda — e uma migração roda por muitos minutos;
#   - nunca é impressa no registro, que o painel exibe na tela.
#
# Ela viaja até o ssh pela variável SSHPASS, que é como o sshpass foi feito para recebê-la.
# O ambiente de um processo (/proc/PID/environ) só é legível pelo dono do processo — aqui,
# o root. É a diferença entre "quem já é root vê" e "qualquer um vê", e é ela que separa
# esta escolha da linha de comando.
#
set -uo pipefail

# O systemd nao define HOME em servicos de sistema, e certbot, git e ssh procuram a
# configuracao deles a partir dele. Sem esta linha, o que funciona pelo terminal (onde o
# sudo define o HOME) falha pelo botao do painel — que e o caminho que existe justamente
# para nao precisar de terminal.
export HOME="${HOME:-/root}"

PEDIDO=/opt/vodmanager/runtime/solicitar-migracao
REGISTRO=/opt/vodmanager/runtime/ultima-migracao.log
FONTE="${VODM_FONTE:-/opt/vodmanager-fonte}"

mkdir -p /opt/vodmanager/runtime
chown vodmanager:vodmanager /opt/vodmanager/runtime 2>/dev/null || true
exec > >(tee "$REGISTRO") 2>&1
chown vodmanager:vodmanager "$REGISTRO" 2>/dev/null || true

falhar() { printf '\n\033[31m%s\033[0m\n' "$*"; exit 1; }

[ -f "$PEDIDO" ] || falhar "erro: não há pedido de migração."

# Lê e apaga. A partir daqui a senha existe apenas em memória.
CONTEUDO=$(cat "$PEDIDO")
shred -u "$PEDIDO" 2>/dev/null || rm -f "$PEDIDO"

DESTINO=""; PORTA_SSH=22; PORTA_APP=8080; SENHA=""
while IFS='=' read -r chave valor; do
    case "$chave" in
        destino)   DESTINO="$valor" ;;
        porta_ssh) PORTA_SSH="$valor" ;;
        porta_app) PORTA_APP="$valor" ;;
        senha)     SENHA="$valor" ;;
    esac
done <<< "$CONTEUDO"
CONTEUDO=""

[ -n "$DESTINO" ] || falhar "erro: o pedido não trouxe o destino."

printf '\033[1mMigração iniciada em %s\033[0m\n' "$(date '+%F %H:%M:%S')"
echo "Destino: $DESTINO (porta SSH $PORTA_SSH, painel na porta $PORTA_APP)"
echo

# O sshpass é o que permite a migração sem terminal. Ele não é instalado por padrão porque
# só faz falta aqui.
if ! command -v sshpass >/dev/null; then
    echo "Instalando sshpass..."
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq && apt-get install -y -qq sshpass >/dev/null \
        || falhar "erro: não consegui instalar o sshpass."
fi

# --sim porque ninguém está na frente de um terminal para digitar "substituir": a
# confirmação já aconteceu no painel, com o aviso na tela e um clique.
export SSHPASS="$SENHA"
SENHA=""
/bin/bash "$FONTE/scripts/migrar.sh" \
    --destino "$DESTINO" \
    --porta-ssh "$PORTA_SSH" \
    --porta-app "$PORTA_APP" \
    --sim
CODIGO=$?
unset SSHPASS

if [ "$CODIGO" -ne 0 ]; then
    printf '\n\033[31mA migração NÃO terminou.\033[0m O servidor atual continua no ar e intacto.\n'
fi
exit "$CODIGO"
