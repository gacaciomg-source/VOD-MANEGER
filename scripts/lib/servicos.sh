#!/bin/bash
#
# Unidades systemd, pasta de trabalho e firewall do VOD Manager — num lugar só.
#
# # Por que este arquivo existe
#
# O instalador e o atualizador precisam produzir EXATAMENTE a mesma instalação. Enquanto
# as unidades viviam só dentro do instalador, atualizar pelo painel trocava o binário mas
# não trazia nada do que a versão nova precisasse do sistema: uma unidade nova, uma pasta
# nova, uma permissão nova. O resultado era o conselho "melhor rodar o instalador de novo"
# — ou seja, um botão de atualizar que não atualizava.
#
# Com as unidades aqui, o atualizador reaplica a mesma configuração que o instalador
# aplicaria, e o botão passa a valer por si.
#
# Todas as funções são IDEMPOTENTES: rodar de novo não estraga nada.
#
# Quem usa este arquivo define antes:
#   FONTE     pasta do código-fonte clonado
#   DESTINO   caminho do binário instalado
#   AMBIENTE  arquivo de variáveis (/etc/vodmanager.env)
#   SERVICO   nome do serviço (vodmanager)

# vodm_pasta_runtime cria a pasta gravável do serviço.
#
# O serviço precisa ESCREVER para pedir atualização, domínio ou migração ao systemd.
# Deixar /opt/vodmanager inteira gravável resolveria — e daria ao processo exposto na
# internet o poder de substituir o próprio binário. Então a escrita fica confinada aqui.
#
# O ReadWritePaths da unidade NÃO basta: ele levanta a proteção do systemd, não a permissão
# do sistema de arquivos. São duas travas, e abrir só uma dá "permission denied".
vodm_pasta_runtime() {
    mkdir -p /opt/vodmanager/runtime
    chown vodmanager:vodmanager /opt/vodmanager/runtime
    chmod 0750 /opt/vodmanager/runtime
}

