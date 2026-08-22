#!/bin/bash
#
# Migra esta instalação do VOD Manager para outra máquina, inteira.
#
# Roda no servidor ATUAL (o que tem os dados) e leva para o destino:
#   - o catálogo inteiro, com os MESMOS ids — os links que os seus clientes já têm
#     continuam apontando para o mesmo filme e o mesmo episódio;
#   - a chave de criptografia, sem a qual as credenciais das fontes e as senhas dos
#     clientes viram bytes ilegíveis;
#   - os usuários do painel, as fontes, as credenciais de saída e o consumo já contado.
#
# O que NÃO faz, de propósito:
#   - não desliga o servidor atual. Enquanto você confere o destino, os seus clientes
#     continuam assistindo aqui. Desligar é uma decisão sua, depois de conferir.
#   - não apaga nada daqui.
#
# Uso:
#     sudo ./scripts/migrar.sh --destino root@203.0.113.10
#
# A senha do SSH é pedida UMA vez: a conexão é reaproveitada para todos os passos.
#
set -euo pipefail

# Sobrescrevíveis para que o script possa ser exercitado fora de uma máquina de produção.
# Em uso normal ninguém define nada disso.
AMBIENTE="${VODM_AMBIENTE:-/etc/vodmanager.env}"
BINARIO="${VODM_BINARIO:-/opt/vodmanager/vodmanager}"
PASTA_BACKUP="${VODM_PASTA_BACKUP:-/var/backups/vodmanager}"
REPO="https://github.com/gacaciomg-source/VOD-MANEGER.git"
DESTINO=""
PORTA_SSH=22
PORTA_APP="${VODM_PORTA:-8080}"
PASTA_REMOTA="/root/VOD-MANEGER"
# SEM_PERGUNTA vale para quando a migração é disparada pelo painel: não há terminal para
# digitar "substituir", e a confirmação já aconteceu na tela, com o aviso e um clique.
SEM_PERGUNTA=0

vermelho() { printf '\033[31m%s\033[0m\n' "$*"; }
verde()    { printf '\033[32m%s\033[0m\n' "$*"; }
passo()    { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
aviso()    { printf '\033[33m    %s\033[0m\n' "$*"; }
erro()     { vermelho "erro: $*"; exit 1; }

ajuda() {
    echo "Uso:  sudo ./scripts/migrar.sh --destino root@IP [--porta-ssh 22] [--porta-app 8080]"
    echo
    echo "Roda no servidor que TEM os dados e copia tudo para o destino, preservando os ids."
    echo "Não desliga nem apaga nada no servidor atual."
}

while [ $# -gt 0 ]; do
    case "$1" in
        --destino)   DESTINO="$2"; shift 2 ;;
        --porta-ssh) PORTA_SSH="$2"; shift 2 ;;
        --porta-app) PORTA_APP="$2"; shift 2 ;;
        --repo)      REPO="$2"; shift 2 ;;
        --pasta)     PASTA_REMOTA="$2"; shift 2 ;;
        --sim)       SEM_PERGUNTA=1; shift ;;
        -h|--help)   ajuda; exit 0 ;;
        *) erro "opção desconhecida: $1" ;;
    esac
done

# ---------------------------------------------------------------------------
passo "1/8  Conferindo antes de começar"

[ "$(id -u)" -eq 0 ] || [ -n "${VODM_SEM_ROOT:-}" ] || \
    erro "rode com sudo:  sudo ./scripts/migrar.sh --destino root@IP"
[ -n "$DESTINO" ]     || erro "informe o destino:  --destino root@IP"
[ -f "$AMBIENTE" ]    || erro "$AMBIENTE não existe. Rode isto no servidor que TEM os dados."
[ -x "$BINARIO" ]     || erro "$BINARIO não existe. Rode isto no servidor que TEM os dados."
command -v ssh >/dev/null || erro "ssh não está instalado:  apt-get install -y openssh-client"

CHAVE=$(grep -oP '^VODM_ENCRYPTION_KEY=\K.*' "$AMBIENTE" || echo "")
[ -n "$CHAVE" ] || erro "não achei VODM_ENCRYPTION_KEY em $AMBIENTE. Sem ela a migração é inútil."

