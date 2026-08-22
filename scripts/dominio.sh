#!/bin/bash
#
# Configura domínio e HTTPS para o VOD Manager.
#
# Disparado pelo painel (que apenas cria um arquivo de pedido) ou à mão:
#     sudo ./scripts/dominio.sh vod.seudominio.com voce@email.com
#
# # A decisão que governa este script
#
# A porta 8080 NUNCA é fechada, e o VODM_HTTP_ADDR nunca é alterado.
#
# O motivo é concreto: os links já entregues aos clientes contêm o IP e a porta. Fechá-la
# ao ligar o domínio derrubaria todo mundo de uma vez, e cada cliente só voltaria depois de
# receber um link novo. Com os dois caminhos vivos, a migração é gradual e ninguém cai.
#
# Fechar a 8080 é uma decisão posterior, tomada quando o administrador vir em Reproduções
# que ninguém mais usa o IP.
#
# # E se der errado
#
# O painel é a ferramenta que o administrador usaria para consertar — então quebrá-lo seria
# o pior resultado possível. Por isso: a configuração anterior é guardada, a nova é validada
# antes de entrar, e se o painel parar de responder o script volta atrás sozinho.
set -euo pipefail

# Um ou mais nomes, separados por vírgula. O primeiro é o principal: é ele que vira o
# endereço público e o que aparece nos links.
#
# Mais de um existe porque o nginx só responde pelo nome EXATO que está no server_name.
# Configurado apenas `vod.seudominio.com`, digitar `seudominio.com` não abre nada — e o
# nome curto é justamente o que se lembra às pressas, quando é preciso entrar no painel com
# urgência.
ENTRADA=""
EMAIL=""
# FORCAR_AAPANEL segue mesmo com o nginx do aaPanel no ar. Existe para quem já entendeu o
# risco e decidiu: uma trava sem porta de saída faz a pessoa procurar um caminho pior.
FORCAR_AAPANEL=0

while [ $# -gt 0 ]; do
    case "$1" in
        --mesmo-com-aapanel) FORCAR_AAPANEL=1; shift ;;
        -h|--help)
            echo "Uso:  sudo ./scripts/dominio.sh dominio[,outro,...] [email] [--mesmo-com-aapanel]"
            exit 0 ;;
        *)
            if [ -z "$ENTRADA" ]; then ENTRADA="$1"
            elif [ -z "$EMAIL" ]; then EMAIL="$1"
            fi
            shift ;;
    esac
done
AMBIENTE=/etc/vodmanager.env
SITE=/etc/nginx/sites-available/vodmanager
LINK=/etc/nginx/sites-enabled/vodmanager
REGISTRO=/opt/vodmanager/runtime/ultimo-dominio.log