# vodm_unidades escreve todas as unidades systemd e as habilita.
#
# # O mecanismo dos botões do painel
#
# O serviço roda como o usuário vodmanager, que não pode reiniciar serviços nem escrever
# fora da pasta de trabalho — e roda com NoNewPrivileges, que impede até o sudo de elevar.
# Ambas as restrições são boas e ficam.
#
# Em vez de abrir uma exceção nelas, o sentido é invertido: o painel apenas CRIA UM ARQUIVO
# dentro da pasta onde já pode escrever, e o systemd — que é root — observa esse arquivo e
# dispara o script. O processo nunca ganha privilégio nenhum e não escolhe o que roda: só
# pede que aconteça o que o root já definiu aqui.
vodm_unidades() {
    cat > /etc/systemd/system/${SERVICO}.service <<UNIT
[Unit]
Description=VOD Manager
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=vodmanager
Group=vodmanager
EnvironmentFile=${AMBIENTE}
ExecStart=${DESTINO}
Restart=always
RestartSec=5

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/vodmanager/runtime

# Streaming abre muitos descritores ao mesmo tempo; sem isto o serviço trava com
# "Too many open files" justamente quando houver audiência.
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
UNIT

    # Atualização pelo painel.
    #
    # WorkingDirectory é obrigatório e já foi a causa de o botão falhar: sem ele o systemd
    # roda o script a partir de /, onde não há go.mod, e o atualizador para na primeira
    # verificação com "rode de dentro da pasta do código" — uma mensagem correta e
    # completamente inútil para quem só clicou num botão.
    cat > /etc/systemd/system/vodmanager-update.service <<UNIT
[Unit]
Description=Atualização do VOD Manager (disparada pelo painel)

[Service]
Type=oneshot
WorkingDirectory=${FONTE}
# HOME não é definido pelo systemd em serviços de sistema, e o Go deriva dele o lugar do
# cache de módulos. Sem esta linha o build morre com "module cache not found: neither
# GOMODCACHE nor GOPATH is set" — uma mensagem que não tem nada a ver com o projeto e que
# só aparece pelo botão do painel, nunca pelo terminal, onde o sudo define o HOME.
#
# O git também precisa dele para achar a configuração.
Environment=HOME=/root
Environment=PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# O pedido é consumido ANTES de começar: se o script falhar, o arquivo já não existe e a
# atualização não fica repetindo em laço.
ExecStartPre=/bin/rm -f /opt/vodmanager/runtime/solicitar-atualizacao
ExecStart=/bin/bash ${FONTE}/scripts/atualizar.sh
# Compilar o projeto inteiro numa VPS pequena passa dos 5 minutos do padrão.
TimeoutStartSec=3600
UNIT

    cat > /etc/systemd/system/vodmanager-update.path <<UNIT
[Unit]
Description=Observa o pedido de atualização vindo do painel

[Path]
PathExists=/opt/vodmanager/runtime/solicitar-atualizacao

[Install]
WantedBy=multi-user.target
UNIT

    # Configuração de domínio pelo painel.
    cat > /etc/systemd/system/vodmanager-domain.service <<UNIT
[Unit]
Description=Configuração de domínio do VOD Manager (disparada pelo painel)

[Service]
Type=oneshot
WorkingDirectory=${FONTE}
# Sem HOME, o certbot e o git procuram configuração num lugar que não existe.
Environment=HOME=/root
Environment=PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# O pedido carrega o domínio e o e-mail, e é consumido ANTES de começar: se o script
# falhar, o arquivo já não existe e a tarefa não fica repetindo em laço.
ExecStart=/bin/bash -c 'read -r d e < /opt/vodmanager/runtime/solicitar-dominio; rm -f /opt/vodmanager/runtime/solicitar-dominio; ${FONTE}/scripts/dominio.sh "\$d" "\$e"'
TimeoutStartSec=1800
UNIT

    cat > /etc/systemd/system/vodmanager-domain.path <<UNIT
[Unit]
Description=Observa o pedido de domínio vindo do painel

[Path]
PathExists=/opt/vodmanager/runtime/solicitar-dominio

[Install]
WantedBy=multi-user.target
UNIT

    # Migração para outra máquina pelo painel.
    #
    # O pedido traz a senha do SSH do destino. Ele é apagado antes de o script começar, e o
    # conteúdo viaja pela entrada padrão — nunca como argumento, que apareceria no `ps`
    # para qualquer usuário da máquina enquanto a migração roda.
    cat > /etc/systemd/system/vodmanager-migrar.service <<UNIT
[Unit]
Description=Migração do VOD Manager para outra máquina (disparada pelo painel)

[Service]
Type=oneshot
WorkingDirectory=${FONTE}
# O ssh guarda em HOME o arquivo de hosts conhecidos. Sem ele, a primeira conexão com a
# máquina de destino não teria onde registrar a chave dela.
Environment=HOME=/root
Environment=PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/bin/bash ${FONTE}/scripts/migrar_pedido.sh
# Copiar o catálogo e compilar no destino leva bem mais que o padrão de 5 minutos.
TimeoutStartSec=7200
UNIT

    cat > /etc/systemd/system/vodmanager-migrar.path <<UNIT
[Unit]
Description=Observa o pedido de migração vindo do painel

[Path]
PathExists=/opt/vodmanager/runtime/solicitar-migracao

[Install]
WantedBy=multi-user.target
UNIT

    rm -f /etc/sudoers.d/vodmanager   # de versões anteriores deste instalador
    systemctl daemon-reload
    systemctl enable --now --quiet vodmanager-update.path
    systemctl enable --now --quiet vodmanager-domain.path
    systemctl enable --now --quiet vodmanager-migrar.path
}

# vodm_outro_firewall_manda diz se outro programa já é o dono do firewall desta máquina.
#
# O aaPanel e o firewalld gerenciam as regras por conta própria. Ligar o ufw por cima
# significa trocar o conjunto de regras inteiro pelo nosso — e o nosso só conhece o SSH e a
# porta da aplicação.
vodm_outro_firewall_manda() {
    [ -d /www/server/panel ] && return 0
    systemctl is-active --quiet firewalld 2>/dev/null && return 0
    return 1
}

# vodm_portas_expostas lista as portas TCP que hoje escutam para fora.
#
# Serve para não cortar nada ao ligar o ufw pela primeira vez. Só as que escutam em todas as
# interfaces entram: uma porta em 127.0.0.1 (o Postgres, por exemplo) não é alcançável de
# fora e não tem por que ser aberta.
vodm_portas_expostas() {
    ss -ltnH 2>/dev/null \
        | awk '{print $4}' \
        | grep -E '^(0\.0\.0\.0|\[::\]|\*):' \
        | sed 's/.*://' \
        | sort -un
}