# Uma conexão só, reaproveitada. Sem isto o SSH pede a senha em cada um dos oito passos,
# e uma migração vira um exercício de digitação.
CANAL=$(mktemp -u /tmp/vodm-migrar-XXXXXX)
SSH=(ssh -p "$PORTA_SSH" -o ControlMaster=auto -o ControlPath="$CANAL" -o ControlPersist=30m)
SCP=(scp -P "$PORTA_SSH" -o ControlPath="$CANAL")

# Senha vinda do painel.
#
# Quando SSHPASS está no ambiente, quem digitou a senha foi a tela de Migração e não há
# terminal nenhum para o ssh perguntar. O sshpass responde ao prompt no lugar de uma pessoa.
#
# StrictHostKeyChecking=accept-new é necessário aqui e não é um relaxamento à toa: a máquina
# de destino é nova, a chave dela nunca foi vista, e sem isto o ssh faria uma pergunta que
# ninguém está lá para responder. É o mesmo "yes" que você digitaria no terminal.
if [ -n "${SSHPASS:-}" ]; then
    command -v sshpass >/dev/null || erro "sshpass não está instalado:  apt-get install -y sshpass"
    SSH=(sshpass -e "${SSH[@]}" -o StrictHostKeyChecking=accept-new)
    SCP=(sshpass -e "${SCP[@]}" -o StrictHostKeyChecking=accept-new)
fi

limpar() { ssh -O exit -o ControlPath="$CANAL" "$DESTINO" 2>/dev/null || true; }
trap limpar EXIT

echo "    conectando em $DESTINO (a senha é pedida uma vez)"
"${SSH[@]}" "$DESTINO" true || erro "não consegui conectar em $DESTINO pela porta $PORTA_SSH."

DISTRO=$("${SSH[@]}" "$DESTINO" 'command -v apt-get >/dev/null && echo apt || echo outro')
[ "$DISTRO" = "apt" ] || erro "o destino não é Ubuntu/Debian; o instalador não roda lá."

# Um destino que já tem dados é o cenário em que a migração apaga o que não devia.
JA_TEM=$("${SSH[@]}" "$DESTINO" "test -f $AMBIENTE && echo sim || echo nao")
if [ "$JA_TEM" = "sim" ]; then
    aviso "o destino JÁ tem uma instalação do VOD Manager."
    aviso "continuar SUBSTITUI os dados de lá pelos daqui."
    if [ "$SEM_PERGUNTA" = "1" ]; then
        aviso "confirmado pelo painel (--sim); seguindo."
    else
        # Sem "|| true", o set -e encerraria aqui quando a entrada acaba (um pipe, um cron)
        # — e a migração morreria sem dizer por quê.
        r=""
        read -rp "    Digite 'substituir' para continuar: " r || true
        [ "$r" = "substituir" ] || { echo "Cancelado. Nada foi alterado."; exit 1; }
    fi
fi
verde "    destino pronto para receber"

# ---------------------------------------------------------------------------
passo "2/8  Backup daqui"

ARQUIVO="$PASTA_BACKUP/migracao-$(date +%F-%H%M).tar.gz"
mkdir -p "$(dirname "$ARQUIVO")"
# As variáveis ficam num subshell: a chave não vaza para o resto do script.
(set -a; . "$AMBIENTE"; set +a; "$BINARIO" backup --arquivo "$ARQUIVO")
TAMANHO=$(du -h "$ARQUIVO" | cut -f1)
verde "    $ARQUIVO ($TAMANHO)"

# Contagens de agora, para conferir do outro lado. É isto que transforma "parece que deu
# certo" em "deu certo".
medir() {
    (set -a; . "$AMBIENTE"; set +a
     psql "$VODM_DATABASE_URL" -tAc "$1" 2>/dev/null) | tr -d '[:space:]'
}
CONTAGEM_AQUI=$(medir "SELECT count(*) FROM contents WHERE status <> 'deleted'")
MAIOR_ID_AQUI=$(medir "SELECT coalesce(max(id),0) FROM contents")