passo()   { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
ok()      { printf '\033[32m    %s\033[0m\n' "$*"; }
aviso()   { printf '\033[33m    %s\033[0m\n' "$*"; }
erro()    { printf '\n\033[31merro: %s\033[0m\n' "$*" >&2; }

# A checagem de root vem ANTES do registro em arquivo: sem ela, uma execução sem sudo
# falha no mkdir com "permission denied", que não diz o que fazer.
[ "$(id -u)" -eq 0 ] || { erro "rode com sudo."; exit 1; }

mkdir -p /opt/vodmanager/runtime
# O dono e o servico, nao o root: e dele que partem os pedidos gravados aqui.
chown vodmanager:vodmanager /opt/vodmanager/runtime 2>/dev/null || true
exec > >(tee "$REGISTRO") 2>&1
chown vodmanager:vodmanager "$REGISTRO" 2>/dev/null || true
[ -n "$ENTRADA" ] || { erro "informe o domínio."; exit 2; }

# Um domínio malformado produziria uma configuração que o nginx recusa, ou pior, uma que
# ele aceita e que não corresponde a nada.
nome_valido() {
    printf '%s' "$1" | grep -qE '^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$'
}

PEDIDOS=()
for nome in ${ENTRADA//,/ }; do
    nome=$(printf '%s' "$nome" | tr 'A-Z' 'a-z')
    [ -n "$nome" ] || continue
    nome_valido "$nome" || { erro "'$nome' não parece um domínio válido."; exit 2; }
    PEDIDOS+=("$nome")
done
[ ${#PEDIDOS[@]} -gt 0 ] || { erro "informe o domínio."; exit 2; }

# DOMINIO continua sendo o principal: o endereço público, os links e a verificação final
# falam dele. Os outros nomes são atalhos que levam ao mesmo lugar.
DOMINIO="${PEDIDOS[0]}"

PORTA=$(grep -oP 'VODM_HTTP_ADDR=.*:\K[0-9]+' "$AMBIENTE" 2>/dev/null || echo 8080)

# ---------------------------------------------------------------------------
passo "1/6  Conferindo se o domínio aponta para esta máquina"

MEU_IP=$(curl -fsS --max-time 8 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')
IP_DOMINIO=$(getent ahostsv4 "$DOMINIO" 2>/dev/null | awk '{print $1; exit}' || echo "")

if [ -z "$IP_DOMINIO" ]; then
    erro "o domínio $DOMINIO não resolve para nenhum endereço.
       Crie um registro A apontando para $MEU_IP e aguarde a propagação."
    exit 1
fi
# atrasDeCloudflare reconhece as faixas mais comuns do proxy da Cloudflare.
#
# Não é uma lista completa — é o suficiente para transformar "os IPs não batem" numa
# mensagem que diz o que fazer. Um domínio atrás do proxy resolve para a Cloudflare, nunca
# para a máquina de origem, e é a causa mais comum desta checagem falhar.
atrasDeCloudflare() {
    case "$1" in
        104.1[6-9].*|104.2[0-7].*|172.6[4-9].*|172.7[0-1].*|162.15[89].*|        188.114.9[6-9].*|188.114.1*|141.101.*|108.162.*|190.93.*|197.234.24*|        198.41.1[2-9]*|173.245.4*|131.0.7[2-5].*) return 0 ;;
        *) return 1 ;;
    esac
}

if [ "$IP_DOMINIO" != "$MEU_IP" ]; then
    if atrasDeCloudflare "$IP_DOMINIO"; then
        erro "o domínio $DOMINIO está atrás do proxy da Cloudflare ($IP_DOMINIO).

       No painel da Cloudflare, no registro deste subdomínio, troque o ícone LARANJA
       por CINZA ('DNS only'). Depois espere propagar e rode de novo.

       Isto não é só burocracia do certificado: com o proxy ligado, TODO o vídeo passaria
       pela Cloudflare, e o plano gratuito deles proíbe isso — funciona por um tempo e
       depois vem bloqueio. Para o painel administrativo o proxy é aceitável, porque o
       tráfego é pequeno; para o conteúdo, não."
        exit 1
    fi
    erro "o domínio $DOMINIO aponta para $IP_DOMINIO, e esta máquina é $MEU_IP.
       O certificado não seria emitido: a autoridade certificadora precisa alcançar
       ESTA máquina pelo domínio. Corrija o registro A e tente de novo."
    exit 1
fi
ok "$DOMINIO → $MEU_IP"

# Os nomes de atalho.
#
# Além dos que foram pedidos, tentamos sozinhos o domínio raiz e o www — são os que a pessoa
# digita de memória quando precisa entrar às pressas, e não ter pensado neles é o que faz
# `seudominio.com` não abrir nada enquanto `vod.seudominio.com` abre.
#
# Só entra o que APONTA PARA ESTA MÁQUINA. Um nome que resolve para outro lugar (ou que não
# resolve) faria a emissão do certificado falhar inteira, derrubando junto o nome principal
# que estava correto — um atalho conveniente não vale esse preço.
candidatos=("${PEDIDOS[@]}")
raiz=$(printf '%s' "$DOMINIO" | awk -F. '{ if (NF>2) print $(NF-1)"."$NF; }')
if [ -n "$raiz" ]; then
    candidatos+=("$raiz" "www.$raiz")
fi
candidatos+=("www.$DOMINIO")

DOMINIOS=()
IGNORADOS=()
for nome in "${candidatos[@]}"; do
    # Sem repetir: certbot recusa a mesma -d duas vezes, e o nginx avisa sobre server_name
    # duplicado.
    ja=0
    for existente in "${DOMINIOS[@]:-}"; do
        [ "$existente" = "$nome" ] && ja=1 && break
    done
    [ "$ja" -eq 1 ] && continue

    ip=$(getent ahostsv4 "$nome" 2>/dev/null | awk '{print $1; exit}' || echo "")
    if [ "$ip" = "$MEU_IP" ]; then
        DOMINIOS+=("$nome")
        [ "$nome" = "$DOMINIO" ] || ok "atalho: $nome → $MEU_IP"
    else
        IGNORADOS+=("$nome")
    fi
done

for nome in "${IGNORADOS[@]:-}"; do
    [ -n "$nome" ] || continue
    # Só vale avisar sobre o que a pessoa PEDIU. Os candidatos automáticos que não existem
    # são o caso normal, não um problema.
    for pedido in "${PEDIDOS[@]}"; do
        if [ "$pedido" = "$nome" ]; then
            aviso "$nome não aponta para esta máquina; ficou de fora"
        fi
    done
done

# ---------------------------------------------------------------------------
passo "2/6  Instalando nginx e certbot, se faltarem"

export DEBIAN_FRONTEND=noninteractive

# Um nginx que não é o do sistema já ocupa as portas 80 e 443.
#
# É o caso do aaPanel, que compila o próprio nginx em /www/server/nginx e não instala o
# pacote do apt. Instalar o pacote aqui produziria DOIS nginx na máquina: o novo não subiria
# (a porta 80 já está tomada), o certbot ajustaria a configuração do que não roda, e o
# domínio continuaria sem responder — com todos os comandos tendo "dado certo".
#
# Pior: o site que já existia no aaPanel poderia cair no meio disso. Então paramos antes, e
# dizemos onde está o caminho que funciona.
# O que atrapalha não é o aaPanel ESTAR instalado — é o nginx dele estar SEGURANDO a porta.
#
# Bloquear pela simples presença da pasta impediria o caminho direto em máquinas onde o
# aaPanel veio junto da imagem e nunca serviu nada. Uma trava que recusa o que funcionaria é
# tão ruim quanto não ter trava nenhuma: as duas fazem a pessoa procurar outro jeito.
aapanel_instalado() {
    [ -d /www/server/panel ] || [ -x /www/server/nginx/sbin/nginx ]
}

# aapanel_nginx_ativo pergunta ao sistema, e não ao disco.
aapanel_nginx_ativo() {
    pgrep -f '/www/server/nginx' >/dev/null 2>&1 && return 0
    # Alguém segurando a 80 que não é o nginx do apt tem o mesmo efeito prático.
    ss -ltnp 2>/dev/null | grep -qE ':80\s' && ! systemctl is-active --quiet nginx 2>/dev/null
}

# A trava é pela PRESENÇA do aaPanel, não por ele estar no ar neste instante.
#
# Parece exagero e não é. O aaPanel tem vigia próprio: um nginx parado agora volta sozinho
# depois. Liberar o caminho direto porque a porta está livre neste segundo instala o nginx
# do sistema, funciona por uns dias, e quebra quando o aaPanel ressuscitar o dele — sem
# nada ligar o sintoma à decisão tomada dias antes.
#
# E numa máquina com aaPanel o caminho por ele é melhor de qualquer forma: é ele quem
# renova o certificado, e é nele que a pessoa vai procurar o site depois.
if aapanel_instalado && [ "$FORCAR_AAPANEL" != "1" ]; then
    if aapanel_nginx_ativo; then
        detalhe="o nginx dele está no ar e já responde pela porta 80"
    else
        detalhe="o nginx dele está parado agora, mas o aaPanel o traz de volta sozinho"
    fi
    erro "esta máquina tem o aaPanel — $detalhe.

       Este script instalaria o nginx do sistema por cima, e os dois brigariam pela mesma
       porta — o domínio não responderia, e os sites que já existem no aaPanel poderiam
       cair. Com todos os comandos tendo 'dado certo'.

       Há dois caminhos, e os dois funcionam:

       A) Configurar o domínio PELO aaPanel (não derruba nada do que está lá):
            Website -> Add site -> (o seu domínio), PHP: Static
            Website -> o site -> Config file: apontar para http://127.0.0.1:${PORTA}
            Website -> o site -> SSL -> Let's Encrypt
          O bloco pronto para colar está na aba Sistema do painel, com botão de copiar.
          O passo a passo completo: docs/16-hospedar-pelo-aapanel.md

       B) Se o aaPanel não serve nada que você use, DESINSTALE o nginx dele pela loja do
          próprio aaPanel (App Store -> Nginx -> Uninstall) e rode este script de novo.
          Só parar o processo não basta: o aaPanel o traz de volta sozinho, e o domínio
          cairia dias depois sem nada explicar por quê.

       Se você já sabe disso e quer seguir mesmo assim, use --mesmo-com-aapanel."
    exit 1
fi

if ! command -v nginx >/dev/null || ! command -v certbot >/dev/null; then
    apt-get update -qq
    apt-get install -y -qq nginx certbot python3-certbot-nginx >/dev/null
fi
ok "$(nginx -v 2>&1) · certbot presente"

# ---------------------------------------------------------------------------
passo "3/6  Guardando a configuração atual"

ANTERIOR=""
if [ -f "$SITE" ]; then
    ANTERIOR="${SITE}.anterior"
    cp -p "$SITE" "$ANTERIOR"
    ok "$ANTERIOR"
else
    ok "não havia configuração anterior"
fi

# ---------------------------------------------------------------------------
passo "4/6  Escrevendo a configuração"

cat > "$SITE" <<NGINX
# Gerado pelo VOD Manager. Alterações à mão são substituídas na próxima execução.
server {
    listen 80;
    server_name ${DOMINIOS[*]};

    location / {
        proxy_pass http://127.0.0.1:${PORTA};
        proxy_http_version 1.1;
        proxy_set_header Host              \$host;
        proxy_set_header X-Real-IP         \$remote_addr;
        proxy_set_header X-Forwarded-For   \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # Vídeo é resposta longa. Com buffering ligado o nginx tenta acumular o filme
        # inteiro antes de repassar, e sem os tempos abaixo ele corta a transmissão no
        # meio — os dois sintomas mais difíceis de diagnosticar depois.
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 1h;
        proxy_send_timeout 1h;
        client_max_body_size 0;
    }
}
NGINX

ln -sf "$SITE" "$LINK"

if ! nginx -t 2>/dev/null; then
    erro "a configuração gerada foi recusada pelo nginx. Nada foi aplicado."
    nginx -t || true
    if [ -n "$ANTERIOR" ]; then cp -p "$ANTERIOR" "$SITE"; else rm -f "$SITE" "$LINK"; fi
    exit 1
fi
systemctl reload nginx
ok "nginx recarregado"

# ---------------------------------------------------------------------------
passo "5/6  Emitindo o certificado"

if [ -z "$EMAIL" ]; then
    aviso "sem e-mail informado; emitindo sem aviso de expiração"
    REGISTRO_EMAIL="--register-unsafely-without-email"
else
    REGISTRO_EMAIL="--email $EMAIL"
fi

# Um -d por nome: o certificado precisa cobrir todos, senão o navegador recusa o atalho com
# um aviso de segurança — pior que o atalho não existir.
ALVOS=()
for nome in "${DOMINIOS[@]}"; do
    ALVOS+=(-d "$nome")
done

# --nginx ajusta o próprio site para 443 e redireciona o 80.
if certbot --nginx "${ALVOS[@]}" --non-interactive --agree-tos $REGISTRO_EMAIL --redirect >/dev/null 2>&1; then
    ok "HTTPS ativo em https://${DOMINIO}"
    ESQUEMA=https
else
    aviso "não foi possível emitir o certificado; o domínio segue em HTTP"
    aviso "veja o motivo em /var/log/letsencrypt/letsencrypt.log"
    ESQUEMA=http
fi

# ---------------------------------------------------------------------------
# Avisar a aplicação de que agora existe um proxy na frente dela.
#
# Sem isto, TODA requisição vinda pelo domínio chega com o endereço 127.0.0.1 — que é o
# nginx, não o espectador. As consequências não aparecem como erro, e é por isso que elas
# demoram a ser notadas:
#
#   - o limite de tentativas de login passa a ser compartilhado por todo mundo que entra
#     pelo domínio: alguém errando a senha tranca os outros;
#   - o limite de telas simultâneas por credencial deixa de distinguir espectadores, porque
#     todos aparecem com o mesmo IP;
#   - o registro de falhas de reprodução aponta sempre para a própria máquina, e some a
#     informação que diria de onde veio o problema.
#
# TRUST_PROXY só faz o X-Forwarded-For ser aceito quando o vizinho imediato é o loopback —
# ou seja, quando quem entregou foi este nginx. Quem chega direto na porta da aplicação não
# consegue forjar o próprio IP com ele.
ajustar_ambiente() {
    local chave="$1" valor="$2"
    if grep -q "^${chave}=" "$AMBIENTE"; then
        sed -i "s|^${chave}=.*|${chave}=${valor}|" "$AMBIENTE"
    else
        printf '%s=%s\n' "$chave" "$valor" >> "$AMBIENTE"
    fi
}

# O cookie de sessão NÃO é forçado a Secure aqui, de propósito. Ele já passa a ser marcado
# como seguro sozinho nas requisições que chegam por HTTPS, e forçá-lo globalmente
# quebraria a entrada por http://IP:PORTA — que é exatamente a saída de emergência que
# nunca se fecha.
ajustar_ambiente VODM_TRUST_PROXY true
systemctl restart vodmanager
ok "o serviço passou a enxergar o IP real de quem acessa"

# ---------------------------------------------------------------------------
passo "6/6  Conferindo que o painel responde pelo domínio"

ufw allow 'Nginx Full' >/dev/null 2>&1 || true

OK=0
for _ in $(seq 1 15); do
    if curl -fsS --max-time 5 "${ESQUEMA}://${DOMINIO}/healthz" >/dev/null 2>&1; then OK=1; break; fi
    sleep 2
done

if [ "$OK" -ne 1 ]; then
    erro "o painel não respondeu por ${ESQUEMA}://${DOMINIO}. Voltando atrás."
    if [ -n "$ANTERIOR" ]; then
        cp -p "$ANTERIOR" "$SITE"
    else
        rm -f "$SITE" "$LINK"
    fi
    nginx -t >/dev/null 2>&1 && systemctl reload nginx
    erro "a configuração anterior foi restaurada. O acesso pelo IP continua funcionando."
    exit 1
fi

# O endereço público passa a ser o domínio.
#
# Sem isto, o domínio funciona e os LINKS continuam saindo com o endereço antigo — que
# depois de uma migração é o IP da máquina que ficou para trás. O painel abre, o catálogo
# aparece, e o vídeo não toca: o sintoma mais confuso possível, porque tudo o que se olha
# está certo.
#
# Os links já entregues não mudam nem quebram: a porta ${PORTA} continua aberta. O que muda
# é o que sai daqui em diante — as listas M3U e a API Xtream passam a entregar o domínio.
if sudo -u postgres psql -d vodmanager -qc \
    "INSERT INTO settings (key, value) VALUES ('public_base_url', to_jsonb('${ESQUEMA}://${DOMINIO}'::text))
     ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now();" >/dev/null 2>&1; then
    ok "endereço público: ${ESQUEMA}://${DOMINIO}"
else
    aviso "não consegui gravar o endereço público; ajuste em Configurações para"
    aviso "${ESQUEMA}://${DOMINIO} — sem isso os links continuam com o endereço antigo."
fi

cat <<FIM

$(ok "Domínio configurado.")

  Painel e conteúdo ..... ${ESQUEMA}://${DOMINIO}
  Também respondem por .. ${DOMINIOS[*]}
  Acesso pelo IP ........ continua funcionando na porta ${PORTA}

A porta ${PORTA} foi deixada ABERTA de propósito: os links que os seus clientes já têm
apontam para o IP, e fechá-la agora derrubaria todos de uma vez.

Migre os clientes aos poucos — em Credenciais > Lista o endereço já sai com o domínio.
Quando ninguém mais aparecer com o IP em Reproduções, aí sim vale fechar:

    sudo ufw delete allow ${PORTA}/tcp

FIM