# vodm_firewall libera o SSH e a porta da aplicação.
#
# # Por que este trecho tem tanto cuidado
#
# Ligar o ufw numa máquina que já servia outras coisas é a maneira mais fácil de derrubar o
# que estava funcionando: o ufw entra com "negar tudo o que não foi listado", e a lista era
# só SSH e a porta da aplicação. Numa VPS com aaPanel, isso fecha de uma vez as portas 80 e
# 443 dos sites hospedados e a porta do próprio painel — inclusive a que a pessoa usaria
# para reabrir. O comando termina com sucesso, e o estrago só aparece quando alguém tenta
# acessar.
#
# Então: se outro programa manda no firewall, não encostamos nele. E, quando somos nós a
# ligar o ufw pela primeira vez, o que já estava exposto continua exposto.
vodm_firewall() {
    local porta="$1" p

    local ativo=0
    ufw status 2>/dev/null | grep -q "Status: active" && ativo=1

    if [ "$ativo" -eq 0 ] && vodm_outro_firewall_manda; then
        echo "    outro programa (aaPanel ou firewalld) gerencia o firewall desta máquina;"
        echo "    o ufw não foi ligado. Libere a porta ${porta}/tcp por lá."
        return 0
    fi

    ufw allow OpenSSH >/dev/null 2>&1 || true
    ufw allow "${porta}/tcp" >/dev/null 2>&1 || true
    # 80 e 443 entram desde já: no dia em que um domínio for configurado, elas passam a ser
    # o caminho principal — e descobrir que estavam fechadas com o certificado já emitido
    # é uma depuração cara por um motivo bobo.
    ufw allow 80/tcp >/dev/null 2>&1 || true
    ufw allow 443/tcp >/dev/null 2>&1 || true

    if [ "$ativo" -eq 0 ]; then
        for p in $(vodm_portas_expostas); do
            ufw allow "${p}/tcp" >/dev/null 2>&1 || true
        done
    fi

    ufw --force enable >/dev/null 2>&1 || true
}

# vodm_pacotes instala apenas o que estiver faltando.
#
# Só chama o apt-get quando há algo a instalar: numa atualização, o caso comum é não faltar
# nada, e um `apt-get update` desnecessário acrescenta um minuto a cada clique no botão.
vodm_pacotes() {
    local faltando=()
    local p
    for p in "$@"; do
        dpkg -s "$p" >/dev/null 2>&1 || faltando+=("$p")
    done
    [ ${#faltando[@]} -eq 0 ] && return 0
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq "${faltando[@]}" >/dev/null
}

# vodm_atualizar_fonte traz a versão nova do repositório para a pasta do código.
#
# Descarta alterações locais de propósito: a pasta do código é uma cópia de trabalho do
# sistema, não um lugar para editar. Um arquivo alterado à mão ali travaria o `pull` e
# faria o botão de atualizar falhar por um motivo que ninguém lembra.
# O botão atualiza a LINHA EM QUE A MÁQUINA ESTÁ, e não sempre a principal.
#
# Antes ele forçava `origin/main`. Numa máquina posta numa branch para testar um recurso, o
# botão a arrastava de volta para a principal — em silêncio, e com a aparência de que o
# recurso tinha sumido. Quem experimenta algo assim conclui, com razão, que não pode usar o
# botão; e um botão em que não se confia é um botão que não existe.
#
# Ficar na branch é o que se pede a quem está testando. Ele precisa poder atualizar sem
# perder o lugar.
vodm_atualizar_fonte() {
    local pasta="$1" repo="$2"
    if [ ! -d "$pasta/.git" ]; then
        rm -rf "$pasta"
        git clone --quiet "$repo" "$pasta"
        return
    fi
    git -C "$pasta" fetch --quiet --all --tags --prune

    local atual
    atual=$(git -C "$pasta" symbolic-ref --quiet --short HEAD 2>/dev/null || echo "")

    # HEAD solto (uma tag, um commit específico) não tem linha para seguir. Nesse caso a
    # principal é o destino certo: quem fixou um commit à mão não usaria o botão para sair
    # dele sem querer.
    if [ -z "$atual" ] || ! git -C "$pasta" rev-parse --verify --quiet "origin/$atual" >/dev/null; then
        atual=$(git -C "$pasta" symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null \
                | sed 's|^origin/||')
        [ -n "$atual" ] || atual="main"
        echo "    linha: $atual (a principal)"
    else
        echo "    linha: $atual"
    fi

    git -C "$pasta" checkout --quiet "$atual" 2>/dev/null || true
    git -C "$pasta" reset --quiet --hard "origin/$atual"
}