# O domínio configurado, se houver. O nginx e o certificado NÃO viajam no backup — eles
# são arquivos da máquina, não do banco. Sem detectar isso aqui, uma instalação que hoje
# atende por domínio migraria e cairia no instante em que o DNS apontasse para o destino:
# o endereço certo, numa máquina sem nginx nenhum.
BASE_PUBLICA=$(medir "SELECT value #>> '{}' FROM settings WHERE key = 'public_base_url'")
DOMINIO_ATUAL=""
if [ -n "$BASE_PUBLICA" ]; then
    hospedeiro=${BASE_PUBLICA#*://}
    hospedeiro=${hospedeiro%%/*}
    hospedeiro=${hospedeiro%%:*}
    # Um IP não é domínio: migrar não muda nada para ele além do próprio endereço.
    if ! [[ "$hospedeiro" =~ ^[0-9]+(\.[0-9]+){3}$ ]] && [ -n "$hospedeiro" ]; then
        DOMINIO_ATUAL="$hospedeiro"
    fi
fi

# Medida vazia dos dois lados compararia igual e anunciaria sucesso sem ter conferido
# nada — a pior forma de falhar, porque parece a melhor.
[ -n "$CONTAGEM_AQUI" ] && [ -n "$MAIOR_ID_AQUI" ] || \
    erro "não consegui medir o banco daqui (psql respondeu vazio). Sem isso não dá para conferir a migração."
echo "    conteúdos aqui: $CONTAGEM_AQUI   maior id: $MAIOR_ID_AQUI"

# ---------------------------------------------------------------------------
passo "3/8  Levando o código para o destino"

"${SSH[@]}" "$DESTINO" "
    set -e
    export DEBIAN_FRONTEND=noninteractive
    command -v git >/dev/null || { apt-get update -qq && apt-get install -y -qq git; }
    if [ -d '$PASTA_REMOTA/.git' ]; then
        cd '$PASTA_REMOTA' && git fetch --quiet origin && git reset --quiet --hard origin/main
    else
        git clone --quiet '$REPO' '$PASTA_REMOTA'
    fi
"
verde "    $PASTA_REMOTA"

# ---------------------------------------------------------------------------
passo "4/8  Levando a chave de criptografia"

# A chave vai ANTES de o instalador rodar. O instalador preserva a chave que encontrar em
# /etc/vodmanager.env e só gera uma nova quando não há nenhuma — então plantá-la aqui é o
# que faz o destino nascer capaz de ler os dados que vão chegar.
#
# Vai pela entrada padrão, e não como argumento: argumento aparece em `ps` para qualquer
# usuário da máquina enquanto o comando roda.
printf 'VODM_ENCRYPTION_KEY=%s\n' "$CHAVE" | \
    "${SSH[@]}" "$DESTINO" "umask 077 && cat > $AMBIENTE"
verde "    chave instalada no destino"

# ---------------------------------------------------------------------------
passo "5/8  Instalando no destino"

echo "    isto leva alguns minutos (Postgres, Go, compilação)"
# O -t dá ao instalador remoto um terminal para escrever o progresso. Ele só faz sentido
# quando ESTE lado tem terminal: disparada pelo painel, a migração não tem nenhum, e o ssh
# gastaria uma linha de aviso para dizer isso.
TTY=(-t)
[ -t 0 ] || TTY=()
"${SSH[@]}" "${TTY[@]}" "$DESTINO" "cd '$PASTA_REMOTA' && VODM_PORTA=$PORTA_APP bash scripts/instalar.sh"

# ---------------------------------------------------------------------------
passo "6/8  Enviando os dados"

REMOTO="/root/$(basename "$ARQUIVO")"
"${SCP[@]}" "$ARQUIVO" "$DESTINO:$REMOTO" >/dev/null
verde "    $REMOTO ($TAMANHO)"

# ---------------------------------------------------------------------------
passo "7/8  Restaurando no destino"

# O serviço para durante a carga: restaurar embaixo de um serviço que está escrevendo é
# pedir para as duas coisas se atrapalharem.
#
# Sem --forcar de propósito. A restauração recusa um backup cujo dono é outra chave, e
# essa recusa é a prova de que o passo 4 funcionou. Passar --forcar aqui seria desligar o
# único aviso que sobraria antes de o dano aparecer.
"${SSH[@]}" "$DESTINO" "
    set -e
    systemctl stop vodmanager
    set -a; . $AMBIENTE; set +a
    $BINARIO restaurar --arquivo '$REMOTO' --sim
    systemctl start vodmanager
"
verde "    dados restaurados"

# ---------------------------------------------------------------------------
passo "8/8  Conferindo o destino"

sleep 3
SAUDE=$("${SSH[@]}" "$DESTINO" \
    "curl -fsS http://127.0.0.1:$PORTA_APP/healthz >/dev/null 2>&1 && echo ok || echo falhou")
[ "$SAUDE" = "ok" ] || erro "o serviço não respondeu no destino. Veja lá: journalctl -u vodmanager -n 50"

# A consulta vai pela entrada padrão (psql -f -), e não embutida no comando remoto: assim
# nenhuma aspa precisa sobreviver a duas camadas de shell.
medir_la() {
    "${SSH[@]}" "$DESTINO" \
        "set -a; . $AMBIENTE; set +a; psql \"\$VODM_DATABASE_URL\" -tA -f - 2>/dev/null" \
        <<< "$1" | tr -d '[:space:]'
}
CONTAGEM_LA=$(medir_la "SELECT count(*) FROM contents WHERE status <> 'deleted';")
MAIOR_ID_LA=$(medir_la "SELECT coalesce(max(id),0) FROM contents;")

echo "    conteúdos:  aqui $CONTAGEM_AQUI   lá $CONTAGEM_LA"
echo "    maior id:   aqui $MAIOR_ID_AQUI   lá $MAIOR_ID_LA"

if [ -z "$CONTAGEM_LA" ] || [ -z "$MAIOR_ID_LA" ]; then
    vermelho "    não consegui medir o banco do destino."
    vermelho "    Os dados podem ter chegado, mas NÃO confirmei. Confira no painel antes de desligar este servidor."
    exit 1
fi

if [ "$CONTAGEM_AQUI" != "$CONTAGEM_LA" ] || [ "$MAIOR_ID_AQUI" != "$MAIOR_ID_LA" ]; then
    vermelho "    os números NÃO batem."
    vermelho "    O servidor atual continua no ar e intacto. Não desligue nada."
    exit 1
fi
verde "    os números batem: os ids foram preservados"

IP_DESTINO=${DESTINO##*@}
echo
verde "Migração concluída."
echo
echo "  Painel no destino:  http://${IP_DESTINO}:${PORTA_APP}"
echo "  Login:              o MESMO de antes (os usuários vieram no backup)"
echo
aviso "O servidor atual continua no ar. Nada foi desligado aqui."
echo
echo "  Antes de desligar este, confira no destino:"
echo "    1. entrar no painel;"
echo "    2. abrir um filme e testar o link de reprodução;"
echo "    3. conferir se as fontes ainda testam com sucesso (as credenciais foram junto)."
echo

if [ -n "$DOMINIO_ATUAL" ]; then
    # O certificado só pode ser emitido DEPOIS de o DNS apontar para cá: a Let's Encrypt
    # valida acessando o domínio, e enquanto ele resolver para o servidor antigo a emissão
    # falha. Por isso este passo não roda sozinho — a ordem importa, e ela depende de algo
    # que está fora desta máquina.
    vermelho "  ATENÇÃO: esta instalação atende pelo domínio ${DOMINIO_ATUAL}."
    echo
    echo "    O nginx e o certificado NÃO vieram no backup — eles são arquivos da máquina,"
    echo "    não do banco. O destino ainda não sabe responder por esse domínio."
    echo
    echo "    Faça nesta ordem, ou o domínio fica fora do ar:"
    echo
    echo "      1. aponte o DNS de ${DOMINIO_ATUAL} para ${IP_DESTINO}"
    echo "      2. espere resolver:  getent hosts ${DOMINIO_ATUAL}"
    echo "      3. no destino, emita o certificado:"
    echo "           ssh ${DESTINO}"
    echo "           cd ${PASTA_REMOTA} && sudo ./scripts/dominio.sh ${DOMINIO_ATUAL} SEU@EMAIL"
    echo
    echo "    Até o passo 3, use http://${IP_DESTINO}:${PORTA_APP} — a porta ${PORTA_APP}"
    echo "    nunca é fechada, justamente para você nunca ficar sem entrada."
    echo
    echo "    Os links dos seus clientes que já usam o domínio ficam IDÊNTICOS: só o DNS"
    echo "    muda de destino."
else
    echo "  Sobre os links dos seus clientes:"
    echo "    Os ids são os mesmos, mas o ENDEREÇO mudou — os links atuais apontam para este"
    echo "    servidor. Aponte o seu domínio para ${IP_DESTINO} e eles seguem sozinhos; sem"
    echo "    domínio, o endereço novo precisa ser trocado onde os links foram cadastrados."
fi
echo
